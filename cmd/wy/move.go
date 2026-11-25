package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
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
  wy move <session> [<account>] [--dry-run] [--bg] [--now] [--force]
                                         move one session (its id, id prefix or name) to an account
                                         (its label, folder or number), else to the best one; a
                                         session pinned in Claude Code needs --force
  wy move --auto                         one pass of Smart Account Switch (the timer runs it; does
                                         nothing unless account.smart_switch is on)
  wy move --list [--json]                the moves of the last day and the sessions pinned in
                                         Claude Code (the switch never moves those)
  wy move --digest [--since 24h] [--json]
                                         moves, limit hits, weekly room, protected time over cap and
                                         suspect moves of the period
  wy move --selftest                     run --plan in the timer's own environment (systemd-run
                                         with the unit's PATH and WY_* values) and say whether
                                         claude and account.move_launch are found there

Smart Account Switch moves the sessions that are burning (mid-turn too) off an account whose
5-hour or weekly level, projected at its current burn, goes over its cap (setup.md 8.6).
Interactive sessions are never restarted: they get a notice with the command to continue on
an account with room. wy config set account.smart_switch on|off turns it on or off (default off).
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
	digest := fs.Bool("digest", false, "the digest of the period (--since)")
	since := fs.String("since", "24h", "the period of --digest (24h, 7d)")
	selftest := fs.Bool("selftest", false, "run --plan in the timer's own environment and report what it can find")
	noHub := fs.Bool("no-hub", false, "use only this machine's data")
	force := fs.Bool("force", false, "move to the named account even when its limit is (nearly) reached, or a session pinned in Claude Code")
	pin := fs.String("pin-account", "", "retired: pin the session in Claude Code")
	unpin := fs.String("unpin", "", "retired: unpin the session in Claude Code")
	// Flags anywhere: `wy move <id> <account> --dry-run` is a dry run (parseInterspersed); a
	// flag-like word it could not read stops the command before anything happens.
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	st := mover.NewState(e.dirs.State.Path)
	modes := 0
	for _, b := range []bool{*plan, *auto, *list, *digest, *selftest, *pin != "", *unpin != "", len(pos) > 0} {
		if b {
			modes++
		}
	}
	if modes != 1 || len(pos) > 2 {
		moveUsage(e.stderr)
		return errUsage
	}
	switch {
	case *digest:
		return e.printDigest(*since, *asJSON)
	case *selftest:
		return moveSelftest(e)
	case *pin != "" || *unpin != "":
		return errors.New("wy move --pin-account and --unpin are gone: pin or unpin the session in Claude Code itself (its account's jobs/pins.json); Smart Account Switch reads those pins")
	case *list:
		es := st.Log(e.now().Add(-24 * time.Hour))
		pins, warns := e.listPins(e.config())
		if *asJSON {
			if es == nil {
				es = []mover.LogEntry{}
			}
			return writeJSONOut(e.stdout, map[string]any{"moves": es, "pins": pins, "warnings": warns})
		}
		labels := e.config().Slots.Labels
		name := func(dir, key string) string {
			if dir == "" {
				return key
			}
			if l := labels["~/"+filepath.Base(dir)]; l != "" {
				return l
			}
			return "~/" + filepath.Base(dir)
		}
		for _, x := range es {
			reason := x.Reason
			if x.Recheck != "" {
				reason += " (" + x.Recheck + ")"
			}
			fmt.Fprintf(e.stdout, "%s  %s  %s -> %s  %s  %s\n", x.At.Local().Format("01-02 15:04"), short(x.SessionID), name(x.FromDir, x.From), name(x.ToDir, x.To), x.Kind, reason)
		}
		printPinRows(e.stdout, pins)
		for _, x := range warns {
			fmt.Fprintln(e.stdout, "warning: "+x)
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
		r := mover.Plan(w.input)
		r.Skips = w.inScope(r.Skips)
		rcs := st.Rechecks(e.now().Add(-time.Hour))
		if *asJSON {
			doc := w.planDoc(r)
			doc["skipped"] = r.Skips
			if len(rcs) > 0 {
				doc["rechecks"] = rcs
			}
			return writeJSONOut(e.stdout, doc)
		}
		w.printPlan(e.stdout, r, cfg)
		for _, k := range r.Skips {
			l, _ := w.launch(k.Session)
			fmt.Fprintf(e.stdout, "%s %s stays on %s: %s\n", short(k.Session), l.Name, w.accLabel(k.Account), k.Why)
		}
		for _, x := range rcs {
			fmt.Fprintln(e.stdout, recheckLine(x))
		}
		return nil
	case *auto:
		unlock, ok, err := st.PassLock()
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(e.stdout, "Another Smart Account Switch pass is running (it may be waiting for a step to end).")
			return nil
		}
		var once sync.Once
		release := func() { once.Do(unlock) }
		defer release()
		return w.autoPass(e, cfg, st, release)
	}
	l, err := w.find(pos[0])
	if err != nil {
		return err
	}
	if w.pinSet.pinned(l) {
		// Claude Code's own pin (setup.md 8.6, Pinned sessions): only with --force, and the pin
		// stays on the old folder's list (Watchyard never writes Claude Code's pins.json).
		acc := w.labelOfDir[l.Slot]
		if acc == "" {
			acc = "~/" + filepath.Base(l.Slot)
		}
		file := "~/" + filepath.Base(l.Slot) + "/jobs/pins.json"
		if !*force {
			return fmt.Errorf("%s is pinned to %s in Claude Code (%s): pass --force to move it anyway", short(l.SessionID), acc, file)
		}
		fmt.Fprintf(e.stderr, "wy move: %s is pinned to %s in Claude Code; moving it anyway (--force). The pin stays on %s's list (%s): unpin it there in Claude Code, Watchyard does not change that file.\n",
			short(l.SessionID), acc, acc, file)
	}
	if w.outScope[l.SessionID] && !w.scope.all {
		return fmt.Errorf("%s is outside account.smart_switch_scope (%s): it is not moved", short(l.SessionID), w.scope)
	}
	var t mover.Target
	var mv mover.Move
	if len(pos) == 2 {
		t, err = w.account(pos[1])
		if err != nil {
			return err
		}
		if full := w.full(t.Key); full != "" && !*force {
			return fmt.Errorf("%s is %s: the session would stop there at once (pass --force to move it anyway, or leave out the account to let Watchyard choose)", t.Label, full)
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
	// A move of it a moment ago may still be starting its resumed copy (a background session
	// registers after its launch): a second move now would leave two copies.
	for _, x := range st.Log(e.now().Add(-moveSettle)) {
		if x.SessionID == l.SessionID && !*force {
			return fmt.Errorf("%s was moved %s ago (%s -> %s): its resumed copy may still be starting; check `claude agents` there, or pass --force",
				short(l.SessionID), e.now().Sub(x.At).Round(time.Second), w.slotLabel(x.FromDir), w.slotLabel(x.ToDir))
		}
	}
	if !*dry {
		// A switch pass may be waiting to move it: never both.
		unlock, ok, err := st.SessionLock(l.SessionID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("a Smart Account Switch move of %s is under way (waiting for its step): nothing done", short(l.SessionID))
		}
		defer unlock()
	}
	if pr, ok := w.proj[w.keyOfDir[l.Slot]]; ok && pr.Window != "" {
		mv = mover.Move{Window: pr.Window, P: pr.P5, ResetAt: e.now().Add(pr.Reset5), Stopped: w.byID[l.SessionID].Stopped}
		if pr.Window == mover.Win7d {
			mv.P, mv.ResetAt = pr.P7, e.now().Add(pr.Reset7)
		}
		mv.HitAt = pr.HitAt(e.now())
	}
	if t.Key != "" && mover.Org(t.Key) != mover.Org(w.keyOfDir[l.Slot]) {
		mv.OrgChanged = true
	}
	if *bg && (l.Kind == mover.KindTmux || l.Kind == mover.KindWrapped || l.Kind == mover.KindTerminal) {
		l.Kind = mover.KindBackground
	}
	mv.ByHand, mv.Rate = true, w.byID[l.SessionID].Rate
	if l.Status == "busy" && *now {
		mv.MidTurn, l.MidTurn = true, true // it resumes with the note as its prompt
		if !*dry {
			w.betweenSteps(e, st, l, &mv)
		}
	}
	from := w.labelOfDir[l.Slot]
	note := mover.Note(from, t.Label, e.now(), mv, w.chainLabels(st, l.SessionID, e.now())...)
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
	n, err := w.carryOut(e, cfg, st, l, t, note, "by hand", false, mv)
	if err != nil {
		return err
	}
	w.tell(e, []laneNotice{n})
	return nil
}

// moveSettle is how long after a move a by-hand move of the same session waits (--force
// overrides): its resumed copy may not be registered yet.
const moveSettle = 5 * time.Minute

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
	builtAt    time.Time // wall clock when its state began to be read
	noHub      bool      // read with --no-hub (a re-check reads the same way)
	laneRate   float64   // the picker's new-lane burn: young lanes count at it in the destination check
	input      mover.Input
	targets    map[string]mover.Target // by account key
	keyOfDir   map[string]string       // slot dir -> account key
	labelOfDir map[string]string
	proj       map[string]mover.Projection
	byID       map[string]mover.Session
	// sw is the switch state of the last auto pass (the spike filter, the notices).
	sw       mover.SwitchState
	warnings []string
	// pins are the live pinned sessions by account key (they hold its reserve).
	pins map[string][]string
	// pinSet are the pins (Claude Code's jobs/pins.json, the only source); pinWarns
	// are the Claude Code pins files it could not read (the switch's log has each once).
	pinSet   pinSet
	pinWarns []string
	// scope is account.smart_switch_scope; outScope the sessions it leaves out (they count in
	// the forecasts but never move).
	scope    switchScope
	outScope map[string]bool
	// handles are the sessions' peers handles before any move (none when peers are off).
	handles map[string]string
	// tokens are the outbox's token rows of the last week and f5 each account's 5-hour points
	// per token unit (wy burn reads them too).
	tokens []picker.TokenUse
	f5     map[string]float64
	// live are the accounts as the picker read them, by key (the weekly reading's parts for
	// passes.jsonl).
	live map[string]picker.LiveAccount
}

