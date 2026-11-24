package mover

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// PassAccount is one account's projection in one auto pass (the plan's, before its moves).
type PassAccount struct {
	Key       string  `json:"key"`
	Label     string  `json:"label"`
	Protected bool    `json:"protected,omitempty"`
	Level5    float64 `json:"l5"`
	Level7    float64 `json:"l7"`
	P5        float64 `json:"p5"`
	P7        float64 `json:"p7"`
	Cap5      float64 `json:"cap5"`
	Cap7      float64 `json:"cap7"`
	R5        float64 `json:"r5"`
	Reset7S   int64   `json:"reset7_s"`         // seconds to the weekly reset
	Window    string  `json:"window,omitempty"` // "5h" or "7d": over its cap
	Note      string  `json:"note,omitempty"`   // the rate note ("... used the readings")
}

// PassRecord is one line of <state>/move/passes.jsonl.
type PassRecord struct {
	At       time.Time     `json:"at"`
	Accounts []PassAccount `json:"accounts"`
}

const (
	passesFile    = "passes.jsonl"
	passesMaxSize = 1 << 20 // trimmed to the last 3 days above this
	passesKeep    = 3 * 24 * time.Hour
)

// AppendPass adds a pass to passes.jsonl and trims the file when it grows large.
func (s State) AppendPass(r PassRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	p := s.path(passesFile)
	if fi, err := os.Stat(p); err == nil && fi.Size() > passesMaxSize {
		var keep []byte
		for _, x := range s.Passes(r.At.Add(-passesKeep)) {
			if lb, err := json.Marshal(x); err == nil {
				keep = append(keep, append(lb, '\n')...)
			}
		}
		_ = os.WriteFile(p, keep, 0o600)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Passes are the recorded passes since since, oldest first.
func (s State) Passes(since time.Time) []PassRecord {
	f, err := os.Open(s.path(passesFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []PassRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r PassRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil && !r.At.Before(since) {
			out = append(out, r)
		}
	}
	return out
}

// Digest tuning.
const (
	// SuspectWithin: a move is a suspect when the source's projection fell back under its cap
	// in a pass this soon after it, while its measured burn had not dropped.
	SuspectWithin = 15 * time.Minute
	// maxGap caps the time one pass stands for (a pass is about a minute apart).
	maxGap = 5 * time.Minute
)

// DigestMove is one move of the period.
type DigestMove struct {
	At      time.Time `json:"at"`
	Session string    `json:"session"`
	Name    string    `json:"name,omitempty"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Auto    bool      `json:"auto"`
	Reason  string    `json:"reason,omitempty"`
}

// LimitHit is a session a limit stopped (a StopFailure rate_limit hook event).
type LimitHit struct {
	At      time.Time `json:"at"`
	Session string    `json:"session"`
}

// WeeklyRoom is an account's weekly window as of its last pass of the period: Room is the
// points projected to be left unused at the reset.
type WeeklyRoom struct {
	Account string    `json:"account"`
	Level7  float64   `json:"level7"`
	P7      float64   `json:"p7"`
	Room    float64   `json:"room"`
	ResetAt time.Time `json:"reset_at"`
}

// ProtectedOver is the time a protected account's projection was over its cap.
type ProtectedOver struct {
	Account string `json:"account"`
	Minutes int    `json:"minutes"`
}

// Suspect is a move that may not have been needed.
type Suspect struct {
	DigestMove
	Why string `json:"why"`
}

// Digest is the period's report (`wy move --digest`).
type Digest struct {
	Since         time.Time       `json:"since"`
	Until         time.Time       `json:"until"`
	Moves         []DigestMove    `json:"moves"`
	LimitHits     []LimitHit      `json:"limit_hits"`
	Weekly        []WeeklyRoom    `json:"weekly"`
	ProtectedOver []ProtectedOver `json:"protected_over"`
	Suspects      []Suspect       `json:"suspects"`
	Notes         []string        `json:"notes,omitempty"`
}

// BuildDigest reports the period [since, now]. name gives the label of a slot folder or key;
// hits are the limit stops found in the outbox by the caller.
func (s State) BuildDigest(since, now time.Time, name func(dir, key string) string, hits []LimitHit) Digest {
	d := Digest{Since: since, Until: now, Moves: []DigestMove{}, LimitHits: []LimitHit{}, Weekly: []WeeklyRoom{},
		ProtectedOver: []ProtectedOver{}, Suspects: []Suspect{}}
	for _, h := range hits {
		if !h.At.Before(since) && !h.At.After(now) {
			d.LimitHits = append(d.LimitHits, h)
		}
	}
	sort.Slice(d.LimitHits, func(i, j int) bool { return d.LimitHits[i].At.Before(d.LimitHits[j].At) })
	passes := s.Passes(since)
	entries := s.Log(since)
	for _, e := range entries {
		d.Moves = append(d.Moves, DigestMove{At: e.At, Session: e.SessionID, Name: e.Name, From: name(e.FromDir, e.From),
			To: name(e.ToDir, e.To), Auto: e.Auto, Reason: e.Reason})
	}
	if len(passes) == 0 {
		d.Notes = append(d.Notes, "no pass records in this period (the switch was off, or it ran before passes were recorded): weekly room, protected minutes and suspects are left out")
	} else {
		d.Weekly = weeklyRoom(passes)
		d.ProtectedOver = protectedOver(passes)
	}
	for i, e := range entries {
		if why := suspectWhy(e, passes); why != "" {
			d.Suspects = append(d.Suspects, Suspect{DigestMove: d.Moves[i], Why: why})
		}
	}
	return d
}

func weeklyRoom(passes []PassRecord) []WeeklyRoom {
	last := map[string]WeeklyRoom{}
	var order []string
	for _, p := range passes {
		for _, a := range p.Accounts {
			if _, ok := last[a.Key]; !ok {
				order = append(order, a.Key)
			}
			label := a.Label
			if label == "" {
				label = a.Key
			}
			last[a.Key] = WeeklyRoom{Account: label, Level7: a.Level7, P7: a.P7, Room: max(0, 100-max(a.Level7, a.P7)),
				ResetAt: p.At.Add(time.Duration(a.Reset7S) * time.Second)}
		}
	}
	out := make([]WeeklyRoom, 0, len(order))
	for _, k := range order {
		out = append(out, last[k])
	}
	return out
}

// protectedOver sums, per protected account, the time its projection was over its cap: each
// pass stands for the time to the next one (at most maxGap; the last for one minute).
func protectedOver(passes []PassRecord) []ProtectedOver {
	secs := map[string]time.Duration{}
	label := map[string]string{}
	var order []string
	for i, p := range passes {
		gap := time.Minute
		if i+1 < len(passes) {
			gap = min(passes[i+1].At.Sub(p.At), maxGap)
		}
		for _, a := range p.Accounts {
			if !a.Protected {
				continue
			}
			if _, ok := label[a.Key]; !ok {
				label[a.Key] = a.Label
				order = append(order, a.Key)
			}
			if a.Window != "" {
				secs[a.Key] += gap
			}
		}
	}
	out := []ProtectedOver{}
	for _, k := range order {
		if secs[k] > 0 {
			out = append(out, ProtectedOver{Account: label[k], Minutes: int((secs[k] + 30*time.Second) / time.Minute)})
		}
	}
	return out
}

// suspectWhy says why move e may have been needless ("" when it looks fine): the plan used
// the readings over the meter for its source, or the source's projection fell back under its
// cap within SuspectWithin while its measured burn had not dropped (the burn did not follow
// the session, so the projection was noise).
func suspectWhy(e LogEntry, passes []PassRecord) string {
	if strings.Contains(e.Reason, "used the readings") {
		return "the plan used the readings (they rose over twice the meter)"
	}
	trig := -1
	for i, p := range passes {
		if !p.At.Before(e.At) {
			trig = i
			break
		}
	}
	if trig < 0 {
		return ""
	}
	src, ok := passAccount(passes[trig], e.From)
	if !ok || src.Window == "" {
		return ""
	}
	if strings.Contains(src.Note, "used the readings") {
		return "the plan used the readings (they rose over twice the meter)"
	}
	for _, p := range passes[trig+1:] {
		if p.At.Sub(e.At) > SuspectWithin {
			break
		}
		if a, ok := passAccount(p, e.From); ok && a.Window == "" && a.R5 >= 0.8*src.R5 {
			return fmt.Sprintf("%s was back under its cap %d min after the move with its burn still %.1f points/h",
				a.Label, int(p.At.Sub(e.At).Round(time.Minute)/time.Minute), a.R5)
		}
	}
	return ""
}

func passAccount(p PassRecord, key string) (PassAccount, bool) {
	for _, a := range p.Accounts {
		if a.Key == key {
			return a, true
		}
	}
	return PassAccount{}, false
}

func (m DigestMove) text() string {
	n := m.Name
	if n == "" {
		n = m.Session
		if len(n) > 8 {
			n = n[:8]
		}
	}
	by := ""
	if !m.Auto {
		by = " (by hand)"
	}
	t := fmt.Sprintf("%s  %s  %s -> %s%s", m.At.Local().Format("01-02 15:04"), n, m.From, m.To, by)
	if m.Reason != "" {
		t += ": " + cutRunes(m.Reason, maxReason)
	}
	return t
}

// Text is the digest for a terminal.
func (d Digest) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Smart Account Switch digest, %s to %s\n", d.Since.Local().Format("01-02 15:04"), d.Until.Local().Format("01-02 15:04"))
	fmt.Fprintf(&b, "\nMoves: %d\n", len(d.Moves))
	for _, m := range d.Moves {
		b.WriteString("  " + m.text() + "\n")
	}
	fmt.Fprintf(&b, "\nLimit hits (sessions a limit stopped): %d\n", len(d.LimitHits))
	for _, h := range d.LimitHits {
		id := h.Session
		if len(id) > 8 {
			id = id[:8]
		}
		fmt.Fprintf(&b, "  %s  %s\n", h.At.Local().Format("01-02 15:04"), id)
	}
	if len(d.Weekly) > 0 {
		b.WriteString("\nWeekly room at the reset (projected, as of the last pass):\n")
		for _, w := range d.Weekly {
			fmt.Fprintf(&b, "  %s: %.0f points left (level %.0f%%, resets %s)\n", w.Account, w.Room, w.Level7, w.ResetAt.Local().Format("Mon 15:04"))
		}
	}
	if len(d.ProtectedOver) > 0 {
		b.WriteString("\nProtected accounts over their cap (projected):\n")
		for _, p := range d.ProtectedOver {
			fmt.Fprintf(&b, "  %s: %d min\n", p.Account, p.Minutes)
		}
	} else if len(d.Weekly) > 0 {
		b.WriteString("\nProtected accounts over their cap: none\n")
	}
	if len(d.Suspects) > 0 {
		b.WriteString("\nMoves that may not have been needed:\n")
		for _, s := range d.Suspects {
			b.WriteString("  " + s.text() + "\n    " + s.Why + "\n")
		}
	}
	for _, n := range d.Notes {
		b.WriteString("\nNote: " + n + "\n")
	}
	return b.String()
}

// Summary is the digest in a few lines for a push.
func (d Digest) Summary() string {
	hours := int(d.Until.Sub(d.Since).Round(time.Hour) / time.Hour)
	t := fmt.Sprintf("Last %dh: %d moves, %d limit hits", hours, len(d.Moves), len(d.LimitHits))
	if len(d.ProtectedOver) > 0 {
		var ps []string
		for _, p := range d.ProtectedOver {
			ps = append(ps, fmt.Sprintf("%s %d min", p.Account, p.Minutes))
		}
		t += "; protected over cap: " + strings.Join(ps, ", ")
	}
	if len(d.Suspects) > 0 {
		t += fmt.Sprintf("; %d suspect", len(d.Suspects))
	}
	if len(d.Weekly) > 0 {
		var ws []string
		for _, w := range d.Weekly {
			ws = append(ws, fmt.Sprintf("%s %.0f", w.Account, w.Room))
		}
		t += ". Weekly room at reset: " + strings.Join(ws, ", ")
	}
	return t
}

// Digest stamp: the local day the daily digest was last sent.
const (
	// DigestHour is when the daily digest goes out; DigestUntil the hour after which a missed
	// digest (the machine was off) is skipped until tomorrow.
	DigestHour  = 9
	DigestUntil = 12
)

// DigestDue reports whether the daily digest is due at now (local time): from 09:00 to 12:00
// and not yet sent today.
func (s State) DigestDue(now time.Time) bool {
	l := now.Local()
	if l.Hour() < DigestHour || l.Hour() >= DigestUntil {
		return false
	}
	b, _ := os.ReadFile(s.path("digest.stamp"))
	return strings.TrimSpace(string(b)) != l.Format("2006-01-02")
}

// MarkDigest records that today's digest was sent.
func (s State) MarkDigest(now time.Time) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.path("digest.stamp"), []byte(now.Local().Format("2006-01-02")+"\n"), 0o600)
}
