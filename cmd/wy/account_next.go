package main

// `wy account next` (docs/spec/setup.md 8.5): recommends the account to start the next
// Claude Code session on (or K of them with --n). It reads, ranks and records a lease per
// pick in the ledger (internal/leases), mirrored to the hub through the outbox; it never
// switches a running session and never changes a login. --exec starts the command the
// caller names, with CLAUDE_CONFIG_DIR set for the pick: an explicit action of the caller.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/watchyard/internal/board"
	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/detach"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/leases"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/picker"
	"github.com/mraj602/watchyard/internal/record"
	"github.com/mraj602/watchyard/internal/version"
)

// Exit codes of setup.md 8.5: 4 when fewer sessions than asked for were placed, 3 when a
// --session-id is already leased or given twice.
const (
	exitNoAccount        = 4
	exitDuplicateSession = 3
)

// nextMaxN bounds --n.
const nextMaxN = 50

// nextFlushEvery debounces the background flush that sends the lease records (the hook's).
const nextFlushEvery = 2 * time.Second

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// nextHubTimeout bounds all hub reads of one run.
const nextHubTimeout = 5 * time.Second

// nextEnv is what `wy account next` reads; tests build it on temporary folders.
type nextEnv struct {
	home   string
	dirs   paths.Dirs
	getenv func(string) string
	now    func() time.Time
	stdout io.Writer
	stderr io.Writer
	// transport for the hub; nil means the one hub.dial asks for.
	transport http.RoundTripper
	// exec starts the command of --exec; nil means execWithEnv.
	exec func(path string, argv, env []string) error
	// lookPath finds the command of --exec; nil means exec.LookPath.
	lookPath func(string) (string, error)
	environ  func() []string
	// authRun runs `claude auth status --json`; nil means identity.ExecRunner.
	authRun identity.Runner
	// spawn starts `wy <args>` in the background; nil means a detached copy of this binary
	// (none with WY_NO_SPAWN=1).
	spawn func(args []string) error
}

// nextAuthFresh: a cached `claude auth status` younger than this is reused, so a burst of
// agents launching at once asks claude once per folder.
const nextAuthFresh = time.Minute

func nextUsage(w io.Writer) {
	fmt.Fprint(w, `usage: wy account next [--strategy lanes|smart|expiring|most-room|spread|fill|sticky] [--model <m>]
                       [--n <K>] [--session-id <uuid>]... [--urgent]
                       [--env | --json | --explain] [--no-reserve] [--no-hub]
       wy account next [flags] --exec [-- <command> [args...]]     (default command: claude)
--n, --session-id and --urgent are for the lanes strategy (the default).
Exit 0: every session asked for was placed. Exit 4: fewer were placed (none for one);
launch only the printed picks. Exit 3: a --session-id is already leased or given twice.
`)
}

func runAccountNext(args []string, stdout, stderr io.Writer) error {
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
	return accountNext(nextEnv{home: home, dirs: d, getenv: os.Getenv, now: time.Now, stdout: stdout, stderr: stderr}, args)
}