func (e moveEnv) world(noHub bool) (*moveWorld, error) {
	built := time.Now()
	cfg := e.config()
	now := e.now()
	w := &moveWorld{builtAt: built, noHub: noHub, targets: map[string]mover.Target{}, keyOfDir: map[string]string{}, labelOfDir: map[string]string{},
		proj: map[string]mover.Projection{}, byID: map[string]mover.Session{}, outScope: map[string]bool{},
		scope: parseScope(cfg.Account.SmartSwitchScope)}
	inject, err := loadMoveTest(e.getenv, w.scope)
	if err != nil {
		return nil, err
	}
	if inject != nil {
		w.warnings = append(w.warnings, "test levels and rates from "+moveTestEnv+" (scope "+w.scope.String()+")")
	}
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
	// This machine's sessions and its live pinned sessions: their accounts keep a reserve
	// free, for new lanes (the picker) and for the moves (setup.md 8.6, Pinned sessions).
	params := moveParams(cfg)
	if inject != nil {
		inject.applyParams(&params)
	}
	sys := e.sys
	if sys == nil {
		sys = &mover.Sys{FS: identity.OSFS{}, Proc: peerspkg.NewProc(), Cmdline: procCmdline, Panes: tmuxPanes,
			State: mover.NewState(e.dirs.State.Path)}
	}
	w.launches = sys.Sessions(islots)
	st := mover.NewState(e.dirs.State.Path)
	var tags map[string][]string
	if e.tags != nil {
		tags = e.tags()
	} else {
		tags, w.handles = peersTags()
	}
	var dirs []string
	for _, s := range slots {
		dirs = append(dirs, s.Dir)
	}
	pins := loadPins(dirs, func(msg string) {
		w.warnings = append(w.warnings, msg)
		w.pinWarns = append(w.pinWarns, msg)
	})
	if x := ignoredPins(st); x != "" {
		w.warnings = append(w.warnings, x)
	}
	w.pinSet = pins
	w.pins = livePins(w.launches, w.keyOfDir, pins, now)
	in.PinReserves = pinReserves(params, w.pins)
	in.Mover = moverView(cfg, w.launches, w.keyOfDir, ob.Tokens, in.Candidates, now)
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
	w.laneRate = lin.SwitchLaneRate()

	// Accounts in the picker's order (its first pick first); Plan puts protected ones last.
	order := map[string]int{}
	drain := map[string]bool{}
	for i, r := range res.Ranked {
		if k := w.keyOfDir[r.Dir]; k != "" {
			if _, ok := order[k]; !ok {
				order[k] = i
			}
			drain[k] = drain[k] || (r.Lanes != nil && r.Lanes.Class == "drain")
		}
	}
	sort.SliceStable(accs, func(i, j int) bool { return rankOf(order, accs[i].Key) < rankOf(order, accs[j].Key) })
	f5 := map[string]float64{}
	for _, a := range accs {
		t, loggedIn := w.targets[a.Key]
		ma := mover.Account{Key: a.Key, Label: t.Label, Protected: a.Protected, Ratio: a.Ratio, Stale: a.Stale, LoggedIn: loggedIn,
			Drain: drain[a.Key], W5: mover.Window{Level: a.FiveHour, Reset: a.FiveReset}, W7: mover.Window{Level: a.Weekly, Reset: a.WeeklyReset},
			ReadAt: a.ReadingAt, DrainUntil: a.DrainUntil}
		if ma.Label == "" {
			ma.Label = a.Key
		}
		ma.Pins = len(w.pins[a.Key])
		ma.Reserve5, ma.Reserve7 = params.PinReserve(ma.Pins)
		if inject != nil {
			inject.applyAccount(&ma, now)
		}
		w.input.Accounts = append(w.input.Accounts, ma)
		f5[a.Key] = a.F5
		if w.live == nil {
			w.live = map[string]picker.LiveAccount{}
		}
		w.live[a.Key] = a
	}

	// Sessions: the registry of every folder, with their burn (their token rows of the burn
	// window, helpers included, × f) and their context.
	started := map[string]time.Time{}
	for _, l := range lanes {
		if l.SID != "" {
			started[l.SID] = l.T0
		}
	}
	w.tokens, w.f5 = ob.Tokens, f5
	burns := burnStats(ob.Tokens, now, params.BurnWindow)
	ctx := map[string]float64{}
	limitStop := map[string]bool{} // its last turn ended on a limit reply
	prompts := map[string][]promptSeen{}
	if recs, err := status.Load(filepath.Join(e.dirs.State.Path, "outbox"), now.Add(-status.Window)); err == nil {
		lastEnd := map[string]time.Time{}
		for _, r := range recs {
			var p struct {
				SessionID string `json:"session_id"`
				Event     string `json:"hook_event_name"`
				Error     string `json:"error"`
			}
			if r.Kind != record.KindHookEvent || json.Unmarshal(r.Payload, &p) != nil {
				continue
			}
			t, err := time.Parse(time.RFC3339Nano, r.TS)
			if err != nil {
				continue
			}
			// Stopped by a limit: the last turn-boundary event of the session is a StopFailure
			// for rate_limit (Claude Code's limit reply). A later prompt or a normal Stop means it
			// went on, or finished its turn: idle is not stopped (owner 2026-11-24).
			switch p.Event {
			case "UserPromptSubmit", "Stop", "StopFailure":
				if !t.Before(lastEnd[p.SessionID]) {
					lastEnd[p.SessionID] = t
					limitStop[p.SessionID] = p.Event == "StopFailure" && p.Error == "rate_limit"
				}
			}
			if p.Event == "UserPromptSubmit" {
				prompts[p.SessionID] = append(prompts[p.SessionID], promptSeen{at: t,
					owner: string(r.Derived["peer_prompt"]) != "true" && string(r.Derived["move_prompt"]) != "true"})
			}
		}
		for id, s := range status.Adapt(recs) {
			if s.ContextPct != nil {
				ctx[id] = *s.ContextPct
			}
		}
	}
	leased := map[string]bool{}
	ledger2 := leases.Ledger{Dir: filepath.Join(e.dirs.State.Path, "next")}
	ls2, _, _ := ledger2.Read(now)
	for _, l := range ls2 {
		if l.SessionID != "" {
			leased[l.SessionID] = true
		}
	}
	firstLeases(started, ls2)
	expect := laneExpect(lanes, ls2, now)
	log := st.Log(now.Add(-6 * time.Hour))
	last := map[string]mover.LogEntry{}
	for _, x := range log {
		last[x.SessionID] = x
	}
	w.sw = st.LoadSwitch(now, params.Pass)
	w.input.Now, w.input.Params, w.input.Recent = now, params, mover.Records(log)
	w.input.LaneRate = w.laneRate
	w.input.Streak = w.sw.Streak
	for _, l := range w.launches {
		key := w.keyOfDir[l.Slot]
		if key == "" {
			continue
		}
		s := mover.Session{ID: l.SessionID, Account: key, Name: l.Name, Expected: expect[l.SessionID]}
		s.Rate = burns[l.SessionID].rate(f5[key])
		s.Started = started[l.SessionID]
		s.ModelFactor = mover.ModelFactor(topModel(burns[l.SessionID].Calls, l.Argv))
		if inject != nil {
			if r, ok := inject.rate(l.SessionID); ok {
				s.Rate = r
			}
		}
		s.Busy = l.Status == "busy"
		// Its first prompt was its task when it is a lane, or when it was started with a prompt
		// (a moved session's command line carries the move note instead: its own lease decides).
		_, moved := last[l.SessionID]
		s.Interactive = interactive(l, prompts[l.SessionID], leased[l.SessionID] || (!moved && mover.HasPrompt(l.Argv)))
		s.Typed = lastOwnerPrompt(prompts[l.SessionID])
		if p, ok := ctx[l.SessionID]; ok {
			s.Context = p / 100 * 200_000
		}
		if pins.pinned(l) {
			s.Pinned, s.PinFrom = true, "in Claude Code"
		}
		if !w.scope.in(l.SessionID, tags[l.SessionID]) {
			s.Pinned, w.outScope[l.SessionID] = true, true
		}
		s.Movable = l.Kind != mover.KindCloud
		s.Copies = len(l.Copies)
		s.Moving = st.Moving(l.SessionID)
		if x, ok := last[l.SessionID]; ok {
			s.MovedAt, s.MovedFrom = x.At, x.From
		}
		// Stopped: a limit ended its last turn and that window is still full. An idle session
		// on a full account that finished its turn normally is not stopped.
		for _, a := range w.input.Accounts {
			if a.Key == key && limitStop[l.SessionID] && (a.W5.Level >= 100 || a.W7.Level >= 100) {
				s.Stopped = true
			}
		}
		w.input.Sessions = append(w.input.Sessions, s)
		w.byID[s.ID] = s
	}
	// The account's burn (setup.md 8.6, Burn): its sessions' meter here plus other machines'
	// sessions the hub measured, cross-checked with the readings' slope (which also sees
	// claude.ai and sessions the hub does not measure): the readings are used when they are
	// over twice the meter on two passes in a row.
	meter := map[string]float64{}
	for _, s := range w.input.Sessions {
		if !s.Stopped {
			meter[s.Account] += s.Rate
		}
	}
	readStreak := map[string]int{}
	for i := range w.input.Accounts {
		a := &w.input.Accounts[i]
		remote := remoteRate(in.Candidates, w.keyOfDir, a.Key, now, params.BurnWindow, f5[a.Key])
		a.Rate = meter[a.Key] + remote
		a.Meter = a.Rate
		if remote > 0 {
			a.RateNote = fmt.Sprintf("%.1f points/h of it from other machines' sessions (hub)", remote)
		}
		if inject != nil && inject.Accounts[a.Label].Rate != nil {
			a.Rate, a.Meter, a.RateNote = *inject.Accounts[a.Label].Rate, *inject.Accounts[a.Label].Rate, "injected"
			if inject.Accounts[a.Label].Readings == nil {
				continue
			}
		}
		rd, ok := readingSlope(in.Candidates, w.keyOfDir, a.Key, now, params.BurnWindow)
		if inject != nil && inject.Accounts[a.Label].Readings != nil {
			rd, ok = *inject.Accounts[a.Label].Readings, true
		}
		if !ok {
			continue
		}
		switch {
		case rd > 2*a.Rate && rd > 1:
			readStreak[a.Key] = w.sw.ReadStreak[a.Key] + 1
			if readStreak[a.Key] < 2 {
				a.RateNote = fmt.Sprintf("the readings rose %.1f points/h, over twice the meter's %.1f: used if it holds on the next pass", rd, a.Rate)
				continue
			}
			a.RateNote = fmt.Sprintf("the readings rose %.1f points/h, over twice the meter's %.1f on %d passes: used the readings", rd, a.Rate, readStreak[a.Key])
			a.Rate, a.FromReadings = rd, true
		case a.Rate > 2*rd && a.Rate > 1:
			a.RateNote = fmt.Sprintf("the meter's %.1f points/h is over twice the readings' %.1f: used the meter", a.Rate, rd)
		}
	}
	w.sw.ReadStreak = readStreak
	// The projections shown and kept (--plan, passes.jsonl) are Plan's: young sessions'
	// burn left out (setup.md 8.6, Young sessions). The readings above were compared with
	// the full meter.
	gated := mover.GateYoung(w.input)
	byAcc := map[string][]mover.Session{}
	for _, s := range gated.Sessions {
		byAcc[s.Account] = append(byAcc[s.Account], s)
	}
	for _, a := range gated.Accounts {
		w.proj[a.Key] = params.Project(a, byAcc[a.Key], 0, now)
	}
	return w, nil
}

