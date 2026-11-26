package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mraj602/watchyard/internal/mover"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/picker"
)

// moveBurnWindow is the window of a session's burn for the mover (setup.md 8.6, Inputs) and
// the default of wy burn's 5-hour rate column.
const moveBurnWindow = 15 * time.Minute

// burnStat is one session's token use in a window: weighted units (setup.md 8.5), API calls
// per model and the helpers (subagents) that made some of them.
type burnStat struct {
	Units   float64
	Window  time.Duration
	Calls   map[string]int
	Helpers map[string]bool
}

// rate is the burn in 5-hour points per hour, with f the account's points per unit.
func (b burnStat) rate(f float64) float64 {
	if b.Window <= 0 {
		return 0
	}
	return b.Units * f / b.Window.Hours()
}

// burnStats sums the token rows of the last window per session. The mover's forecast (with
// the 15-minute window) and wy burn read the same numbers.
func burnStats(ts []picker.TokenUse, now time.Time, window time.Duration) map[string]burnStat {
	out := map[string]burnStat{}
	for _, t := range ts {
		if now.Sub(t.At) > window || t.At.After(now.Add(time.Minute)) {
			continue
		}
		b, ok := out[t.Session]
		if !ok {
			b = burnStat{Window: window, Calls: map[string]int{}, Helpers: map[string]bool{}}
		}
		b.Units += t.Units
		b.Calls[shortModel(t.Model)]++
		if t.Agent != "" {
			b.Helpers[t.Agent] = true
		}
		out[t.Session] = b
	}
	return out
}

// shortModel is a model id without its "claude-" prefix and date ("opus-4-8").
func shortModel(m string) string {
	m = strings.TrimPrefix(m, "claude-")
	if i := strings.LastIndexByte(m, '-'); i > 0 && len(m)-i == 9 {
		m = m[:i]
	}
	if m == "" {
		return "?"
	}
	return m
}

type burnSession struct {
	Account   string         `json:"account"`
	Session   string         `json:"session"`
	Name      string         `json:"name,omitempty"`
	Kind      mover.Kind     `json:"kind"`
	State     string         `json:"state,omitempty"`
	Helpers   int            `json:"helpers"`
	Calls     map[string]int `json:"calls"`
	UnitsMin  float64        `json:"units_per_min"`
	FivePerH  float64        `json:"five_hour_points_per_hour"`
	Folder    string         `json:"folder"`
	MovedFrom string         `json:"moved_from,omitempty"`
}

type burnAccount struct {
	Account  string  `json:"account"`
	FiveHour float64 `json:"five_hour"`
	Weekly   float64 `json:"weekly"`
	FivePerH float64 `json:"five_hour_points_per_hour"`
	UnitsMin float64 `json:"units_per_min"`
	Sessions int     `json:"sessions"`
	HitIn    string  `json:"five_hour_hit_in,omitempty"`
}

