package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/leases"
	"github.com/mraj602/watchyard/internal/mover"
	"github.com/mraj602/watchyard/internal/paths"
	peerspkg "github.com/mraj602/watchyard/internal/peers"
	"github.com/mraj602/watchyard/internal/picker"
	"github.com/mraj602/watchyard/internal/record"
	"github.com/mraj602/watchyard/internal/status"
)

// moveEnv is `wy move`'s world: the picker's (nextEnv) plus the machine's sessions and the
// host that stops and starts them. Tests replace sys, host and tags.
type moveEnv struct {
	nextEnv
	sys  *mover.Sys
	host mover.Host
	// tags returns each session's peers tags (nil: none known).
	tags func() map[string][]string
}

func moveUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  wy move --plan [--json]                what Smart Account Switch would do now (changes nothing)
  wy move <session> [<account>] [--dry-run] [--bg] [--now]
                                         move one session (its id, id prefix or name) to an account
                                         (its label, folder or number), else to the best one
  wy move --auto                         one pass of Smart Account Switch (the timer runs it; does
                                         nothing unless account.smart_switch is on)
  wy move --pin-account <session>        never move this session (--unpin to undo; the peers tag
                                         no-move does the same)
  wy move --list [--json]                the moves of the last day

Smart Account Switch moves a session only when its account would reach its 5-hour or weekly
limit before that window resets, at its current burn (setup.md 8.6). wy config set
account.smart_switch on|off turns it on or off (default off).
`)
}

func runMove(args []string, stdout, stderr io.Writer) error {
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
	return moveCmd(e, args)
}

func moveCmd(e moveEnv, args []string) error {
	fs := flag.NewFlagSet("move", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() { moveUsage(e.stderr) }
	plan := fs.Bool("plan", false, "show what Smart Account Switch would do now")
	auto := fs.Bool("auto", false, "one pass of Smart Account Switch (only when account.smart_switch is on)")
	dry := fs.Bool("dry-run", false, "print the steps of the move, change nothing")
	bg := fs.Bool("bg", false, "start the moved session as a background session (claude --resume <id> --bg)")
	now := fs.Bool("now", false, "move a session that is in the middle of a turn (its turn is lost)")
	asJSON := fs.Bool("json", false, "print JSON")
	list := fs.Bool("list", false, "the moves of the last day")
	noHub := fs.Bool("no-hub", false, "use only this machine's data")
	pin := fs.String("pin-account", "", "never move this session")
	unpin := fs.String("unpin", "", "let Smart Account Switch move this session again")
	var pos []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return usageErr(err)
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	st := mover.NewState(e.dirs.State.Path)
	modes := 0
	for _, b := range []bool{*plan, *auto, *list, *pin != "", *unpin != "", len(pos) > 0} {
		if b {
			modes++
		}
	}
	if modes != 1 || len(pos) > 2 {
		moveUsage(e.stderr)
		return errUsage
	}
	switch {
	case *pin != "" || *unpin != "":
		id, on := *pin, true
		if id == "" {
			id, on = *unpin, false
		}
		w, err := e.world(*noHub)
		if err != nil {
			return err
		}
		l, err := w.find(id)
		if err != nil {
			return err
		}
		if err := st.SetPin(l.SessionID, on); err != nil {
			return err
		}
		verb := "pinned to its account: Smart Account Switch will not move it"
		if !on {
			verb = "unpinned: Smart Account Switch may move it"
		}
		fmt.Fprintf(e.stdout, "%s %s\n", short(l.SessionID), verb)
		return nil
	case *list:
		es := st.Log(e.now().Add(-24 * time.Hour))
		if *asJSON {
			return writeJSONOut(e.stdout, es)
		}
		for _, x := range es {
			fmt.Fprintf(e.stdout, "%s  %s  %s -> %s  %s  %s\n", x.At.Local().Format("01-02 15:04"), short(x.SessionID), x.From, x.To, x.Kind, x.Reason)
		}
		return nil
	}
	cfg := e.config()
	if *auto && !cfg.Account.SmartSwitch {
		fmt.Fprintln(e.stdout, "Smart Account Switch is off (wy config set account.smart_switch on)")
		return nil
	}
	w, err := e.world(*noHub)
	if err != nil {
		return err
	}
	switch {
	case *plan:
		moves := mover.Plan(w.input)
		if *asJSON {
			return writeJSONOut(e.stdout, w.planDoc(moves))
		}
		w.printPlan(e.stdout, moves, cfg)
		return nil
	case *auto:
		return w.autoPass(e, cfg, st)
	}
	l, err := w.find(pos[0])
	if err != nil {
		return err
	}
	var t mover.Target
	var mv mover.Move
	if len(pos) == 2 {
		t, err = w.account(pos[1])
		if err != nil {
			return err
		}
	} else {
		key, why := mover.Destination(w.input, l.SessionID)
		if key == "" {
			return exitWith(exitNoAccount, fmt.Errorf("no account can take %s: %s", short(l.SessionID), why))
		}
		t = w.targets[key]
	}
	if t.Dir == l.Slot {
		return fmt.Errorf("%s already runs on %s", short(l.SessionID), t.Label)
	}
	if f, ok := w.forecasts[w.keyOfDir[l.Slot]]; ok {
		if wf, hits := f.First(); hits {
			mv = mover.Move{Window: wf.Window, HitAt: e.now().Add(wf.HitIn), ResetAt: e.now().Add(wf.ResetIn)}
		}
	}
	if *bg && (l.Kind == mover.KindTmux || l.Kind == mover.KindWrapped || l.Kind == mover.KindTerminal) {
		l.Kind = mover.KindBackground
	}
	from := w.labelOfDir[l.Slot]
	note := mover.Note(from, t.Label, e.now(), mv)
	if *dry || !l.Kind.Movable() {
		if !l.Kind.Movable() && !*dry {
			fmt.Fprintf(e.stderr, "wy move: %s is a %s session: Watchyard cannot restart it; the steps:\n", short(l.SessionID), l.Kind)
		}
		for _, s := range mover.Manual(l, t, cfg.Account.MoveLaunch, note) {
			fmt.Fprintln(e.stdout, s)
		}
		if !l.Kind.Movable() && !*dry {
			return exitWith(1, nil)
		}
		return nil
	}
	if l.Status == "busy" && !*now {
		return fmt.Errorf("%s is in the middle of a turn: wait for it to finish, or pass --now (the turn and its running subagents are lost)", short(l.SessionID))
	}
	return w.carryOut(e, cfg, st, l, t, note, "by hand", false)
}

func (e moveEnv) config() config.Config {
	cfg := config.Defaults()
	if f, err := config.Load(e.dirs.Config.Path); err == nil {
		if c, err := f.Typed(); err == nil {
			cfg = c
		}
	}
	return cfg
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func writeJSONOut(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// moveWorld is one read of the machine for the mover.
type moveWorld struct {
	launches   []mover.Launch
	input      mover.Input
	targets    map[string]mover.Target // by account key
	keyOfDir   map[string]string       // slot dir -> account key
	labelOfDir map[string]string
	forecasts  map[string]mover.Forecast
	byID       map[string]mover.Session
	warnings   []string
}

func (e moveEnv) world(noHub bool) (*moveWorld, error) {
	cfg := e.config()
	now := e.now()
	w := &moveWorld{targets: map[string]mover.Target{}, keyOfDir: map[string]string{}, labelOfDir: map[string]string{},
		forecasts: map[string]mover.Forecast{}, byID: map[string]mover.Session{}}
	slots, err := nextSlots(e.nextEnv, cfg)
	if err != nil {
		return nil, err
	}
	var islots []identity.Slot
	for _, s := range slots {
		islots = append(islots, identity.Slot{Dir: s.Dir, Name: s.Folder, Default: s.Default})
		w.labelOfDir[s.Dir] = s.Label
		if s.LoggedIn && s.Account != "" {
			k := "acct:" + s.Account + "/" + s.Org
			w.keyOfDir[s.Dir] = k
			if _, ok := w.targets[k]; !ok {
				w.targets[k] = mover.Target{Key: k, Label: s.Label, Dir: s.Dir}
			}
		}
	}
	// The picker's view, read-only (as wy account next --no-reserve).
	m, err := identity.LoadOrCreateMachine(e.dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return nil, err
	}
	src := picker.Sources{Machine: m.ID}
	if !noHub {
		if hd, herr := nextHub(e.nextEnv, cfg, slots, m.ID, now); herr != nil {
			w.warnings = append(w.warnings, "hub not read: "+herr.Error())
		} else if hd != nil {
			src.Hub = hd
		}
	}
	ob, lerr := picker.ReadOutbox(filepath.Join(e.dirs.State.Path, "outbox"), now.Add(-7*24*time.Hour), true)
	if lerr != nil {
		w.warnings = append(w.warnings, "outbox: "+lerr.Error())
	}
	src.Local = ob.Samples
	settings := picker.SettingsFrom(cfg.Next)
	settings.Proposed = cfg.Slots.Modes
	in := picker.Input{Now: now, Strategy: config.NextStrategyLanes, Settings: settings, Candidates: picker.Candidates(slots, src),
		Outbox: ob, N: 1, HubDown: src.Hub == nil && cfg.Hub.URL != ""}
	if src.Hub == nil && cfg.Hub.URL != "" {
		picker.UseHubModes(in.Candidates, picker.LoadHubModes(filepath.Join(e.dirs.State.Path, "next", "modes.json")))
	}
	in.Calibration = picker.LoadCalibration(filepath.Join(e.dirs.State.Path, "next", "calibration.json"))
	ledger := leases.Ledger{Dir: filepath.Join(e.dirs.State.Path, "next")}
	if ls, _, err := ledger.Read(now); err == nil {
		in.Leases = ls
	}
	var lin *picker.LaneInput
	in.OnLanes = func(l picker.LaneInput) { lin = &l }
	res, err := picker.Decide(in)
	if err != nil {
		return nil, err
	}
	if lin == nil {
		return nil, errors.New("the lanes picker gave no reading")
	}
	accs, lanes := lin.Live()

	// Accounts in the picker's order (its first pick first; protected ones last by Plan).
	order := map[string]int{}
	for i, r := range res.Ranked {
		if k := w.keyOfDir[r.Dir]; k != "" {
			if _, ok := order[k]; !ok {
				order[k] = i
			}
		}
	}
	sort.SliceStable(accs, func(i, j int) bool { return rankOf(order, accs[i].Key) < rankOf(order, accs[j].Key) })
	f5 := map[string]float64{}
	for _, a := range accs {
		t, ok := w.targets[a.Key]
		ma := mover.Account{Key: a.Key, Label: t.Label, Protected: a.Protected, Ratio: a.Ratio, Stale: a.Stale, Excluded: !ok,
			W5: mover.Window{Level: a.FiveHour, Reset: a.FiveReset}, W7: mover.Window{Level: a.Weekly, Reset: a.WeeklyReset}}
		w.input.Accounts = append(w.input.Accounts, ma)
		f5[a.Key] = a.F5
	}

	// Sessions: the registry of every folder, with their burn (the lease's, else their token
	// rows of the last 15 minutes) and their context.
	sys := e.sys
	if sys == nil {
		sys = &mover.Sys{FS: identity.OSFS{}, Proc: peerspkg.NewProc(), Cmdline: procCmdline, Panes: tmuxPanes,
			State: mover.NewState(e.dirs.State.Path)}
	}
	w.launches = sys.Sessions(islots)
	rate := map[string]mover.Session{}
	for _, l := range lanes {
		rate[l.SID] = mover.Session{Rate: l.Rate, Expected: max(0, l.Expect-now.Sub(l.T0)), Age: now.Sub(l.T0)}
	}
	const bw = 15 * time.Minute
	units := map[string]float64{}
	first := map[string]time.Time{}
	for _, t := range ob.Tokens {
		if _, ok := first[t.Session]; !ok {
			first[t.Session] = t.At
		}
		if now.Sub(t.At) <= bw {
			units[t.Session] += t.Units
		}
	}
	ctx := map[string]float64{}
	if recs, err := status.Load(filepath.Join(e.dirs.State.Path, "outbox"), now.Add(-status.Window)); err == nil {
		for id, s := range status.Adapt(recs) {
			if s.ContextPct != nil {
				ctx[id] = *s.ContextPct
			}
		}
	}
	st := mover.NewState(e.dirs.State.Path)
	pins := st.Pins()
	var tags map[string][]string
	if e.tags != nil {
		tags = e.tags()
	} else {
		tags = peersTags()
	}
	log := st.Log(now.Add(-6 * time.Hour))
	last := map[string]mover.LogEntry{}
	for _, x := range log {
		last[x.SessionID] = x
	}
	w.input.Now, w.input.Params, w.input.Recent = now, mover.DefaultParams, mover.Records(log)
	for _, l := range w.launches {
		key := w.keyOfDir[l.Slot]
		if key == "" {
			continue
		}
		s := rate[l.SessionID]
		s.ID, s.Account, s.Name = l.SessionID, key, l.Name
		if s.Rate == 0 {
			s.Rate = units[l.SessionID] * f5[key] / bw.Hours()
		}
		if s.Age == 0 {
			if t, ok := first[l.SessionID]; ok {
				s.Age = now.Sub(t)
			}
		}
		interactive := l.Kind == mover.KindTmux || l.Kind == mover.KindWrapped || l.Kind == mover.KindTerminal
		s.Idle = l.Status == "idle"
		if s.Idle && !l.StatusSince.IsZero() {
			s.IdleFor = now.Sub(l.StatusSince)
		}
		// The owner at it: an interactive session whose status changed in the last 3 minutes.
		s.Attended = interactive && !l.StatusSince.IsZero() && now.Sub(l.StatusSince) < 3*time.Minute
		if p, ok := ctx[l.SessionID]; ok {
			s.Context = p / 100 * 200_000
		}
		s.Pinned = pins[l.SessionID] || peerspkg.HasTag(tags[l.SessionID], "no-move")
		s.Movable = l.Kind.Movable()
		if x, ok := last[l.SessionID]; ok {
			s.MovedAt, s.MovedFrom = x.At, x.From
		}
		for _, a := range w.input.Accounts {
			if a.Key == key && s.Idle && (a.W5.Level >= 100 || a.W7.Level >= 100) {
				s.Stopped = true
			}
		}
		w.input.Sessions = append(w.input.Sessions, s)
		w.byID[s.ID] = s
	}
	byAcc := map[string][]mover.Session{}
	for _, s := range w.input.Sessions {
		byAcc[s.Account] = append(byAcc[s.Account], s)
	}
	for _, a := range w.input.Accounts {
		w.forecasts[a.Key] = w.input.Params.Forecast(a, byAcc[a.Key], now, 0)
	}
	return w, nil
}

func rankOf(order map[string]int, k string) int {
	if i, ok := order[k]; ok {
		return i
	}
	return 1 << 20
}

// find resolves a session by id, id prefix or name.
func (w *moveWorld) find(q string) (mover.Launch, error) {
	var hits []mover.Launch
	for _, l := range w.launches {
		if l.SessionID == q || (len(q) >= 4 && strings.HasPrefix(l.SessionID, q)) || (l.Name != "" && strings.EqualFold(l.Name, q)) {
			hits = append(hits, l)
		}
	}
	switch len(hits) {
	case 0:
		return mover.Launch{}, fmt.Errorf("no running session %q on this machine", q)
	case 1:
		return hits[0], nil
	}
	return mover.Launch{}, fmt.Errorf("%q matches %d sessions: give more of the id", q, len(hits))
}

// account resolves an account by label, folder (~/.claude-3) or number.
func (w *moveWorld) account(q string) (mover.Target, error) {
	for dir, k := range w.keyOfDir {
		t := w.targets[k]
		folder := "~/" + filepath.Base(dir)
		if strings.EqualFold(w.labelOfDir[dir], q) || folder == q || dir == q || mover.SlotNumber(dir) == q {
			return mover.Target{Key: k, Label: w.labelOfDir[dir], Dir: t.Dir}, nil
		}
	}
	return mover.Target{}, fmt.Errorf("no logged-in account %q", q)
}

func (w *moveWorld) launch(id string) (mover.Launch, bool) {
	for _, l := range w.launches {
		if l.SessionID == id {
			return l, true
		}
	}
	return mover.Launch{}, false
}

type planMove struct {
	mover.Move
	Kind   mover.Kind `json:"kind"`
	Steps  []string   `json:"steps,omitempty"`
	ToName string     `json:"to_label,omitempty"`
}

type planAccount struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Window   string  `json:"window,omitempty"`
	Level    float64 `json:"level"`
	BurnRate float64 `json:"burn_per_hour"`
	HitIn    string  `json:"hit_in,omitempty"`
	ResetIn  string  `json:"reset_in"`
}

func (w *moveWorld) planDoc(moves []mover.Move) map[string]any {
	var accs []planAccount
	for _, a := range w.input.Accounts {
		f := w.forecasts[a.Key]
		wf, hits := f.First()
		if !hits {
			wf = f.W5
		}
		pa := planAccount{Key: a.Key, Label: a.Label, Level: wf.Level, BurnRate: wf.BurnRate, ResetIn: wf.ResetIn.Round(time.Minute).String()}
		if hits {
			pa.Window, pa.HitIn = wf.Window, wf.HitIn.Round(time.Minute).String()
		}
		accs = append(accs, pa)
	}
	var ms []planMove
	for _, m := range moves {
		pm := planMove{Move: m, ToName: w.targets[m.To].Label}
		if l, ok := w.launch(m.Session); ok {
			pm.Kind = l.Kind
		}
		ms = append(ms, pm)
	}
	return map[string]any{"accounts": accs, "moves": ms, "warnings": w.warnings}
}

func (w *moveWorld) printPlan(out io.Writer, moves []mover.Move, cfg config.Config) {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ACCOUNT\t5H\t7D\tBURN/H\tFORECAST")
	for _, a := range w.input.Accounts {
		f := w.forecasts[a.Key]
		fc := "no limit before its resets"
		if wf, hits := f.First(); hits {
			fc = fmt.Sprintf("%s hits in %s, before its reset in %s", wf.Window, wf.HitIn.Round(time.Minute), wf.ResetIn.Round(time.Minute))
		}
		if a.Stale {
			fc = "stale reading"
		}
		fmt.Fprintf(tw, "%s\t%.0f%%\t%.0f%%\t%.1f\t%s\n", a.Label, a.W5.Level, a.W7.Level, f.W5.BurnRate, fc)
	}
	_ = tw.Flush()
	state := "off: nothing moves by itself"
	if cfg.Account.SmartSwitch {
		state = "on"
	}
	fmt.Fprintf(out, "\nSmart Account Switch is %s.\n", state)
	if len(moves) == 0 {
		fmt.Fprintln(out, "No session needs a move.")
	}
	for _, m := range moves {
		l, _ := w.launch(m.Session)
		to := w.targets[m.To].Label
		if to == "" {
			to = "(none)"
		}
		fmt.Fprintf(out, "%s %s (%s): %s -> %s: %s\n", short(m.Session), l.Name, l.Kind, w.targets[m.From].Label, to, m.Reason)
		if m.To != "" && !l.Kind.Movable() {
			for _, s := range mover.Manual(l, w.targets[m.To], cfg.Account.MoveLaunch, "") {
				fmt.Fprintln(out, "    "+s)
			}
		}
	}
	for _, x := range w.warnings {
		fmt.Fprintln(out, "warning: "+x)
	}
}

// autoPass carries out the plan's moves that are due now (not waits, a destination found,
// a kind the mover can restart).
func (w *moveWorld) autoPass(e moveEnv, cfg config.Config, st mover.State) error {
	moves := mover.Plan(w.input)
	var first error
	for _, m := range moves {
		l, ok := w.launch(m.Session)
		if !ok || m.To == "" || m.Wait || !l.Kind.Movable() {
			continue
		}
		note := mover.Note(w.targets[m.From].Label, w.targets[m.To].Label, e.now(), m)
		if err := w.carryOut(e, cfg, st, l, w.targets[m.To], note, m.Reason, true); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// carryOut moves l to t and records it: the move log, the lease ledger (the old lease ends,
// a new one on t carries the session) and the note.
func (w *moveWorld) carryOut(e moveEnv, cfg config.Config, st mover.State, l mover.Launch, t mover.Target, note, reason string, auto bool) error {
	host := e.host
	if host == nil {
		host = mover.RealHost{Proc: peerspkg.NewProc(), State: st}
	}
	log := filepath.Join(st.Dir, "logs", l.SessionID+".log")
	argv, err := mover.Execute(host, l, t, cfg.Account.MoveLaunch, note, log)
	from := w.labelOfDir[l.Slot]
	entry := mover.LogEntry{At: e.now(), SessionID: l.SessionID, From: w.keyOfDir[l.Slot], To: t.Key, FromDir: l.Slot, ToDir: t.Dir,
		Kind: l.Kind, Auto: auto, Reason: reason, Argv: argv}
	if err != nil {
		entry.Error = err.Error()
		_ = st.Append(entry)
		return fmt.Errorf("move %s: %w", short(l.SessionID), err)
	}
	_ = st.Append(entry)
	protected := false
	for _, a := range w.input.Accounts {
		protected = protected || (a.Key == t.Key && a.Protected)
	}
	moveLease(e, l.SessionID, t, from, protected)
	fmt.Fprintf(e.stdout, "moved %s %s from %s to %s (%s)\n", short(l.SessionID), l.Name, from, t.Label, l.Kind)
	return nil
}

// moveLease ends the session's lease and leases it on t (setup.md 8.6, Records).
func moveLease(e moveEnv, sid string, t mover.Target, from string, protected bool) {
	g := leases.Ledger{Dir: filepath.Join(e.dirs.State.Path, "next")}
	unlock, err := g.Lock()
	if err != nil {
		return
	}
	defer unlock()
	now := e.now()
	_, _ = g.Mark(sid, "ended", now)
	acct, org, _ := strings.Cut(strings.TrimPrefix(t.Key, "acct:"), "/")
	kind := "aggressive"
	if protected {
		kind = "protected"
	}
	_ = g.Append(leases.Line{V: 1, Op: leases.OpLease, ID: record.NewID(now), At: now.UTC(), Key: t.Key, Account: acct, Org: org,
		Folder: "~/" + filepath.Base(t.Dir), Label: t.Label, Kind: kind, SessionID: sid, Strategy: "move", MovedFrom: from})
	_, _ = g.Mark(sid, "launched", now)
}

// procCmdline reads /proc/<pid>/cmdline (nil elsewhere).
func procCmdline(pid int) []string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
}

// tmuxPanes maps every tmux pane's process to its pane id (nil without tmux).
func tmuxPanes() map[int]string {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil
	}
	out, err := exec.Command("tmux", "list-panes", "-a", "-F", "#{pane_pid} #{pane_id}").Output()
	if err != nil {
		return nil
	}
	m := map[int]string{}
	for _, line := range strings.Split(string(out), "\n") {
		pid, id, ok := strings.Cut(strings.TrimSpace(line), " ")
		if n, err := strconv.Atoi(pid); ok && err == nil {
			m[n] = id
		}
	}
	return m
}

// peersTags are the sessions' peers tags from `wy peers list --all`'s source, when peers are
// on (nil otherwise).
func peersTags() map[string][]string {
	pe, err := peersEnv()
	if err != nil || !pe.Enabled {
		return nil
	}
	es, _, rf := peersListEntries(pe, true)
	if rf != nil {
		return nil
	}
	out := map[string][]string{}
	for _, x := range es {
		out[x.Session] = x.Tags
	}
	return out
}
