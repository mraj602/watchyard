package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/desktop"
	"github.com/mraj602/watchyard/internal/directpush"
	"github.com/mraj602/watchyard/internal/hub/cli"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/machineadd"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/sshx"
	"github.com/mraj602/watchyard/internal/update"
	"github.com/mraj602/watchyard/internal/version"
)

const machineUsage = `usage: wy machine [show] [--json]
       wy machine rename <name>
       wy machine add <[user@]host> [--name N] [--reach auto|direct|forward|pull] [--hub-url U]
                      [--insecure-lan] [--port N] [--forward-socket] [--pull-from cidrs]
                      [--hub-admin local|<command prefix>] [--code-stdin]
                      [--version V | --binary FILE] [--accounts L] [--share S] [--login ask|never]
                      [--direct-push] [--ssh-option K=V]... [--yes] [--dry-run] [--json]
       wy machine list [--json]
       wy machine remove <name> [--revoke] [--keep-remote] [--json]
       wy machine repoint <url> [--machines all|a,b] [--json]
       wy machine probe --json [--hub-url U] [--ports A-B]          (runs on the remote)
       wy machine authorize-pull --key-stdin --target ID [--from cidrs] [--no-restrict]
       wy machine authorize-pull --remove --target ID              (runs on the remote)
`

// machineEnv is what the machine commands read from this machine; tests replace parts.
type machineEnv struct {
	dirs      paths.Dirs
	home      string
	getenv    func(string) string
	stdin     io.Reader
	stdinFile *os.File // set when stdin and stderr are terminals
	exe       string
	// hub runs `wy hub <args>` in this process (the `local` admin channel).
	hub func(ctx context.Context, args []string, stdin []byte) (stdout, stderr []byte, exit int)
}

// machineEnvHook lets tests adjust the environment.
var machineEnvHook func(*machineEnv)

func osMachineEnv() (*machineEnv, error) {
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	e := &machineEnv{dirs: d, home: home, getenv: os.Getenv, stdin: os.Stdin, exe: exe, hub: localHub}
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd())) {
		e.stdinFile = os.Stdin
	}
	if machineEnvHook != nil {
		machineEnvHook(e)
	}
	return e, nil
}

// localHub is the in-process admin channel: `wy hub …` against <data>/hub.db, output
// captured. Exit codes follow `wy hub`: 2 usage, 3 not implemented, 1 any other failure.
func localHub(ctx context.Context, args []string, stdin []byte) ([]byte, []byte, int) {
	var out, errb bytes.Buffer
	var err error
	switch {
	case len(args) > 0 && args[0] == "pull":
		err = cli.PullIn(bytes.NewReader(stdin), args[1:], &out, &errb)
	case len(args) > 0 && args[0] == "keys":
		err = cli.KeysIn(bytes.NewReader(stdin), args[1:], &out, &errb)
	default:
		err = cli.Run(args, &out, &errb)
	}
	switch {
	case err == nil:
		return out.Bytes(), errb.Bytes(), 0
	case errors.Is(err, cli.ErrUsage):
		return out.Bytes(), errb.Bytes(), 2
	case errors.Is(err, cli.ErrNotImplemented):
		fmt.Fprintf(&errb, "%v\n", err)
		return out.Bytes(), errb.Bytes(), 3
	}
	fmt.Fprintf(&errb, "%v\n", err)
	return out.Bytes(), errb.Bytes(), 1
}

// runMachine implements `wy machine …` (machines.md 4.5). A bare `wy machine` or `show`
// prints this machine's id and name, creating machine.json on first use.
func runMachine(args []string, stdout, stderr io.Writer) error {
	sub := "show"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if sub == "help" {
		fmt.Fprint(stdout, machineUsage)
		return nil
	}
	e, err := osMachineEnv()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch sub {
	case "show":
		return machineShow(e, args, stdout, stderr)
	case "rename":
		return machineRename(e, args, stdout, stderr)
	case "add":
		return machineAdd(ctx, e, args, stdout, stderr)
	case "list":
		return machineList(ctx, e, args, stdout, stderr)
	case "remove":
		return machineRemove(ctx, e, args, stdout, stderr)
	case "repoint":
		return machineRepoint(ctx, e, args, stdout, stderr)
	case "probe":
		return machineProbe(ctx, e, args, stdout, stderr)
	case "authorize-pull":
		return machineAuthorizePull(e, args, stdout, stderr)
	}
	fmt.Fprintf(stderr, "wy machine: unknown command %q\n\n%s", sub, machineUsage)
	return errUsage
}

func machineFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("machine "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, machineUsage) }
	return fs
}

func machineShow(e *machineEnv, args []string, stdout, stderr io.Writer) error {
	fs := machineFlags("show", stderr)
	asJSON := fs.Bool("json", false, "print {id, name, created} as JSON")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "wy machine: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	m, err := identity.LoadOrCreateMachine(e.dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(m)
	}
	fmt.Fprintf(stdout, "%s  %s\n", m.ID, m.Name)
	return nil
}

// ---- rename -------------------------------------------------------------------------------

func machineRename(e *machineEnv, args []string, stdout, stderr io.Writer) error {
	fs := machineFlags("rename", stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return errUsage
	}
	if _, err := identity.CleanMachineName(pos[0]); err != nil {
		return exitWith(exitUsage, err)
	}
	old, m, err := identity.RenameMachine(e.dirs.State.Path, pos[0], identity.MachineOptions{})
	if err != nil {
		return err
	}
	queued := 0
	if old.Name != m.Name {
		// identity.md 2: a slot_info per folder so the hub follows (connect_probe.go builds them).
		deps := connectDeps{dirs: e.dirs}
		deps.timer.Home, deps.timer.Getenv = e.home, e.getenv
		recs, rerr := probeRecords(deps)
		ob := outbox.New(filepath.Join(e.dirs.State.Path, "outbox"))
		for _, r := range recs {
			if _, err := ob.AppendLine(r); err == nil {
				queued++
			}
		}
		if rerr != nil {
			fmt.Fprintf(stderr, "wy machine rename: slot_info not queued: %v\n", rerr)
		}
		// machines.md 6.3: a rename sends machine_info too.
		if sent, err := sendMachineInfo(e.dirs, e.home, true); err != nil {
			fmt.Fprintf(stderr, "wy machine rename: machine_info not queued: %v\n", err)
		} else if sent {
			queued++
		}
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(map[string]any{"id": m.ID, "old_name": old.Name, "name": m.Name, "queued": queued})
	}
	if old.Name == m.Name {
		fmt.Fprintf(stdout, "%s is already named %s.\n", m.ID, m.Name)
		return nil
	}
	fmt.Fprintf(stdout, "Renamed %s from %s to %s. The hub follows with the next flush (wy flush sends it now).\n", m.ID, old.Name, m.Name)
	return nil
}

// ---- add ----------------------------------------------------------------------------------

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func loadMachineConfig(e *machineEnv) (*config.File, config.Config) {
	f, err := config.Load(e.dirs.Config.Path)
	if err != nil {
		return nil, config.Defaults()
	}
	cfg, err := f.Typed()
	if err != nil {
		return f, config.Defaults()
	}
	return f, cfg
}

// adminChannel resolves --hub-admin (4.4): the flag, else machines.hub_admin, else local
// when <data>/hub.db exists here, else none (a pasted code). saveable says the value came
// from the flag and should be stored after it worked.
func adminChannel(e *machineEnv, flagValue string, cfg config.Config) (machineadd.Admin, string, error) {
	v := flagValue
	if v == "" {
		v = cfg.Machines.HubAdmin
	}
	// The local hub.db is the channel only while the hub is here: after a move it is stale.
	if v == "" && desktop.HubHere(cfg.Hub.URL) {
		if _, err := os.Stat(filepath.Join(e.dirs.Data.Path, "hub.db")); err == nil {
			v = "local"
		}
	}
	switch v {
	case "":
		return nil, "", nil
	case "local":
		return machineadd.LocalAdmin{Run: e.hub}, v, nil
	}
	p, err := machineadd.NewPrefixAdmin(v)
	if err != nil {
		return nil, "", err
	}
	return p, v, nil
}

func sshRunner(e *machineEnv, opts []string) (*sshx.Runner, error) {
	return sshx.FromEnv(e.getenv, opts...)
}