func runBurn(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("burn", flag.ContinueOnError)
	fs.SetOutput(stderr)
	minutes := fs.Int("minutes", 30, "the window, in minutes")
	asJSON := fs.Bool("json", false, "print JSON")
	noHub := fs.Bool("no-hub", false, "use only this machine's data")
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: wy burn [--minutes N] [--json] [--no-hub]

Who is burning: per account its 5-hour and weekly levels and burn speed, then every running
session on this machine ranked by its token burn over the last N minutes (default 30), helper
agents included: account, name, kind, state, helpers, API calls per model, weighted token units
per minute, 5-hour points per hour and folder. The 5-hour rate is the one Smart Account Switch
forecasts with (setup.md 8.6).
`)
	}
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 || *minutes < 1 || *minutes > 24*60 {
		fs.Usage()
		return errUsage
	}
	pe := paths.FromOS()
	d, err := paths.Resolve(pe)
	if err != nil {
		return err
	}
	home := pe.Home
	if home == "" {
		return errors.New("cannot find the home folder")
	}
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	e := moveEnv{nextEnv: nextEnv{home: home, dirs: d, getenv: os.Getenv, now: time.Now, stdout: stdout, stderr: stderr}}
	return burnCmd(e, time.Duration(*minutes)*time.Minute, *asJSON, *noHub)
}

func burnCmd(e moveEnv, window time.Duration, asJSON, noHub bool) error {
	w, err := e.world(noHub)
	if err != nil {
		return err
	}
	now := e.now()
	stats := burnStats(w.tokens, now, window)
	var ss []burnSession
	per := map[string]*burnAccount{}
	for _, a := range w.input.Accounts {
		ba := &burnAccount{Account: a.Label, FiveHour: a.W5.Level, Weekly: a.W7.Level}
		if pr, ok := w.proj[a.Key]; ok {
			ba.FivePerH = pr.R5
			if pr.Hit > 0 {
				ba.HitIn = pr.Hit.Round(time.Minute).String()
			}
		}
		per[a.Key] = ba
	}
	for _, l := range w.launches {
		key := w.keyOfDir[l.Slot]
		b := stats[l.SessionID]
		s := burnSession{Account: w.labelOfDir[l.Slot], Session: l.SessionID, Name: l.Name, Kind: l.Kind, State: l.Status,
			Helpers: len(b.Helpers), Calls: b.Calls, UnitsMin: b.Units / window.Minutes(), FivePerH: w.byID[l.SessionID].Rate,
			Folder: tilde(e.home, l.CWD)}
		if s.Calls == nil {
			s.Calls = map[string]int{}
		}
		if m := w.byID[l.SessionID]; !m.MovedAt.IsZero() {
			s.MovedFrom = w.targets[m.MovedFrom].Label
		}
		ss = append(ss, s)
		if ba := per[key]; ba != nil {
			ba.Sessions++
			ba.UnitsMin += s.UnitsMin
		}
	}
	sort.SliceStable(ss, func(i, j int) bool {
		if ss[i].UnitsMin != ss[j].UnitsMin {
			return ss[i].UnitsMin > ss[j].UnitsMin
		}
		return ss[i].Session < ss[j].Session
	})
	var accs []burnAccount
	for _, a := range w.input.Accounts {
		accs = append(accs, *per[a.Key])
	}
	sort.SliceStable(accs, func(i, j int) bool { return accs[i].FivePerH > accs[j].FivePerH })
	if asJSON {
		return writeJSONOut(e.stdout, map[string]any{"minutes": int(window.Minutes()), "accounts": accs, "sessions": ss, "warnings": w.warnings})
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ACCOUNT\t5H\t7D\t5H PTS/H\tUNITS/MIN\tSESSIONS\t5H HIT IN")
	for _, a := range accs {
		fmt.Fprintf(tw, "%s\t%.0f%%\t%.0f%%\t%.1f\t%s\t%d\t%s\n", a.Account, a.FiveHour, a.Weekly, a.FivePerH, units(a.UnitsMin), a.Sessions, dash(a.HitIn))
	}
	_ = tw.Flush()
	fmt.Fprintf(e.stdout, "\nSessions by burn over the last %d min (helpers included):\n", int(window.Minutes()))
	tw = tabwriter.NewWriter(e.stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "UNITS/MIN\t5H PTS/H\tACCOUNT\tSESSION\tNAME\tKIND\tSTATE\tHELPERS\tCALLS\tFOLDER")
	for _, s := range ss {
		acct := s.Account
		if s.MovedFrom != "" {
			acct += " (from " + s.MovedFrom + ")"
		}
		fmt.Fprintf(tw, "%s\t%.1f\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", units(s.UnitsMin), s.FivePerH, acct, short(s.Session), dash(s.Name),
			s.Kind, dash(s.State), s.Helpers, callsLine(s.Calls), s.Folder)
	}
	_ = tw.Flush()
	for _, x := range w.warnings {
		fmt.Fprintln(e.stdout, "warning: "+x)
	}
	return nil
}

func units(v float64) string {
	switch {
	case v >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.0fk", v/1e3)
	}
	return fmt.Sprintf("%.0f", v)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func callsLine(c map[string]int) string {
	if len(c) == 0 {
		return "-"
	}
	ks := make([]string, 0, len(c))
	for k := range c {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var b []string
	for _, k := range ks {
		b = append(b, fmt.Sprintf("%s:%d", k, c[k]))
	}
	return strings.Join(b, ",")
}

func tilde(home, p string) string {
	if home != "" && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + p[len(home):]
	}
	return p
}
