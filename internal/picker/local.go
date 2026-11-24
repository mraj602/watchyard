package picker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

// LocalSample is one limit_sample the status line wrote on this machine (record.md 4.2).
type LocalSample struct {
	At                    time.Time
	Slot                  string
	Account, Org          string
	Five, Seven           *float64
	FiveReset, SevenReset *time.Time
}

// TokenUse is one API response in this machine's token rows (record.md 4.4), merged over its
// streaming snapshots (the largest value of each counter), in weighted units.
type TokenUse struct {
	At      time.Time
	Session string
	Slot    string // the record's slot ("" when the transcripts folder is shared)
	Units   float64
	// Model is the response's model; Agent the helper (subagent) that made it, "" for the
	// session itself (wy burn).
	Model, Agent string
}

// HookSession is what this machine's hook events say about one session: its newest event,
// whether that event ended it, and the folder and login it ran under.
type HookSession struct {
	Last         time.Time
	Ended        bool
	Slot         string
	Account, Org string
}

// Outbox is what the picker reads from this machine's outbox.
type Outbox struct {
	Samples  []LocalSample          // oldest first
	Tokens   []TokenUse             // oldest first
	Sessions map[string]HookSession // by session id
}

// Units is the weighted token units of setup.md 8.5: input + 5 × output + 1.25 × cache_5m +
// 2 × cache_1h + 0.1 × cache_read.
func Units(input, output, cache5m, cache1h, cacheRead int64) float64 {
	return float64(input) + 5*float64(output) + 1.25*float64(cache5m) + 2*float64(cache1h) + 0.1*float64(cacheRead)
}

var (
	dayFileRe   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl$`)
	spillFileRe = regexp.MustCompile(`^spill-\d+-[0-9A-Za-z]+\.jsonl$`)
	limitKind   = []byte(`"kind":"` + record.KindLimitSample + `"`)
	tokenKind   = []byte(`"kind":"` + record.KindTokenRows + `"`)
	hookKind    = []byte(`"kind":"` + record.KindHookEvent + `"`)
)

// ReadLocal reads the limit samples in the outbox that are not older than since.
func ReadLocal(outboxDir string, since time.Time) ([]LocalSample, error) {
	o, err := ReadOutbox(outboxDir, since, false)
	return o.Samples, err
}

// ReadOutbox reads the outbox once (day files from the day before since on, and spill files)
// for records not older than since: limit samples, and with meter also token rows and hook
// events. The outbox keeps sent records for outbox.keep_days, so this works with or without
// a hub.
func ReadOutbox(outboxDir string, since time.Time, meter bool) (Outbox, error) {
	out := Outbox{Sessions: map[string]HookSession{}}
	ents, err := os.ReadDir(outboxDir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	cutoff := since.UTC().Add(-24 * time.Hour).Format("2006-01-02")
	var names []string
	for _, e := range ents {
		n := e.Name()
		if m := dayFileRe.FindStringSubmatch(n); m != nil && m[1] >= cutoff || spillFileRe.MatchString(n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	type counts struct {
		at                  time.Time
		session, slot       string
		model, agent        string
		in, out, c5, c1, cr int64
	}
	tokens := map[string]*counts{}
	for _, n := range names {
		f, err := os.Open(filepath.Join(outboxDir, n))
		if err != nil {
			continue // a spill file drained while we listed
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), record.MaxEncodedBytes+1)
		for sc.Scan() {
			line := sc.Bytes()
			head := line
			if len(head) > 300 {
				head = head[:300]
			}
			isLimit := bytes.Contains(head, limitKind)
			isToken := meter && bytes.Contains(head, tokenKind)
			isHook := meter && bytes.Contains(head, hookKind)
			if !isLimit && !isToken && !isHook {
				continue
			}
			var r record.Record
			if json.Unmarshal(line, &r) != nil {
				continue
			}
			ts, err := r.Time()
			if err != nil {
				continue
			}
			switch {
			case r.Kind == record.KindLimitSample && isLimit:
				if ts.Before(since) {
					continue
				}
				s := LocalSample{At: ts, Slot: r.Slot, Five: derivedPct(r, "five_hour_pct"), Seven: derivedPct(r, "seven_day_pct"),
					FiveReset: derivedTime(r, "five_hour_resets_at"), SevenReset: derivedTime(r, "seven_day_resets_at")}
				if r.Seat != nil {
					s.Account, s.Org = r.Seat.Account, r.Seat.Org
				}
				if s.Five != nil || s.Seven != nil {
					out.Samples = append(out.Samples, s)
				}
			case r.Kind == record.KindTokenRows && isToken:
				var p struct {
					Rows []record.TokenRow `json:"rows"`
				}
				if json.Unmarshal(r.Payload, &p) != nil {
					continue
				}
				for _, row := range p.Rows {
					at, err := time.Parse(time.RFC3339Nano, row.TS)
					if err != nil || at.Before(since) || row.Key == "" || row.SessionID == "" {
						continue
					}
					c := tokens[row.Key]
					if c == nil {
						c = &counts{at: at, session: row.SessionID, slot: r.Slot, model: row.Model, agent: row.AgentID}
						tokens[row.Key] = c
					}
					c.in, c.out = max(c.in, row.Input), max(c.out, row.Output)
					c.c5, c.c1, c.cr = max(c.c5, row.Cache5m), max(c.c1, row.Cache1h), max(c.cr, row.CacheRead)
					if c.slot == "" {
						c.slot = r.Slot
					}
				}
			case r.Kind == record.KindHookEvent && isHook:
				if ts.Before(since) {
					continue
				}
				var p struct {
					Event   string `json:"hook_event_name"`
					Session string `json:"session_id"`
				}
				if json.Unmarshal(r.Payload, &p) != nil || p.Session == "" {
					continue
				}
				hs := out.Sessions[p.Session]
				if !ts.Before(hs.Last) {
					hs.Last, hs.Ended = ts, p.Event == "SessionEnd"
					if r.Slot != "" {
						hs.Slot = r.Slot
					}
					if r.Seat != nil && r.Seat.Account != "" {
						hs.Account, hs.Org = r.Seat.Account, r.Seat.Org
					}
				} else if hs.Account == "" && r.Seat != nil {
					hs.Account, hs.Org = r.Seat.Account, r.Seat.Org
				}
				out.Sessions[p.Session] = hs
			}
		}
		f.Close()
	}
	sort.SliceStable(out.Samples, func(i, j int) bool { return out.Samples[i].At.Before(out.Samples[j].At) })
	for _, c := range tokens {
		out.Tokens = append(out.Tokens, TokenUse{At: c.at, Session: c.session, Slot: c.slot, Units: Units(c.in, c.out, c.c5, c.c1, c.cr),
			Model: c.model, Agent: c.agent})
	}
	sort.SliceStable(out.Tokens, func(i, j int) bool {
		if !out.Tokens[i].At.Equal(out.Tokens[j].At) {
			return out.Tokens[i].At.Before(out.Tokens[j].At)
		}
		return out.Tokens[i].Session < out.Tokens[j].Session
	})
	return out, nil
}

func derivedPct(r record.Record, key string) *float64 {
	raw, ok := r.Derived[key]
	if !ok {
		return nil
	}
	var f *float64
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	return f
}

func derivedTime(r record.Record, key string) *time.Time {
	raw, ok := r.Derived[key]
	if !ok {
		return nil
	}
	var s *string
	if json.Unmarshal(raw, &s) != nil || s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return nil
	}
	return &t
}