func accountNext(e nextEnv, args []string) error {
	var cmdArgs []string
	for i, a := range args {
		if a == "--" {
			args, cmdArgs = args[:i], args[i+1:]
			break
		}
	}
	fs := flag.NewFlagSet("account next", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() { nextUsage(e.stderr) }
	strategy := fs.String("strategy", "", "lanes, smart, expiring, most-room, spread, fill or sticky (default next.strategy)")
	model := fs.String("model", "", "the model the session will use, e.g. opus (recorded on the lease)")
	envOut := fs.Bool("env", false, "print only CLAUDE_CONFIG_DIR=<folder> (for env $(...) claude)")
	asJSON := fs.Bool("json", false, "every account ranked, as JSON")
	explain := fs.Bool("explain", false, "every account ranked, as a table")
	noReserve := fs.Bool("no-reserve", false, "a dry query: record no lease")
	noHub := fs.Bool("no-hub", false, "use only this machine's data")
	doExec := fs.Bool("exec", false, "start the command after -- (default claude) with the pick's CLAUDE_CONFIG_DIR")
	n := fs.Int("n", 0, "picks for this many sessions, 1 to 50 (default: the number of --session-id, else 1)")
	urgent := fs.Bool("urgent", false, "lanes: may pass an aggressive account's burst cap when nothing else is free")
	var sids []string
	fs.Func("session-id", "the session id (a UUID) each session will get, repeatable, in pick order", func(v string) error {
		sids = append(sids, v)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		nextUsage(e.stderr)
		return errUsage
	}
	bad := func(msg string) error {
		fmt.Fprintln(e.stderr, "wy account next: "+msg)
		return errUsage
	}
	outs := 0
	for _, b := range []bool{*envOut, *asJSON, *explain, *doExec} {
		if b {
			outs++
		}
	}
	if outs > 1 {
		return bad("--env, --json, --explain and --exec do not mix")
	}
	if len(cmdArgs) > 0 && !*doExec {
		return bad("a command after -- needs --exec")
	}
	if *strategy != "" && !config.ValidNextStrategy(*strategy) {
		return bad(fmt.Sprintf("unknown strategy %q (want %s)", *strategy, strings.Join(config.NextStrategies, ", ")))
	}
	k := *n
	if k == 0 {
		k = max(1, len(sids))
	}
	if k < 1 || k > nextMaxN {
		return bad(fmt.Sprintf("--n must be 1 to %d", nextMaxN))
	}
	if len(sids) > 0 && len(sids) != k {
		return bad(fmt.Sprintf("--n %d needs exactly %d --session-id values, got %d", k, k, len(sids)))
	}
	for _, sid := range sids {
		if !uuidRe.MatchString(sid) {
			return bad(fmt.Sprintf("--session-id %q is not a UUID", sid))
		}
	}
	if k > 1 && (*envOut || *doExec) {
		return bad("--env and --exec start one session: use them without --n or with --n 1")
	}

	cfg := config.Defaults()
	var warns []string
	if cf, err := config.Load(e.dirs.Config.Path); err == nil {
		c, terr := cf.Typed()
		cfg = c
		if terr != nil {
			warns = append(warns, terr.Error())
		}
	} else {
		warns = append(warns, err.Error())
	}
	settings := picker.SettingsFrom(cfg.Next)
	strat := *strategy
	if strat == "" {
		strat = settings.Strategy
	}
	lanes := strat == config.NextStrategyLanes
	if !lanes && (k > 1 || len(sids) > 0 || *urgent) {
		return bad("--n above 1, --session-id and --urgent are for the lanes strategy (strategy is " + strat + ")")
	}
	now := e.now()

	slots, err := nextSlots(e, cfg)
	if err != nil {
		return err
	}
	m, err := identity.LoadOrCreateMachine(e.dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return err
	}
	src := picker.Sources{Machine: m.ID}
	data, hubErr := "local", ""
	if !*noHub {
		hd, herr := nextHub(e, cfg, slots, m.ID, now)
		switch {
		case herr != nil:
			hubErr = herr.Error()
		case hd != nil:
			src.Hub, data = hd, "hub"
		}
	}
	ob, lerr := picker.ReadOutbox(filepath.Join(e.dirs.State.Path, "outbox"), now.Add(-7*24*time.Hour), lanes)
	if lerr != nil {
		warns = append(warns, "outbox: "+lerr.Error())
	}
	src.Local = ob.Samples
	in := picker.Input{Now: now, Strategy: strat, Model: *model, Settings: settings, Candidates: picker.Candidates(slots, src),
		Outbox: ob, N: k, SIDs: sids, Urgent: *urgent}

	ledger := leases.Ledger{Dir: filepath.Join(e.dirs.State.Path, "next")}
	var res picker.Result
	var lines []leases.Line
	skipped := 0
	if *noReserve {
		ls, sk, err := ledger.Read(now)
		if err != nil {
			warns = append(warns, "lease ledger: "+err.Error())
		}
		in.Leases, skipped = ls, sk
		res, err = picker.Decide(in)
		if err != nil {
			return nextDuplicate(e, err)
		}
	} else {
		unlock, err := ledger.Lock()
		if err != nil {
			return err
		}
		ls, sk, err := ledger.Read(now)
		if err != nil {
			unlock()
			return fmt.Errorf("read the lease ledger: %w", err)
		}
		in.Leases, skipped = ls, sk
		res, err = picker.Decide(in)
		if err != nil {
			unlock()
			return nextDuplicate(e, err)
		}
		lines = leaseLines(res, *model, now)
		err = ledger.Append(lines...)
		if err == nil {
			err = ledger.Prune(now)
		}
		unlock()
		if err != nil {
			return fmt.Errorf("record the lease: %w", err)
		}
		if w := mirrorLeases(e, cfg, m.ID, res, lines); w != "" {
			warns = append(warns, w)
		}
	}
	if skipped > 0 {
		warns = append(warns, fmt.Sprintf("lease ledger: %d unreadable lines skipped", skipped))
	}
	res.Warnings = append(warns, res.Warnings...)

	switch {
	case *asJSON:
		if err := picker.WriteJSON(e.stdout, picker.NewDoc(res, data, hubErr, len(lines) > 0, skipped)); err != nil {
			return err
		}
	case *explain:
		if err := picker.Explain(e.stdout, res, data, hubErr); err != nil {
			return err
		}
	case len(res.Picks) == 0:
		// below
	case *envOut:
		fmt.Fprintln(e.stdout, picker.EnvArgs(res.Picks[0].Ranked))
	case *doExec:
		p := res.Picks[0]
		if p.Urgent {
			fmt.Fprintln(e.stderr, "wy account next: urgent: "+p.Label+" passed its burst cap")
		}
		return nextExec(e, p.Ranked, withSessionID(cmdArgs, p.SessionID))
	default:
		for _, p := range res.Picks {
			line := picker.Line(p.Ranked)
			if data == "local" && hubErr != "" {
				line += "  [hub not read: " + hubErr + "]"
			}
			fmt.Fprintln(e.stdout, line)
		}
	}
	for _, p := range res.Picks {
		if p.Urgent && !*doExec {
			fmt.Fprintln(e.stderr, "wy account next: urgent: "+p.Label+" passed its burst cap")
		}
	}
	if len(res.Picks) == 0 || res.Waiting > 0 {
		if !*asJSON && !*explain {
			fmt.Fprintln(e.stderr, picker.NoneLine(res))
		}
		return exitWith(exitNoAccount, nil)
	}
	return nil
}

// nextDuplicate reports a --session-id that is already leased or given twice (exit 3).
func nextDuplicate(e nextEnv, err error) error {
	if !errors.Is(err, picker.ErrDuplicateSession) {
		return err
	}
	fmt.Fprintln(e.stderr, "wy account next: "+err.Error()+"; nothing was recorded (make new session ids and ask again)")
	return exitWith(exitDuplicateSession, nil)
}

// leaseLines are the ledger lines of the picks: one lease each, sharing a batch id.
func leaseLines(res picker.Result, model string, now time.Time) []leases.Line {
	if len(res.Picks) == 0 {
		return nil
	}
	batch := record.NewID(now)
	var out []leases.Line
	for _, p := range res.Picks {
		c5, cf := p.C5, p.CF
		out = append(out, leases.Line{V: 1, Op: leases.OpLease, ID: record.NewID(now), At: now.UTC(), Key: p.Key(),
			Account: p.Account(), Org: p.Org(), Folder: p.Folder, Label: p.Label, Kind: p.LaneKind,
			SessionID: p.SessionID, C5: &c5, CF: &cf, Model: model, Strategy: res.Strategy, Urgent: p.Urgent, Batch: batch})
	}
	return out
}

// mirrorLeases appends an account_lease record per lease to the outbox and starts a
// background flush; it never waits for the hub. It returns a warning, or "".
func mirrorLeases(e nextEnv, cfg config.Config, machine string, res picker.Result, lines []leases.Line) string {
	if len(lines) == 0 {
		return ""
	}
	ob := outbox.New(filepath.Join(e.dirs.State.Path, "outbox"))
	ob.NoFsync = !cfg.Outbox.Fsync
	wrote := false
	var problems []string
	for _, l := range lines {
		if l.Account == "" || l.Org == "" {
			continue // a folder whose login is unknown: the hub could not place it
		}
		r, err := record.NewAccountLease(record.Meta{Machine: machine, Client: "wy/" + version.Version, Now: l.At,
			Seat: &record.Seat{Account: l.Account, Org: l.Org, Src: "claude_json"}},
			record.AccountLeasePayload{LeaseID: l.ID, SessionID: l.SessionID, Kind: l.Kind, C5: *l.C5, CF: *l.CF,
				Strategy: l.Strategy, Urgent: l.Urgent, Model: l.Model, Folder: l.Folder, Batch: l.Batch})
		if err == nil {
			_, err = ob.Append(r)
		}
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		wrote = true
	}
	if wrote && cfg.Hub.URL != "" && detach.Debounce(filepath.Join(ob.Dir, "flush.stamp"), nextFlushEvery, time.Now()) {
		spawn := e.spawn
		if spawn == nil {
			spawn = func(args []string) error {
				if e.getenv("WY_NO_SPAWN") == "1" {
					return nil
				}
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				return detach.Start(exe, args, nil)
			}
		}
		if err := spawn([]string{"flush", "--quiet"}); err != nil {
			problems = append(problems, "background flush: "+err.Error())
		}
	}
	if len(problems) > 0 {
		return "lease not sent to the hub: " + strings.Join(problems, "; ")
	}
	return ""
}

// withSessionID adds --session-id to a claude command that names no session of its own, so
// the session starts with the leased id and the hook marks the lease launched.
func withSessionID(argv []string, sid string) []string {
	if sid == "" {
		return argv
	}
	if len(argv) == 0 {
		argv = []string{"claude"}
	}
	if !strings.EqualFold(strings.TrimSuffix(filepath.Base(argv[0]), ".exe"), "claude") {
		return argv
	}
	for _, a := range argv[1:] {
		switch {
		case a == "--session-id", strings.HasPrefix(a, "--session-id="), a == "--resume", strings.HasPrefix(a, "--resume="),
			a == "-r", a == "--continue", a == "-c":
			return argv
		}
	}
	out := append([]string{}, argv[0], "--session-id", sid)
	return append(out, argv[1:]...)
}

// nextSlots lists this machine's folders and their logins, decided by identity.Login.LoggedIn
// (the rule `wy doctor` uses): the state file's oauthAccount, overruled by
// `claude auth status`, which is asked again (in parallel, AuthTimeout each) when its cached
// answer is older than nextAuthFresh and claude is on PATH, as `wy account list` does.
func nextSlots(e nextEnv, cfg config.Config) ([]picker.Slot, error) {
	loc := identity.Locator{Home: e.home, FS: identity.OSFS{}}
	// Every folder in its own view: CLAUDE_CONFIG_DIR of the calling session is ignored,
	// so ~/.claude is always its default view.
	noCCD := func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return ""
		}
		return e.getenv(k)
	}
	found, err := loc.Discover(cfg.Slots.Extra, noCCD)
	if err != nil {
		return nil, err
	}
	st := &identity.Store{StateDir: e.dirs.State.Path, FS: identity.OSFS{}, Now: e.now}
	look, environ := e.lookPath, e.environ
	if look == nil {
		look = exec.LookPath
	}
	if environ == nil {
		environ = os.Environ
	}
	if _, err := look("claude"); err == nil {
		var wg sync.WaitGroup
		for _, s := range found {
			if l, err := st.Login(s); err == nil && !l.AuthStatusAt.IsZero() && e.now().Sub(l.AuthStatusAt) < nextAuthFresh {
				continue
			}
			wg.Add(1)
			go func(s identity.Slot) {
				defer wg.Done()
				// A failure keeps the cached answer: the state file decides alone.
				_, _ = st.RefreshAuthStatus(context.Background(), s, environ(), e.authRun)
			}(s)
		}
		wg.Wait()
	}
	var out []picker.Slot
	for _, s := range found {
		label := cfg.Slots.Labels[s.Name]
		if label == "" {
			label = strings.TrimLeft(filepath.Base(s.Dir), ".")
		}
		ps := picker.Slot{Folder: s.Name, Dir: s.Dir, Default: s.Default, Label: label}
		if l, err := st.Login(s); err == nil && l.LoggedIn() {
			ps.LoggedIn, ps.Account, ps.Org, ps.Email = true, l.Seat.Account, l.Seat.Org, l.Seat.Email
		}
		out = append(out, ps)
	}
	return out, nil
}