func machineAdd(ctx context.Context, e *machineEnv, args []string, stdout, stderr io.Writer) error {
	fs := machineFlags("add", stderr)
	var o machineadd.Options
	var sshOpts multiFlag
	fs.StringVar(&o.Name, "name", "", "machine name (default the remote's short hostname)")
	fs.StringVar(&o.Reach, "reach", "auto", "auto, direct, forward or pull")
	fs.StringVar(&o.HubURL, "hub-url", "", "the hub address as the remote will use it")
	fs.BoolVar(&o.InsecureLAN, "insecure-lan", false, "allow plain http to a LAN hub on the remote")
	fs.IntVar(&o.Port, "port", 0, "forward port on the remote (47830-47849)")
	fs.BoolVar(&o.ForwardSocket, "forward-socket", false, "forward to a Unix socket instead of a port")
	fs.StringVar(&o.PullFrom, "pull-from", "", "from= addresses for the hub's key (pull)")
	hubAdmin := fs.String("hub-admin", "", "local, or a command prefix that runs wy on the hub host (\"ssh hub-host wy\")")
	fs.BoolVar(&o.CodeStdin, "code-stdin", false, "read a machine pairing code from stdin")
	fs.StringVar(&o.Version, "version", "", "release to install (default this wy's version)")
	fs.StringVar(&o.Binary, "binary", "", "install this file instead of a release")
	fs.StringVar(&o.Accounts, "accounts", "", "passed to the remote's wy setup")
	fs.StringVar(&o.Share, "share", "", "passed to the remote's wy setup")
	fs.StringVar(&o.Login, "login", "", "ask or never")
	fs.BoolVar(&o.DirectPush, "direct-push", false, "also turn direct push on there with this machine's settings")
	fs.Var(&sshOpts, "ssh-option", "extra ssh option K=V (repeatable)")
	fs.BoolVar(&o.Yes, "yes", false, "do not ask")
	fs.BoolVar(&o.DryRun, "dry-run", false, "print the steps and change nothing")
	asJSON := fs.Bool("json", false, "print one JSON object per step and a summary")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return errUsage
	}
	o.Host = pos[0]
	if o.Login != "" && o.Login != "ask" && o.Login != "never" {
		return exitWith(exitUsage, errors.New("--login must be ask or never"))
	}
	if o.Binary != "" {
		if abs, err := filepath.Abs(o.Binary); err == nil {
			o.Binary = abs
		}
	}
	runner, err := sshRunner(e, sshOpts)
	if err != nil {
		return exitWith(exitUsage, err)
	}
	if o.CodeStdin && !o.DryRun {
		line, _ := bufio.NewReader(e.stdin).ReadString('\n')
		o.Code = strings.TrimSpace(line)
		if o.Code == "" {
			return exitWith(exitUsage, errors.New("--code-stdin: no code on stdin"))
		}
	}
	file, cfg := loadMachineConfig(e)
	admin, adminValue, err := adminChannel(e, *hubAdmin, cfg)
	if err != nil {
		return exitWith(exitUsage, err)
	}
	interactive := e.stdinFile != nil && !*asJSON
	env := &machineadd.Env{
		SSH: runner, Admin: admin, StateDir: e.dirs.State.Path, Home: e.home, OperatorHub: cfg.Hub.URL,
		HubLocal: adminValue == "local", Source: update.Source{BaseURL: firstNonEmpty(e.getenv("WY_INSTALL_BASE_URL"), cfg.Update.BaseURL)},
		SelfExe: e.exe, SelfVersion: version.Version, Interactive: interactive,
		Command: "wy machine add " + strings.Join(args, " "),
		Ask:     func(q string, def bool) bool { return askYesNo(e, stderr, q, def) },
		Choose: func(q string, options []string) string {
			fmt.Fprintf(stderr, "%s [%s] ", q, strings.Join(options, "/"))
			line, _ := bufio.NewReader(e.stdin).ReadString('\n')
			for _, op := range options {
				if strings.TrimSpace(line) == op {
					return op
				}
			}
			return options[0]
		},
		ReadSecret: func(prompt string) (string, error) {
			fmt.Fprint(stderr, prompt)
			if e.stdinFile == nil {
				return "", errors.New("not a terminal")
			}
			b, err := term.ReadPassword(int(e.stdinFile.Fd()))
			fmt.Fprintln(stderr)
			return string(b), err
		},
		DirectPush: func() (string, bool, []byte, error) {
			s, err := directpush.LoadSecret(e.dirs.State.Path)
			if err != nil || !cfg.Push.Direct.Enabled {
				return "", false, nil, errors.New("direct push is not on here (wy direct-push on first)")
			}
			b, err := json.Marshal(s)
			return cfg.Push.Direct.Server, cfg.Push.Direct.Private, b, err
		},
	}
	machineAddKeys(env, e.dirs.State.Path, interactive, stderr)
	enc := json.NewEncoder(stdout)
	env.Events = func(ev machineadd.Event) {
		if *asJSON {
			_ = enc.Encode(ev)
			return
		}
		mark := map[string]string{"ok": "ok  ", "skip": "--  ", "warn": "!!  ", "fail": "xx  ", "plan": "->  "}[ev.Status]
		fmt.Fprintf(stdout, "%s%s\n", mark, ev.Text)
	}
	sum, err := machineadd.Add(ctx, o, env)
	if *asJSON {
		out := map[string]any{"summary": sum}
		if err != nil {
			out["error"] = err.Error()
		}
		_ = enc.Encode(out)
	}
	if err == nil && *hubAdmin != "" && *hubAdmin != cfg.Machines.HubAdmin && file != nil && !o.DryRun {
		if serr := file.Set("machines.hub_admin", []string{*hubAdmin}); serr == nil {
			_ = file.Save()
		}
	}
	if err != nil {
		var ee *machineadd.ExitError
		if errors.As(err, &ee) {
			if *asJSON {
				return exitWith(ee.Code, nil)
			}
			return exitWith(ee.Code, ee.Err)
		}
		return err
	}
	if !*asJSON && len(sum.Logins) > 0 {
		fmt.Fprintln(stdout, "Still to do:")
		for _, l := range sum.Logins {
			fmt.Fprintln(stdout, "  "+l)
		}
	}
	return nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func askYesNo(e *machineEnv, stderr io.Writer, q string, def bool) bool {
	hint := "[Y/n]"
	if !def {
		hint = "[y/N]"
	}
	fmt.Fprintf(stderr, "%s %s ", q, hint)
	line, _ := bufio.NewReader(e.stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	}
	return def
}

