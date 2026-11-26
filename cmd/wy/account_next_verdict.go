package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/picker"
	"github.com/mraj602/watchyard/internal/record"
	"github.com/mraj602/watchyard/internal/version"
)

// verdictRepeat: a verdict that says the same as the last one this machine wrote less than
// this long ago is not written again (setup.md 8.5, Verdicts to the hub).
const verdictRepeat = 15 * time.Minute

// verdictMark is <state>/next/verdict.json: the last written verdict's digest and time.
type verdictMark struct {
	Digest string    `json:"digest"`
	At     time.Time `json:"at"`
}

// nextVerdicts is the account_verdicts payload of a run (record.md 4.11): every ranked
// account whose login is known, once per login, in rank order.
func nextVerdicts(res picker.Result) record.AccountVerdictsPayload {
	p := record.AccountVerdictsPayload{Strategy: res.Strategy}
	if res.NextFree != nil {
		s := res.NextFree.UTC().Format(time.RFC3339)
		p.NextFree = &s
	}
	seen := map[[2]string]bool{}
	rank := 0
	for _, r := range res.Ranked {
		login := [2]string{r.Account(), r.Org()}
		if login[0] == "" || login[1] == "" || seen[login] {
			continue
		}
		if len(p.Accounts) == record.VerdictMaxAccounts {
			break
		}
		seen[login] = true
		v := record.AccountVerdict{Account: login[0], Org: login[1], Kind: r.Kind, Eligible: r.Eligible, Stale: r.Stale}
		if r.Kind != record.LeaseAggressive && r.Kind != record.LeaseProtected {
			v.Kind = ""
		}
		switch r.ModeFrom {
		case record.ModeFromLocal, record.ModeFromHub, record.ModeFromProposed, record.ModeFromNone:
			v.ModeFrom = r.ModeFrom
		}
		v.Reason = cutRunes(r.Reason, record.VerdictMaxReason)
		if r.Eligible {
			rank++
			n := rank
			v.Rank = &n
		} else if r.FreeAt != nil {
			s := r.FreeAt.UTC().Format(time.RFC3339)
			v.FreeAt = &s
		}
		if l := r.Lanes; l != nil {
			w, f, b, c := l.WeeklyPct, l.FiveHourPct, l.Burst, l.BurstCap
			v.WeeklyPct, v.FiveHourPct, v.Burst, v.BurstCap = level(w), level(f), &b, &c
			v.WeeklyCap, v.FiveHourCap = level(l.WeeklyCap), level(l.FiveHourCap)
			v.Expiring, v.Stale = l.Expiring, v.Stale || l.Stale
			if l.Pins > 0 {
				n := l.Pins
				v.Pins = &n
				v.PinReserve5h, v.PinReserve7d = level(l.PinReserve5hPct), level(l.PinReserve7dPct)
				if l.PinFull5hPct > 0 {
					v.PinFull5h, v.PinFull7d = level(l.PinFull5hPct), level(l.PinFull7dPct)
				}
				if l.HoursToReset > 0 {
					s := res.Now.Add(time.Duration(l.HoursToReset * float64(time.Hour))).UTC().Truncate(time.Minute).Format(time.RFC3339)
					v.WeeklyResetsAt = &s
				}
			}
			if !r.Eligible {
				s := l.SkipLevel
				v.SkipLevel = &s
			}
		} else {
			for _, w := range r.Windows {
				switch {
				case w.Kind == picker.Kind5h && v.FiveHourPct == nil:
					v.FiveHourPct = level(w.Now)
				case w.Kind == picker.Kind7d && v.WeeklyPct == nil:
					v.WeeklyPct = level(w.Now)
				}
			}
		}
		p.Accounts = append(p.Accounts, v)
	}
	return p
}

func level(x float64) *float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return nil
	}
	x = math.Round(math.Min(math.Max(x, 0), 1000)*10) / 10
	return &x
}

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// verdictDigest is what makes two verdicts "the same": levels rounded to whole points, times
// to the minute.
func verdictDigest(p record.AccountVerdictsPayload) string {
	minute := func(s *string) *string {
		if s == nil {
			return nil
		}
		t, err := time.Parse(time.RFC3339, *s)
		if err != nil {
			return s
		}
		m := t.Truncate(time.Minute).Format(time.RFC3339)
		return &m
	}
	whole := func(x *float64) *float64 {
		if x == nil {
			return nil
		}
		r := math.Round(*x)
		return &r
	}
	q := p
	q.NextFree = minute(p.NextFree)
	q.Accounts = make([]record.AccountVerdict, len(p.Accounts))
	for i, a := range p.Accounts {
		a.FreeAt, a.WeeklyPct, a.FiveHourPct = minute(a.FreeAt), whole(a.WeeklyPct), whole(a.FiveHourPct)
		a.WeeklyCap, a.FiveHourCap = whole(a.WeeklyCap), whole(a.FiveHourCap)
		q.Accounts[i] = a
	}
	b, _ := json.Marshal(q)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// mirrorVerdicts appends the run's account_verdicts record to the outbox unless it repeats the
// last one within verdictRepeat or the machine has no hub; it reports whether it wrote one, and
// a warning or "". The caller flushes in the background.
func mirrorVerdicts(e nextEnv, cfg config.Config, machine string, res picker.Result, now time.Time) (bool, string) {
	if cfg.Hub.URL == "" {
		return false, ""
	}
	p := nextVerdicts(res)
	if len(p.Accounts) == 0 {
		return false, ""
	}
	markPath := filepath.Join(e.dirs.State.Path, "next", "verdict.json")
	digest := verdictDigest(p)
	var last verdictMark
	if b, err := os.ReadFile(markPath); err == nil && json.Unmarshal(b, &last) == nil &&
		last.Digest == digest && now.Sub(last.At) < verdictRepeat && !now.Before(last.At) {
		return false, ""
	}
	r, err := record.NewAccountVerdicts(record.Meta{Machine: machine, Client: "wy/" + version.Version, Now: now}, p)
	if err != nil {
		return false, "verdict not sent to the hub: " + err.Error()
	}
	ob := outbox.New(filepath.Join(e.dirs.State.Path, "outbox"))
	ob.NoFsync = !cfg.Outbox.Fsync
	if _, err := ob.Append(r); err != nil {
		return false, "verdict not sent to the hub: " + err.Error()
	}
	b, _ := json.Marshal(verdictMark{Digest: digest, At: now.UTC()})
	if err := os.MkdirAll(filepath.Dir(markPath), 0o700); err == nil {
		_ = atomicfile.Write(markPath, append(b, '\n'), 0o600)
	}
	return true, ""
}