// promptSeen is one UserPromptSubmit of a session: owner is false for a peer message or a
// move note.
type promptSeen struct {
	at    time.Time
	owner bool
}

// interactive reports whether session l is a human-facing session (setup.md 8.6,
// Interactive: moved like any other, with a message where the human sees it): a plain terminal or `wy run` session; a tmux pane with Remote
// Control on or a prompt from the owner in its life. With task set, its first prompt was its
// task (a lane leased by `wy account next`, or a session started with a prompt on its command
// line), not the owner typing. Background and headless sessions are agents.
func interactive(l mover.Launch, ps []promptSeen, task bool) bool {
	switch l.Kind {
	case mover.KindTerminal, mover.KindWrapped:
		return true
	case mover.KindTmux:
		if l.RemoteControl {
			return true
		}
		sort.Slice(ps, func(i, j int) bool { return ps[i].at.Before(ps[j].at) })
		for i, p := range ps {
			if i == 0 && task {
				continue
			}
			if p.owner {
				return true
			}
		}
	}
	return false
}

// hasRemoteControlFlag reports whether argv turns Remote Control on (kept on resume).
func hasRemoteControlFlag(argv []string) bool {
	for _, a := range argv {
		if a == "--remote-control" || strings.HasPrefix(a, "--remote-control=") {
			return true
		}
	}
	return false
}