// ---- list, remove, repoint ------------------------------------------------------------------

func manageEnv(e *machineEnv, stderr io.Writer, asJSON bool, stdout io.Writer, withAdmin bool) (*machineadd.ManageEnv, error) {
	runner, err := sshRunner(e, nil)
	if err != nil {
		return nil, exitWith(exitUsage, err)
	}
	// remove, repoint and the fleet run unattended: never wait on a prompt or a dead host.
	runner.Batch, runner.AutoDefaults = true, true
	me := &machineadd.ManageEnv{SSH: runner, StateDir: e.dirs.State.Path, Home: e.home}
	if withAdmin {
		_, cfg := loadMachineConfig(e)
		admin, _, err := adminChannel(e, "", cfg)
		if err != nil {
			return nil, exitWith(exitUsage, err)
		}
		me.Admin = admin
	}
	if !asJSON {
		me.Events = func(ev machineadd.Event) {
			if ev.Status == "warn" {
				fmt.Fprintf(stderr, "warning: %s\n", ev.Text)
				return
			}
			if ev.Step != "repoint" {
				fmt.Fprintln(stdout, ev.Text)
			}
		}
	}
	return me, nil
}

func manageResult(err error) error {
	var ee *machineadd.ExitError
	if errors.As(err, &ee) {
		return exitWith(ee.Code, ee.Err)
	}
	return err
}

func machineList(ctx context.Context, e *machineEnv, args []string, stdout, stderr io.Writer) error {
	fs := machineFlags("list", stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	hubAdmin := fs.String("hub-admin", "", "admin channel for the hub view")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "wy machine list: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	me, err := manageEnv(e, stderr, *asJSON, stdout, *hubAdmin == "")
	if err != nil {
		return err
	}
	if *hubAdmin != "" {
		if me.Admin, _, err = adminChannel(e, *hubAdmin, config.Defaults()); err != nil {
			return exitWith(exitUsage, err)
		}
	}
	rows, err := machineadd.List(ctx, me)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(map[string]any{"machines": rows})
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "No machines added yet (wy machine add <host>).")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSSH\tREACH\tSY\tLAST SEEN\tBACKLOG")
	for _, r := range rows {
		seen, backlog := "-", "-"
		if v := r.HubView; v != nil {
			if v.LastSeen != nil {
				seen = *v.LastSeen
			}
			if v.Backlog != nil {
				backlog = fmt.Sprint(*v.Backlog)
			}
		} else if r.HubErr != "" {
			seen = "?"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.SSH, r.Reach, r.WyVersion, seen, backlog)
	}
	return tw.Flush()
}

