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

var (
	dayFileRe   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl$`)
	spillFileRe = regexp.MustCompile(`^spill-\d+-[0-9A-Za-z]+\.jsonl$`)
	limitKind   = []byte(`"kind":"` + record.KindLimitSample + `"`)
)

// ReadLocal reads the limit samples in the outbox (day files from the day before since
// on, and spill files) that are not older than since. The outbox keeps sent records for
// outbox.keep_days, so this works with or without a hub.
func ReadLocal(outboxDir string, since time.Time) ([]LocalSample, error) {
	ents, err := os.ReadDir(outboxDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
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
	var out []LocalSample
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
			if !bytes.Contains(head, limitKind) {
				continue
			}
			var r record.Record
			if json.Unmarshal(line, &r) != nil || r.Kind != record.KindLimitSample {
				continue
			}
			ts, err := r.Time()
			if err != nil || ts.Before(since) {
				continue
			}
			s := LocalSample{At: ts, Slot: r.Slot, Five: derivedPct(r, "five_hour_pct"), Seven: derivedPct(r, "seven_day_pct"),
				FiveReset: derivedTime(r, "five_hour_resets_at"), SevenReset: derivedTime(r, "seven_day_resets_at")}
			if r.Seat != nil {
				s.Account, s.Org = r.Seat.Account, r.Seat.Org
			}
			if s.Five != nil || s.Seven != nil {
				out = append(out, s)
			}
		}
		f.Close()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
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