// lastOwnerPrompt is when the owner last typed into the session (zero: never seen).
func lastOwnerPrompt(ps []promptSeen) time.Time {
	var t time.Time
	for _, p := range ps {
		if p.owner && p.at.After(t) {
			t = p.at
		}
	}
	return t
}

// moveParams are the decision rules from the account.move_* settings.
func moveParams(cfg config.Config) mover.Params {
	a := cfg.Account
	p := mover.DefaultParams
	mins := func(v float64) time.Duration { return time.Duration(v * float64(time.Minute)) }
	p.Cap5, p.Cap7, p.Cap5Protected, p.Cap7Protected = a.MoveCap5h, a.MoveCap7d, a.MoveCap5hProtected, a.MoveCap7dProtected
	p.Hysteresis = a.MoveHysteresis
	if a.MoveBurnMin > 0 {
		p.BurnWindow = mins(a.MoveBurnMin)
	}
	p.WeekSpan = mins(60 * a.MoveWeekSpanH)
	p.DestHorizon = 0 // retired: a destination is judged as the trigger projects it (move_dest_horizon_min ignored)
	p.YoungGate = mins(a.MoveYoungMin)
	p.MinBurn = math.Max(0, a.MoveMinBurn)
	p.PullMinBurn = math.Max(0, a.MovePullMinBurn)
	p.SpikePasses, p.SpikeNear = int(a.MoveSpikePasses), mins(a.MoveSpikeNearMin)
	p.Cooldown, p.NoReturn = mins(a.MoveCooldownMin), mins(a.MoveNoReturnMin)
	p.AccountMoves, p.AccountWindow = int(a.MoveAccountMoves), mins(a.MoveAccountWindowMin)
	p.AccountBurn = math.Max(0, a.MoveAccountBurn)
	p.ExpiringRoom = time.Duration(math.Max(0, a.MoveExpiringH) * float64(time.Hour))
	p.StaleAfter = mins(math.Max(0, a.MoveStaleMin))
	p.StoppedWait, p.OwnerGrace, p.StepWait = mins(a.MoveStoppedWaitMin), mins(a.MoveOwnerGraceMin), mins(a.MoveStepWaitMin)
	p.PassWait = time.Duration(math.Max(0, a.MovePassWaitS) * float64(time.Second))
	p.PinReserve5, p.PinReserve7 = math.Max(0, a.PinReserve5h), math.Max(0, a.PinReserve7d)
	if a.MoveOrgs != "" {
		p.Orgs = a.MoveOrgs
	}
	return p
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
		l := hits[0]
		if len(l.Copies) > 0 {
			// Two processes of one session (a resumed copy next to the old one): moving
			// either would make a third. Nothing moves until one is stopped.
			where := []string{fmt.Sprintf("pid %d on %s", l.PID, w.slotLabel(l.Slot))}
			for _, c := range l.Copies {
				where = append(where, fmt.Sprintf("pid %d on %s", c.PID, w.slotLabel(c.Slot)))
			}
			return mover.Launch{}, fmt.Errorf("%d copies of %s are running (%s): stop the extra one first; nothing moved",
				len(where), short(l.SessionID), strings.Join(where, ", "))
		}
		return l, nil
	}
	return mover.Launch{}, fmt.Errorf("%q matches %d sessions: give more of the id", q, len(hits))
}