func machineRemove(ctx context.Context, e *machineEnv, args []string, stdout, stderr io.Writer) error {
	fs := machineFlags("remove", stderr)
	var o machineadd.RemoveOptions
	fs.BoolVar(&o.Revoke, "revoke", false, "also revoke the machine's devices on the hub")
	fs.BoolVar(&o.KeepRemote, "keep-remote", false, "do not ssh to the machine")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return errUsage
	}
	me, err := manageEnv(e, stderr, *asJSON, stdout, true)
	if err != nil {
		return err
	}
	me.Command = "wy machine remove " + strings.Join(args, " ")
	res, err := machineadd.Remove(ctx, pos[0], o, me)
	if err != nil {
		return manageResult(err)
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(res)
	}
	return nil
}

func machineRepoint(ctx context.Context, e *machineEnv, args []string, stdout, stderr io.Writer) error {
	fs := machineFlags("repoint", stderr)
	which := fs.String("machines", "all", "all, or names separated by commas")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return errUsage
	}
	var names []string
	if *which != "all" && *which != "" {
		names = strings.Split(*which, ",")
	}
	me, err := manageEnv(e, stderr, *asJSON, stdout, false)
	if err != nil {
		return err
	}
	rows, err := machineadd.Repoint(ctx, pos[0], names, me)
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"machines": rows})
	} else {
		for _, r := range rows {
			fmt.Fprintf(stdout, "%-20s %-5s %s\n", r.Name, r.Status, r.Detail)
		}
	}
	return manageResult(err)
}

// ---- remote side: probe, authorize-pull -----------------------------------------------------

func machineProbe(ctx context.Context, e *machineEnv, args []string, stdout, stderr io.Writer) error {
	fs := machineFlags("probe", stderr)
	fs.Bool("json", true, "print JSON (the only output)")
	hubURL := fs.String("hub-url", "", "check GET <url>/health from here")
	ports := fs.String("ports", "", "list the free loopback ports of A-B")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return errUsage
	}
	o := machineadd.ProbeOptions{StateDir: e.dirs.State.Path, ConfigPath: e.dirs.Config.Path, Home: e.home, HubURL: *hubURL, Getenv: e.getenv}
	if *hubURL != "" {
		u, _, err := client.CheckURL(*hubURL, true)
		if err != nil {
			return exitWith(exitUsage, err)
		}
		o.HubURL = u
	}
	if *ports != "" {
		p, err := machineadd.ParsePorts(*ports)
		if err != nil {
			return exitWith(exitUsage, err)
		}
		o.Ports = p
	}
	r, err := machineadd.Probe(ctx, o)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(r)
}

func machineAuthorizePull(e *machineEnv, args []string, stdout, stderr io.Writer) error {
	fs := machineFlags("authorize-pull", stderr)
	keyStdin := fs.Bool("key-stdin", false, "read the hub's public key line from stdin")
	remove := fs.Bool("remove", false, "remove the target's line")
	target := fs.String("target", "", "pull target id (pt_...)")
	from := fs.String("from", "", "from= addresses or CIDRs")
	noRestrict := fs.Bool("no-restrict", false, "use the option list of OpenSSH before 7.2 instead of restrict")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 || *keyStdin == *remove || *target == "" {
		fs.Usage()
		return errUsage
	}
	if *remove {
		changed, err := machineadd.RemovePull(e.home, *target)
		if err != nil {
			return err
		}
		if changed {
			fmt.Fprintf(stdout, "Removed the hub's key for %s from ~/.ssh/authorized_keys.\n", *target)
		} else {
			fmt.Fprintf(stdout, "No key for %s in ~/.ssh/authorized_keys.\n", *target)
		}
		return nil
	}
	line, _ := bufio.NewReader(e.stdin).ReadString('\n')
	wy := e.exe
	if p := filepath.Join(e.home, ".local", "bin", "wy"); fileExists(p) {
		wy = p // the stable path wy update replaces in place
	}
	entry, key, err := machineadd.PullKeyLine(strings.TrimSpace(line), wy, *target, *from, !*noRestrict)
	if err != nil {
		return exitWith(exitUsage, err)
	}
	changed, err := machineadd.AuthorizePull(e.home, entry, key, *target)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(stdout, "Authorized the hub's key for %s (forced command: wy outbox drain --stdio).\n", *target)
	} else {
		fmt.Fprintf(stdout, "The hub's key for %s is already authorized.\n", *target)
	}
	return nil
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