// nextHub reads the hub (setup.md 8.5): GET /v1/usage with this machine's token, or, when
// there is no machine token or the hub is too old for /v1/usage, /v1/accounts with the board
// token. (nil, nil): no hub is set up.
func nextHub(e nextEnv, cfg config.Config, slots []picker.Slot, machine string, now time.Time) (*picker.HubData, error) {
	if cfg.Hub.URL == "" {
		return nil, nil
	}
	if _, _, err := client.CheckURL(cfg.Hub.URL, cfg.Hub.InsecureLAN); err != nil {
		return nil, err
	}
	machineTok, mErr := client.ReadToken(e.dirs.State.Path)
	boardTok, bErr := board.LoadToken(e.dirs.State.Path, cfg.Hub.URL)
	if mErr != nil && bErr != nil {
		return nil, errors.New("no hub token on this machine; run wy connect")
	}
	tr := e.transport
	if tr == nil {
		t, err := client.DialTransport(cfg.Hub.Dial)
		if err != nil {
			return nil, fmt.Errorf("hub.dial: %w", err)
		}
		tr = t
	}
	ctx, cancel := context.WithTimeout(context.Background(), nextHubTimeout)
	defer cancel()
	hc := picker.HubClient{URL: cfg.Hub.URL, HTTP: &http.Client{Transport: tr, Timeout: nextHubTimeout}}
	timeout := func(err error) error {
		if ctx.Err() != nil {
			return errors.New("the hub did not answer in time")
		}
		return err
	}
	var usageErr error
	if mErr == nil {
		hc.Token = machineTok
		d, err := hc.FetchUsage(ctx, now)
		if err == nil {
			return &d, nil
		}
		if bErr != nil || !errors.Is(err, picker.ErrNoUsageEndpoint) {
			return nil, timeout(err)
		}
		usageErr = err
	}
	hc.Token = boardTok.Token
	d, err := hc.Fetch(ctx, now, picker.WantSeat(slots, machine))
	if err != nil {
		if usageErr != nil {
			return nil, usageErr
		}
		return nil, timeout(err)
	}
	return &d, nil
}

// nextExec starts the command (default claude) with CLAUDE_CONFIG_DIR set to the pick, or
// unset for the default view of ~/.claude.
func nextExec(e nextEnv, pick picker.Ranked, argv []string) error {
	if len(argv) == 0 {
		argv = []string{"claude"}
	}
	look := e.lookPath
	if look == nil {
		look = exec.LookPath
	}
	path, err := look(argv[0])
	if err != nil {
		return fmt.Errorf("cannot find %s: %w", argv[0], err)
	}
	environ := e.environ
	if environ == nil {
		environ = os.Environ
	}
	var env []string
	for _, kv := range environ() {
		if !strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			env = append(env, kv)
		}
	}
	if !pick.Default {
		env = append(env, "CLAUDE_CONFIG_DIR="+pick.Dir)
	}
	fmt.Fprintln(e.stderr, "wy account next: "+picker.Line(pick))
	run := e.exec
	if run == nil {
		run = execWithEnv
	}
	return run(path, argv, env)
}