// slotLabel is the label of config folder dir (~/.claude-3 when it has none).
func (w *moveWorld) slotLabel(dir string) string {
	if l := w.labelOfDir[dir]; l != "" {
		return l
	}
	return "~/" + filepath.Base(dir)
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

// full says why an account cannot take a session now ("" when it can): a window at or above
// 98%, or readings too old to know.
func (w *moveWorld) full(key string) string {
	for _, a := range w.input.Accounts {
		if a.Key != key {
			continue
		}
		switch {
		case a.W5.Level >= 98:
			return fmt.Sprintf("at %.0f%% of its 5-hour limit", a.W5.Level)
		case a.W7.Level >= 98:
			return fmt.Sprintf("at %.0f%% of its weekly limit", a.W7.Level)
		}
	}
	return ""
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

func (w *moveWorld) planDoc(r mover.Result) map[string]any {
	var ms []planMove
	for _, m := range r.Moves {
		pm := planMove{Move: m, ToName: w.targets[m.To].Label}
		if l, ok := w.launch(m.Session); ok {
			pm.Kind = l.Kind
		}
		ms = append(ms, pm)
	}
	alerts := []map[string]any{}
	for _, a := range r.Alerts {
		alerts = append(alerts, map[string]any{"session": a.Session, "account": a.Account, "window": a.Window, "projected": a.P,
			"rate": a.Rate, "hit_at": a.HitAt, "reset_at": a.ResetAt, "command": w.pinCommand(a.Session)})
	}
	return map[string]any{"accounts": r.Accounts, "moves": ms, "alerts": alerts, "warnings": w.warnings,
		"pins": pinRows(w.launches, w.labelOfDir, w.keyOfDir, w.pinSet, w.pins)}
}

// accLabel is the label of account key (the key when none is known here).
func (w *moveWorld) accLabel(key string) string {
	for _, a := range w.input.Accounts {
		if a.Key == key && a.Label != "" {
			return a.Label
		}
	}
	return key
}

// inScope drops the skips of sessions outside account.smart_switch_scope (never candidates).
func (w *moveWorld) inScope(ks []mover.Skip) []mover.Skip {
	var out []mover.Skip
	for _, k := range ks {
		if !w.outScope[k.Session] {
			out = append(out, k)
		}
	}
	return out
}

func (w *moveWorld) printPlan(out io.Writer, r mover.Result, cfg config.Config) {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ACCOUNT\t5H\t7D\tBURN/H\tP5\tP7\tCAPS\tPIN RESERVE\tSTATE")
	loggedIn := map[string]bool{}
	drainUntil := map[string]time.Time{}
	for _, a := range w.input.Accounts {
		loggedIn[a.Key] = a.LoggedIn
		if a.DrainUntil.After(w.input.Now) {
			drainUntil[a.Key] = a.DrainUntil
		}
	}
	for _, pr := range r.Accounts {
		state := "under its caps"
		switch {
		case pr.Triggered && pr.Hard:
			state = pr.Window + " cap reached now: moving its burners"
		case pr.Triggered:
			state = pr.Window + " over its cap: moving its burners"
		case pr.Held:
			state = fmt.Sprintf("%s over its cap, pass %d of %d (spike filter)", pr.Window, pr.Streak, w.input.Params.SpikePasses)
		}
		if pr.StaleFor > 0 {
			state = fmt.Sprintf("data stale (%s), no moves", staleAge(pr.StaleFor))
		}
		if pr.Hit > 0 && pr.StaleFor == 0 {
			state += fmt.Sprintf("; 100%% in %s", pr.Hit.Round(time.Minute))
		}
		if pr.Left != "" {
			state += "; " + pr.Left
		}
		if pr.Expiring {
			state += fmt.Sprintf("; expiring room: weekly cap %.0f%% until its weekly reset in %s", pr.Cap7+pr.Reserve7, pr.Reset7.Round(time.Minute))
		}
		if u, ok := drainUntil[pr.Key]; ok {
			state = mover.OwnerDrainText(u, w.input.Now) + "; " + state
		}
		if !loggedIn[pr.Key] {
			state += "; not logged in here (never a destination)"
		}
		reserve := "-"
		if pr.Pins > 0 {
			reserve = fmt.Sprintf("%d pinned: %.0f/%.0f", pr.Pins, pr.Reserve5, pr.Reserve7)
		}
		fmt.Fprintf(tw, "%s\t%.0f%%\t%.0f%%\t%.1f\t%.0f%%\t%.0f%%\t%.0f/%.0f\t%s\t%s\n", pr.Label, pr.Level5, pr.Level7, pr.R5, pr.P5, pr.P7,
			pr.Cap5, pr.Cap7, reserve, state)
	}
	_ = tw.Flush()
	for _, pr := range r.Accounts {
		if pr.RateNote != "" {
			fmt.Fprintf(out, "%s burn: %s\n", pr.Label, pr.RateNote)
		}
	}
	state := "off: nothing moves by itself"
	if cfg.Account.SmartSwitch {
		state = "on"
	}
	fmt.Fprintf(out, "\nSmart Account Switch is %s (organisations: %s).\n", state, w.input.Params.Orgs)
	pinned := false
	for _, pr := range r.Accounts {
		pinned = pinned || pr.Pins > 0
	}
	if p := w.input.Params; pinned && (p.PinReserve5 > 0 || p.PinReserve7 > 0) {
		fmt.Fprintf(out, "Each live pinned session keeps %.0f%% of its account's 5-hour window and %.0f%% of its week free (CAPS are less that).\n",
			p.PinReserve5, p.PinReserve7)
	}
	printPinRows(out, pinRows(w.launches, w.labelOfDir, w.keyOfDir, w.pinSet, w.pins))
	for _, a := range r.Alerts {
		x := mover.PinAlert{At: w.input.Now, Session: a.Session, Name: w.byID[a.Session].Name, Account: w.accLabel(a.Account), Window: a.Window, HitAt: a.HitAt,
			ResetAt: a.ResetAt, Rate: a.Rate, Command: w.pinCommand(a.Session)}
		fmt.Fprintln(out, "alert: "+x.Text())
	}
	if len(r.Moves) == 0 {
		fmt.Fprintln(out, "No session needs a move.")
	}
	for _, m := range r.Moves {
		l, _ := w.launch(m.Session)
		to := w.targets[m.To].Label
		if to == "" {
			to = "(none)"
		}
		kind := string(l.Kind)
		switch {
		case l.Kind == mover.KindTerminal:
			kind += ", interactive: goes on as a background session; its terminal gets a message"
		case m.Interactive:
			kind += ", interactive: goes on in place; it gets a message there"
		}
		fmt.Fprintf(out, "%s %s (%s): %s -> %s: %s\n", short(m.Session), l.Name, kind, w.accLabel(m.From), to, m.Reason)
	}
	for _, x := range w.warnings {
		fmt.Fprintln(out, "warning: "+x)
	}
}

// humanMessage is what the human sees where session l ran (setup.md 8.6, Interactive): in
// its tmux pane or `wy run` terminal above the resumed session, or written to its plain
// terminal, which the session leaves for a background session on t.
func (w *moveWorld) humanMessage(cfg config.Config, l mover.Launch, t mover.Target, mv mover.Move, at time.Time) string {
	from := w.labelOfDir[l.Slot]
	if from == "" {
		from = filepath.Base(l.Slot)
	}
	var b strings.Builder
	if mv.ByHand {
		fmt.Fprintf(&b, "[Watchyard] This session was moved by hand by the owner from %s to %s at %s", from, t.Label, at.Local().Format("15:04"))
	} else {
		fmt.Fprintf(&b, "[Watchyard] This session moved from %s to %s at %s", from, t.Label, at.Local().Format("15:04"))
	}
	switch {
	case mv.ByHand:
	case mv.Stopped && mv.Window != "":
		fmt.Fprintf(&b, " (it was stopped at %s's %s limit)", from, mv.Window)
	case mv.Window != "" && mv.HitAt.After(at):
		fmt.Fprintf(&b, " (%s's %s would hit 100%% at %s)", from, mv.Window, mover.Clock(mv.HitAt, at))
	}
	if l.Kind == mover.KindTerminal {
		agents := mover.Shell(mover.Command(cfg.Account.MoveLaunch, t, []string{"agents"}))
		attach := mover.Shell(mover.Command(cfg.Account.MoveLaunch, t, []string{"attach", short(l.SessionID)}))
		fmt.Fprintf(&b, ". It continues as a background agent on %s. See it: %s. Continue it in this terminal: %s.", t.Label, agents, attach)
	} else {
		fmt.Fprintf(&b, ". It continues here under %s.", t.Label)
	}
	switch {
	case l.RemoteControl && hasRemoteControlFlag(l.Argv):
		b.WriteString(" Remote Control comes back on by itself (it was started with --remote-control); if it does not, turn it on again with /remote-control.")
	case l.RemoteControl:
		fmt.Fprintf(&b, " Remote Control is off now: turn it on again with /remote-control (unless it comes on by itself under %s).", t.Label)
	}
	return b.String()
}

// autoPass carries out the plan (the moves that have a destination) and keeps the switch
// state for the next pass. The pass lock (release) is held only while it plans: each move holds
// its session's lock instead, the mid-turn ones wait for their step side by side, at most
// Params.PassWait in all, so the next pass is never blind behind a waiting move.
func (w *moveWorld) autoPass(e moveEnv, cfg config.Config, st mover.State, release func()) error {
	for _, x := range mover.Reap(moveHost(e, st), st, e.now()) {
		fmt.Fprintf(e.stdout, "stopped a copy of %s that the supervisor of %s restarted after its move\n", short(x.SessionID), filepath.Base(x.FromDir))
	}
	if msg := strings.Join(w.pinWarns, "; "); st.Changed("pins-warned", msg) && msg != "" {
		fmt.Fprintln(e.stdout, "warning: "+msg)
	}
	if age, old := worldTooOld(w.builtAt, time.Now()); old {
		// Its config and levels are from before (live 2026-11-25 05:40: a pass that read its
		// state before an account was set protected moved a session onto aggressive caps
		// minutes later). The next pass reads them again.
		fmt.Fprintf(e.stdout, "this pass read its state %s ago (the hub or the disk was slow): no moves\n", age.Round(time.Second))
		return nil
	}
	r := mover.Plan(w.input)
	var first error
	type pick struct {
		m      mover.Move
		l      mover.Launch
		unlock func()
	}
	var picks []pick
	picked := map[string]bool{}
	for _, m := range r.Moves {
		l, ok := w.launch(m.Session)
		if !ok || m.To == "" || !l.Kind.Movable() {
			continue
		}
		unlock, ok, err := st.SessionLock(m.Session)
		if err != nil || !ok {
			fmt.Fprintf(e.stdout, "%s %s: a move of it is already under way\n", short(m.Session), l.Name)
			continue
		}
		l.MidTurn = m.MidTurn
		picks = append(picks, pick{m, l, unlock})
		picked[m.Session] = true
	}
	// A session that waited for its step in an earlier pass and is no longer picked waits no more.
	for _, s := range w.input.Sessions {
		if !picked[s.ID] && !s.Moving {
			st.ClearWait(s.ID)
		}
	}
	w.pinAlerts(e.stdout, st, r, e.now())
	w.staleAlert(e, cfg, st, r)
	w.recordPass(st, r, e.now())
	w.sw.At, w.sw.Streak = e.now(), r.Streak
	if err := st.SaveSwitch(w.sw); err != nil {
		first = err
	}
	release() // planning done: the next pass may start while these moves wait

	var mu sync.Mutex
	var told []laneNotice
	var wg sync.WaitGroup
	for _, p := range picks {
		wg.Add(1)
		go func(p pick) {
			defer wg.Done()
			defer p.unlock()
			m, l := p.m, p.l
			if m.MidTurn && !w.waitStep(e, st, &mu, l, &m) {
				return // still inside its step: the next pass waits on
			}
			// The waits run side by side; the moves themselves one at a time, each as soon as
			// its session is at a step (the move log, the ledger and the output are shared).
			mu.Lock()
			defer mu.Unlock()
			st.ClearWait(m.Session)
			t := w.targets[m.To]
			if m.MidTurn && time.Since(w.builtAt) >= recheckAfter {
				var ok bool
				if m, t, ok = w.recheck(e, st, l, m); !ok {
					return
				}
			}
			note := mover.Note(w.accLabel(m.From), t.Label, e.now(), m, w.chainLabels(st, m.Session, e.now())...)
			n, err := w.carryOut(e, cfg, st, l, t, note, m.Reason, true, m)
			if err != nil {
				if first == nil {
					first = err
				}
				return
			}
			told = append(told, n)
		}(p)
	}
	wg.Wait()
	w.tell(e, told) // one notice per parent for the whole pass
	return first
}

// recheckLine is a re-check of the last hour for --plan.
func recheckLine(x mover.RecheckEntry) string {
	at := x.At.Local().Format("15:04")
	if x.To == "" {
		return fmt.Sprintf("re-check %s: %s %s was planned %s -> %s, not moved: %s", at, short(x.SessionID), x.Name, x.From, x.Planned, strings.TrimPrefix(x.Why, "re-checked after its step wait: "))
	}
	return fmt.Sprintf("re-check %s: %s %s was planned %s -> %s, moved to %s instead (the state read again after its step wait)", at, short(x.SessionID), x.Name, x.From, x.Planned, x.To)
}

// recheckAfter: a move carried out this long after its pass began to read its state is
// re-chosen on state read again (setup.md 8.6, Re-check). A variable for the tests.
var recheckAfter = 5 * time.Second

// beforeRecheck runs just before a re-check reads the state again (tests: the state changes
// during the step wait).
var beforeRecheck func()

// recheck reads the state again after m's step wait and re-chooses (mover.Recheck): false
// drops the move; a changed destination is recorded (rechecks.jsonl, the move's recheck field)
// and printed. A failed read keeps the move as planned.
func (w *moveWorld) recheck(e moveEnv, st mover.State, l mover.Launch, m mover.Move) (mover.Move, mover.Target, bool) {
	t := w.targets[m.To]
	if beforeRecheck != nil {
		beforeRecheck()
	}
	w2, err := e.world(w.noHub)
	if err != nil {
		fmt.Fprintf(e.stdout, "%s %s: the re-check after its step wait could not read the state (%v): moved as planned\n", short(m.Session), l.Name, err)
		return m, t, true
	}
	nm, why, ok := mover.Recheck(w2.input, m)
	rec := mover.RecheckEntry{At: e.now(), SessionID: m.Session, Name: l.Name, From: w.accLabel(m.From), Planned: t.Label, Why: why}
	if !ok {
		fmt.Fprintf(e.stdout, "%s %s: not moved to %s: %s\n", short(m.Session), l.Name, t.Label, why)
		_ = st.AppendRecheck(rec)
		return m, t, false
	}
	if nt, ok := w2.targets[nm.To]; ok {
		t = nt
	}
	if nm.Recheck != "" {
		rec.To = t.Label
		fmt.Fprintf(e.stdout, "%s %s: %s\n", short(m.Session), l.Name, nm.Recheck)
		_ = st.AppendRecheck(rec)
	}
	return nm, t, true
}

// waitStep waits, within this pass's share (Params.PassWait), for mid-turn session l to be
// between two tool calls, and records on mv what the note says. Its wait counts from its
// first pass (State.WaitStarted): after Params.StepWait in all (or a minute before the
// projected hit) it is moved inside the step; before that, a session still inside its step
// when this pass's share runs out is left for the next pass (false).
func (w *moveWorld) waitStep(e moveEnv, st mover.State, mu *sync.Mutex, l mover.Launch, mv *mover.Move) bool {
	p := w.input.Params
	now := time.Now() // wall time, as the wait itself (WaitStep)
	began := st.WaitStarted(l.SessionID, now)
	deadline := began.Add(p.StepWait)
	if !mv.HitAt.IsZero() {
		if hit := now.Add(mv.HitAt.Sub(e.now()) - time.Minute); hit.Before(deadline) {
			deadline = hit
		}
	}
	left := deadline.Sub(now)
	if left < 0 {
		left = 0
	}
	share := min(left, p.PassWait)
	host := moveHost(e, st)
	start := time.Now()
	s, between := st.WaitStep(l.SessionID, share, func() bool { return host.Alive(l) })
	mu.Lock()
	defer mu.Unlock()
	switch {
	case between:
		mv.StepEnded = true
		if d := time.Since(start); d > time.Second {
			fmt.Fprintf(e.stdout, "%s %s: waited %s for its step to end\n", short(l.SessionID), l.Name, d.Round(time.Second))
		}
	case s.In && share < left:
		fmt.Fprintf(e.stdout, "%s %s: still inside a step (%s) after %s: the next pass waits on (up to %s in all)\n",
			short(l.SessionID), l.Name, s.Tool, time.Since(start).Round(time.Second), p.StepWait)
		return false
	case s.In:
		mv.StepTool, mv.StepCmd = s.Tool, s.Cmd
		fmt.Fprintf(e.stdout, "%s %s: still inside a step (%s) after %s: moved anyway\n", short(l.SessionID), l.Name, s.Tool, (now.Sub(began) + time.Since(start)).Round(time.Second))
	}
	return true
}

// maxWorldAge: a pass whose state (config, levels, sessions) took longer than this to read
// moves nothing.
const maxWorldAge = 2 * time.Minute

// worldTooOld says how long ago (wall clock) a pass began to read its state, and whether that
// is over maxWorldAge.
func worldTooOld(built, wall time.Time) (time.Duration, bool) {
	age := wall.Sub(built)
	return age, !built.IsZero() && age > maxWorldAge
}

// betweenSteps waits until session l, moved by hand, is between two tool calls (setup.md
// 8.6, Between steps), up to account.move_pass_wait_s (a pass's share; the owner is waiting
// at the prompt: live 2026-11-25, 3.5 minutes per move at the full step wait), the step wait
// or until the projected hit, whichever is sooner, and records on mv what the note says: the
// last step ended, or the step it is stopped inside.
func (w *moveWorld) betweenSteps(e moveEnv, st mover.State, l mover.Launch, mv *mover.Move) {
	max := w.input.Params.StepWait
	if pw := w.input.Params.PassWait; pw > 0 && pw < max {
		max = pw
	}
	if !mv.HitAt.IsZero() {
		if until := mv.HitAt.Sub(e.now()) - time.Minute; until < max {
			max = until
		}
	}
	if max < 0 {
		max = 0
	}
	host := moveHost(e, st)
	if x := st.StepNow(l.SessionID, time.Now()); max >= 5*time.Second && x.In {
		fmt.Fprintf(e.stdout, "%s %s is inside a step (%s): waiting up to %s for it to end\n", short(l.SessionID), l.Name, x.Tool, max.Round(time.Second))
	}
	start := time.Now()
	s, between := st.WaitStep(l.SessionID, max, func() bool { return host.Alive(l) })
	switch {
	case between:
		mv.StepEnded = true
		if d := time.Since(start); d > time.Second {
			fmt.Fprintf(e.stdout, "%s %s: waited %s for its step to end\n", short(l.SessionID), l.Name, d.Round(time.Second))
		}
	case s.In:
		mv.StepTool, mv.StepCmd = s.Tool, s.Cmd
		fmt.Fprintf(e.stdout, "%s %s: still inside a step (%s) after %s: moved anyway\n", short(l.SessionID), l.Name, s.Tool, time.Since(start).Round(time.Second))
	}
}

// moveHost is the host that stops and starts sessions (tests set e.host).
func moveHost(e moveEnv, st mover.State) mover.Host {
	if e.host != nil {
		return e.host
	}
	return mover.RealHost{Proc: peerspkg.NewProc(), State: st}
}

// carryOut moves l to t and records it: the move log, the lease ledger (the old lease ends,
// a new one on t carries the session) and the note.
func (w *moveWorld) carryOut(e moveEnv, cfg config.Config, st mover.State, l mover.Launch, t mover.Target, note, reason string, auto bool, mv mover.Move) (laneNotice, error) {
	host := moveHost(e, st)
	// The steps in flight now are the old process's: it is stopped next, and no PostToolUse
	// comes for them.
	st.EndSteps(l.SessionID, e.now())
	switch l.Kind {
	case mover.KindTmux, mover.KindWrapped, mover.KindTerminal:
		l.Message = w.humanMessage(cfg, l, t, mv, e.now())
	}
	log := filepath.Join(st.Dir, "logs", l.SessionID+".log")
	argv, err := mover.Execute(host, l, t, cfg.Account.MoveLaunch, note, log)
	from := w.labelOfDir[l.Slot]
	entry := mover.LogEntry{At: e.now(), SessionID: l.SessionID, Name: l.Name, From: w.keyOfDir[l.Slot], To: t.Key, FromDir: l.Slot, ToDir: t.Dir,
		Kind: l.Kind, Auto: auto, Reason: reason, Argv: argv, Rate: mv.Rate, RateKnown: true, Recheck: mv.Recheck}
	if err != nil {
		entry.Error = err.Error()
		_ = st.Append(entry)
		return laneNotice{}, fmt.Errorf("move %s: %w", short(l.SessionID), err)
	}
	_ = st.Append(entry)
	protected := false
	for _, a := range w.input.Accounts {
		protected = protected || (a.Key == t.Key && a.Protected)
	}
	parent := moveLease(e, l.SessionID, t, from, protected)
	fmt.Fprintf(e.stdout, "moved %s %s from %s to %s (%s)\n", short(l.SessionID), l.Name, from, t.Label, l.Kind)
	return laneNotice{Parent: parent, Session: l.SessionID, Name: l.Name, From: from, To: t.Label, ToDir: t.Dir, At: e.now(), Move: mv}, nil
}

// moveLease ends the session's lease and leases it on t (setup.md 8.6, Records). The new
// lease keeps the parent of the old one, which it returns ("" when none).
func moveLease(e moveEnv, sid string, t mover.Target, from string, protected bool) string {
	g := leases.Ledger{Dir: filepath.Join(e.dirs.State.Path, "next")}
	unlock, err := g.Lock()
	if err != nil {
		return ""
	}
	defer unlock()
	now := e.now()
	parent := ""
	var exp leases.Line // the session's expected end, kept on the new lease
	if ls, _, err := g.Read(now); err == nil {
		for _, l := range ls {
			if l.SessionID == sid && l.ExpectEnd != nil {
				exp.Expect, exp.ExpectMin, exp.ExpectEnd = l.Expect, l.ExpectMin, l.ExpectEnd
			}
			if l.SessionID == sid && l.Parent != "" {
				parent = l.Parent
			}
		}
	}
	_, _ = g.Mark(sid, "ended", now)
	acct, org, _ := strings.Cut(strings.TrimPrefix(t.Key, "acct:"), "/")
	kind := "aggressive"
	if protected {
		kind = "protected"
	}
	_ = g.Append(leases.Line{V: 1, Op: leases.OpLease, ID: record.NewID(now), At: now.UTC(), Key: t.Key, Account: acct, Org: org,
		Folder: "~/" + filepath.Base(t.Dir), Label: t.Label, Kind: kind, SessionID: sid, Strategy: "move", MovedFrom: from, Parent: parent,
		Expect: exp.Expect, ExpectMin: exp.ExpectMin, ExpectEnd: exp.ExpectEnd})
	_, _ = g.Mark(sid, "launched", now)
	return parent
}

// MoveMemory is how long a session's moves are remembered for its old handles and its note's
// path of accounts.
const moveMemory = 7 * 24 * time.Hour

// movedSlots is peers.Env.PastSlots from the move log (read once per directory build).
func movedSlots(stateDir string) func(string) []string {
	st := mover.NewState(stateDir)
	return func(sid string) []string { return st.PastSlots(sid, time.Now().Add(-moveMemory)) }
}

// chainLabels is the labels of the session's earlier moves, oldest first (none when it never
// moved), for its note.
func (w *moveWorld) chainLabels(st mover.State, sid string, now time.Time) []string {
	var out []string
	for _, d := range st.Chain(sid, now.Add(-moveMemory)) {
		l := w.labelOfDir[d]
		if l == "" {
			l = "~/" + filepath.Base(d)
		}
		out = append(out, l)
	}
	return out
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

// peersTags are the sessions' peers tags and handles from `wy peers list --all`'s source, when
// peers are on (nil otherwise).
func peersTags() (map[string][]string, map[string]string) {
	pe, err := peersEnv()
	if err != nil || !pe.Enabled {
		return nil, nil
	}
	es, _, rf := peersListEntries(pe, true)
	if rf != nil {
		return nil, nil
	}
	tags, handles := map[string][]string{}, map[string]string{}
	for _, x := range es {
		tags[x.Session], handles[x.Session] = x.Tags, x.Handle
	}
	return tags, handles
}

// firstLeases sets each running lane's start to its session's first lease in the ledger: its
// real age, not its last move's lease (a moved session is not a new one; setup.md 8.6, Young
// sessions; live pre9: 54c4e1e8 "under 15 min old" on claude-6 12 minutes after its move).
func firstLeases(started map[string]time.Time, ledger []leases.Lease) {
	for _, l := range ledger {
		if t, ok := started[l.SessionID]; ok && l.SessionID != "" && !l.At.IsZero() && l.At.Before(t) {
			started[l.SessionID] = l.At
		}
	}
}

// laneExpect is each running lane's expected time left for the switch (setup.md 8.6, one
// rule): to its lease's expected end (expect_end; the picker writes one for every lane, a
// medium lane's without --expect), 0 once it overran (the switch then projects it to the
// horizons). A lane whose lease line has none (a move lease written before) takes its
// session's latest one in the ledger; a lease with neither, as before (its in-run --expect).
func laneExpect(lanes []picker.LiveLane, ledger []leases.Lease, now time.Time) map[string]time.Duration {
	ends := map[string]time.Time{}
	var at = map[string]time.Time{}
	for _, l := range ledger {
		if l.SessionID != "" && l.ExpectEnd != nil && !l.At.Before(at[l.SessionID]) {
			ends[l.SessionID], at[l.SessionID] = *l.ExpectEnd, l.At
		}
	}
	out := map[string]time.Duration{}
	for _, l := range lanes {
		if l.SID == "" {
			continue
		}
		end := l.ExpectEnd
		if end.IsZero() {
			end = ends[l.SID]
		}
		switch {
		case !end.IsZero():
			out[l.SID] = max(0, end.Sub(now))
		case l.Expect > 0:
			out[l.SID] = max(0, l.Expect-now.Sub(l.T0))
		}
	}
	return out
}
