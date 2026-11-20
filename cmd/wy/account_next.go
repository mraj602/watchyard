package main

// `wy account next` (docs/spec/setup.md 8.5): recommends the account to start the next
// Claude Code session on. It reads, ranks and records a short reservation; it never
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
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/board"
	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/picker"
)

// exitNoAccount: no account can take a new session now (setup.md 8.5).
const exitNoAccount = 4

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
}

func nextUsage(w io.Writer) {
	fmt.Fprint(w, `usage: wy account next [--strategy smart|expiring|most-room|spread|fill|sticky] [--model <m>]
                       [--env | --json | --explain] [--no-reserve] [--no-hub]
       wy account next [flags] --exec [-- <command> [args...]]     (default command: claude)
Exit 0: an account was picked. Exit 4: no account can take a new session now.
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
	strategy := fs.String("strategy", "", "smart, expiring, most-room, spread, fill or sticky (default next.strategy)")
	model := fs.String("model", "", "the model the session will use, e.g. opus (adds that model's weekly limit)")
	envOut := fs.Bool("env", false, "print only CLAUDE_CONFIG_DIR=<folder> (for env $(...) claude)")
	asJSON := fs.Bool("json", false, "every account ranked, as JSON")
	explain := fs.Bool("explain", false, "every account ranked, as a table")
	noReserve := fs.Bool("no-reserve", false, "a dry query: record no reservation and no pick")
	noHub := fs.Bool("no-hub", false, "use only this machine's data")
	doExec := fs.Bool("exec", false, "start the command after -- (default claude) with the pick's CLAUDE_CONFIG_DIR")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		nextUsage(e.stderr)
		return errUsage
	}
	outs := 0
	for _, b := range []bool{*envOut, *asJSON, *explain, *doExec} {
		if b {
			outs++
		}
	}
	if outs > 1 {
		fmt.Fprintln(e.stderr, "wy account next: --env, --json, --explain and --exec do not mix")
		return errUsage
	}
	if len(cmdArgs) > 0 && !*doExec {
		fmt.Fprintln(e.stderr, "wy account next: a command after -- needs --exec")
		return errUsage
	}
	if *strategy != "" && !config.ValidNextStrategy(*strategy) {
		fmt.Fprintf(e.stderr, "wy account next: unknown strategy %q (want %s)\n", *strategy, strings.Join(config.NextStrategies, ", "))
		return errUsage
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
	local, lerr := picker.ReadLocal(filepath.Join(e.dirs.State.Path, "outbox"), now.Add(-7*24*time.Hour))
	if lerr != nil {
		warns = append(warns, "outbox: "+lerr.Error())
	}
	src.Local = local
	in := picker.Input{Now: now, Strategy: *strategy, Model: *model, Settings: settings, Candidates: picker.Candidates(slots, src)}

	store := picker.Store{Dir: filepath.Join(e.dirs.State.Path, "next")}
	var res picker.Result
	rank := func(l picker.Ledger) {
		in.Pending, in.LastPicked, in.LastKey = l.Pending(now), l.Last, l.LastKey
		res = picker.Rank(in)
	}
	reserved := false
	if *noReserve {
		l, err := store.Read(now)
		if err != nil {
			warns = append(warns, "pick log: "+err.Error())
		}
		rank(l)
	} else {
		err := store.Update(now, func(l *picker.Ledger) error {
			rank(*l)
			if res.Pick != nil {
				l.Record(res.Pick.Key(), res.Pick.Folder, now, settings.ReserveTTL)
				reserved = true
			}
			return nil
		})
		if err != nil { // the pick still stands; it is just not reserved
			warns = append(warns, "reservation not recorded: "+err.Error())
			if !reserved {
				l, _ := store.Read(now)
				rank(l)
			}
		}
	}
	res.Warnings = append(warns, res.Warnings...)

	switch {
	case *asJSON:
		if err := picker.WriteJSON(e.stdout, picker.NewDoc(res, data, hubErr, reserved)); err != nil {
			return err
		}
	case *explain:
		if err := picker.Explain(e.stdout, res, data, hubErr); err != nil {
			return err
		}
	case res.Pick == nil:
		// below
	case *envOut:
		fmt.Fprintln(e.stdout, picker.EnvArgs(*res.Pick))
	case *doExec:
		return nextExec(e, *res.Pick, cmdArgs)
	default:
		line := picker.Line(*res.Pick)
		if data == "local" && hubErr != "" {
			line += "  [hub not read: " + hubErr + "]"
		}
		fmt.Fprintln(e.stdout, line)
	}
	if res.Pick == nil {
		if !*asJSON && !*explain {
			fmt.Fprintln(e.stderr, picker.NoneLine(res))
		}
		return exitWith(exitNoAccount, nil)
	}
	return nil
}

// nextSlots lists this machine's folders and their logins without running claude: the
// state file's oauthAccount through the identity cache, as `wy doctor` reads it.
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
	var out []picker.Slot
	for _, s := range found {
		label := cfg.Slots.Labels[s.Name]
		if label == "" {
			label = strings.TrimLeft(filepath.Base(s.Dir), ".")
		}
		ps := picker.Slot{Folder: s.Name, Dir: s.Dir, Default: s.Default, Label: label}
		if l, err := st.Login(s); err == nil && l.HasSeat {
			ps.LoggedIn, ps.Account, ps.Org, ps.Email = true, l.Seat.Account, l.Seat.Org, l.Seat.Email
		}
		out = append(out, ps)
	}
	return out, nil
}

// nextHub reads the hub with the board token. (nil, nil): no hub is set up.
func nextHub(e nextEnv, cfg config.Config, slots []picker.Slot, machine string, now time.Time) (*picker.HubData, error) {
	if cfg.Hub.URL == "" {
		return nil, nil
	}
	tok, err := board.LoadToken(e.dirs.State.Path, cfg.Hub.URL)
	if err != nil {
		if errors.Is(err, board.ErrNoToken) {
			return nil, errors.New("no read token for the hub; run wy board connect")
		}
		return nil, err
	}
	if _, _, err := client.CheckURL(cfg.Hub.URL, cfg.Hub.InsecureLAN); err != nil {
		return nil, err
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
	hc := picker.HubClient{URL: cfg.Hub.URL, Token: tok.Token, HTTP: &http.Client{Transport: tr, Timeout: nextHubTimeout}}
	d, err := hc.Fetch(ctx, now, picker.WantSeat(slots, machine))
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("the hub did not answer in time")
		}
		return nil, err
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
