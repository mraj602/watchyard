package main

// `wy setup` (docs/spec/setup.md 3, 4, 6, 7): detect, the five screens (or flags), the plan,
// apply with a backup set, logins, doctor; `--undo` and `--undo-list`. Machine output
// (`--json`) goes to stdout as one object; human text goes to stderr in that mode.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	neturl "net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/mraj602/watchyard/internal/account"
	"github.com/mraj602/watchyard/internal/claudecli"
	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/doctor"
	"github.com/mraj602/watchyard/internal/hub/cli"
	"github.com/mraj602/watchyard/internal/hub/pair"
	"github.com/mraj602/watchyard/internal/service"
	"github.com/mraj602/watchyard/internal/setup/apply"
	"github.com/mraj602/watchyard/internal/setup/backup"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/plan"
	"github.com/mraj602/watchyard/internal/setup/render"
	"github.com/mraj602/watchyard/internal/setup/tui"
	"github.com/mraj602/watchyard/internal/warp"
)

// setupFlags are the flags of setup.md 4 that are not Choices.
type setupFlags struct {
	dryRun, yes, asJSON, detect bool
	undo, undoList, force       bool
	undoID                      string
	noTimer                     bool
	c                           model.Choices
	// term is --terminal: terminal pieces planned with the setup (terminal.md T10).
	term model.TermChoices
}

// listFlag collects a comma-separated flag value that may also be repeated.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(s string) error {
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			*l = append(*l, x)
		}
	}
	return nil
}

func setupUsage(w io.Writer) {
	fmt.Fprint(w, `usage: wy setup [--dry-run] [--yes] [--json] [--detect]
                [--primary <folder>]
                [--labels '<folder|email>=<label>[:<colour>],...']
                [--add <label>[:<colour>],...] | [--accounts <label>,...]
                [--share defaults|none|<switch>,...]   switches: settings,instructions,addons,plugins,mcp,history,prompts
                [--conflicts keep|primary|merge] [--resolve '<folder>:<item>=keep|primary|folder|merge',...]
                [--hub keep|here|later|pull|join:<url>] [--token-stdin | --code <code> | --code-stdin]
                [--hub-dial <addr>] [--reach direct|forward] [--insecure-lan] [--listen <addr>]
                [--no-plugin] [--no-statusline] [--no-launchers] [--named-launchers] [--launcher-dir <dir>]
                [--login ask|never] [--expect-plan <plan id>] [--no-timer]
                [--terminal pane,titles,warp-plugin,warp-tabs]
       wy setup --undo [<backup id>] [--force] [--dry-run] [--yes] [--json]
       wy setup --undo-list [--json]
`)
}

// parseSetup parses the flags of setup.md 4 into setupFlags.
func parseSetup(e *setupEnv, args []string) (setupFlags, error) {
	var f setupFlags
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() { setupUsage(e.stderr) }
	fs.BoolVar(&f.dryRun, "dry-run", false, "detect and plan, print, change nothing")
	fs.BoolVar(&f.yes, "yes", false, "apply without questions")
	fs.BoolVar(&f.asJSON, "json", false, "machine output on stdout")
	fs.BoolVar(&f.detect, "detect", false, "step 1 only")
	fs.BoolVar(&f.undo, "undo", false, "undo a backup set (the newest by default)")
	fs.BoolVar(&f.undoList, "undo-list", false, "list backup sets")
	fs.BoolVar(&f.force, "force", false, "with --undo: undo an older set, or paths changed since")
	fs.BoolVar(&f.noTimer, "no-timer", false, "hub step: do not install the OS timer")
	primary := fs.String("primary", "", "folder shared items live in")
	var labels, add, accounts, share, resolve, terminal listFlag
	fs.Var(&terminal, "terminal", "pane,titles,warp-plugin,warp-tabs: also set up these terminal pieces (wy term setup)")
	fs.Var(&labels, "labels", "'<folder|email>=<label>[:<colour>],...'")
	fs.Var(&add, "add", "<label>[:<colour>],...: create new folders")
	fs.Var(&accounts, "accounts", "<label>,...: labels for existing folders, extras are added")
	fs.Var(&share, "share", "defaults|none|<switch>,...")
	fs.Var(&resolve, "resolve", "'<folder>:<item>=keep|primary|folder|merge',...")
	conflicts := fs.String("conflicts", "", "keep|primary|merge")
	hub := fs.String("hub", "", "keep|here|later|pull|join:<url>")
	fs.BoolVar(&f.c.CodeStdin, "code-stdin", false, "read the pairing code (or its secret) from the first stdin line")
	fs.StringVar(&f.c.Dial, "hub-dial", "", "with join: reach the hub through this loopback host:port or unix:<path> (forward mode)")
	fs.StringVar(&f.c.Reach, "reach", "", "direct or forward, recorded as hub.reach")
	fs.BoolVar(&f.c.TokenStdin, "token-stdin", false, "read the hub token from the first stdin line")
	code := fs.String("code", "", "machine pairing code for --hub join:<url> (wy hub pair --kind machine)")
	fs.BoolVar(&f.c.InsecureLAN, "insecure-lan", false, "allow plain http to a LAN hub")
	fs.StringVar(&f.c.Listen, "listen", "", "hub listen address for --hub here")
	fs.BoolVar(&f.c.NoPlugin, "no-plugin", false, "skip the plugin")
	fs.BoolVar(&f.c.NoStatusLine, "no-statusline", false, "skip the status line wrap")
	fs.BoolVar(&f.c.NoLaunchers, "no-launchers", false, "write no launchers")
	fs.BoolVar(&f.c.NamedLaunchers, "named-launchers", false, "also write claude-<label>")
	fs.StringVar(&f.c.LauncherDir, "launcher-dir", "", "where launchers go (default ~/.local/bin)")
	fs.StringVar(&f.c.Login, "login", "", "ask|never")
	fs.StringVar(&f.c.ExpectPlan, "expect-plan", "", "apply only if the plan id equals this one")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return f, err
	}
	bad := func(format string, a ...any) (setupFlags, error) {
		fmt.Fprintf(e.stderr, "wy setup: "+format+"\n", a...)
		return f, errUsage
	}
	if len(pos) > 0 {
		if !f.undo || len(pos) > 1 {
			return bad("unexpected argument %q", pos[0])
		}
		f.undoID = pos[0]
	}
	f.c.Code = strings.TrimSpace(*code)
	if f.c.Code != "" && f.c.TokenStdin {
		return bad("use --code or --token-stdin, not both")
	}
	if f.c.CodeStdin && (f.c.Code != "" || f.c.TokenStdin) {
		return bad("--code-stdin excludes --code and --token-stdin")
	}
	f.c.Primary = *primary
	for _, kv := range labels {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return bad("--labels: want <folder|email>=<label>[:<colour>], got %q", kv)
		}
		l, c, err := account.ParseLabelColour(v)
		if err != nil {
			return bad("--labels %q: %v", kv, err)
		}
		if f.c.Labels == nil {
			f.c.Labels = map[string]model.LabelColour{}
		}
		if !strings.Contains(k, "@") {
			k = e.tildeArg(k)
		}
		f.c.Labels[k] = model.LabelColour{Label: l, Colour: c}
	}
	for _, a := range add {
		l, c, err := account.ParseLabelColour(a)
		if err != nil {
			return bad("--add %q: %v", a, err)
		}
		f.c.Add = append(f.c.Add, model.LabelColour{Label: l, Colour: c})
	}
	f.c.Accounts = accounts
	if len(f.c.Add) > 0 && len(f.c.Accounts) > 0 {
		return bad("use --add or --accounts, not both")
	}
	f.c.Share = share
	if _, err := model.ResolveShare(f.c.Share, false); err != nil {
		return bad("--share: %v", err)
	}
	switch *conflicts {
	case "", model.ResolveKeep, model.ResolvePrimary, model.ResolveMerge:
		f.c.Conflicts = *conflicts
	default:
		return bad("--conflicts must be keep, primary or merge")
	}
	for _, r := range resolve {
		i := strings.LastIndex(r, "=")
		if i < 0 {
			return bad("--resolve: want <folder>:<item>=<choice>, got %q", r)
		}
		left, res := r[:i], r[i+1:]
		j := strings.LastIndex(left, ":")
		if j < 0 {
			return bad("--resolve: want <folder>:<item>=<choice>, got %q", r)
		}
		switch res {
		case model.ResolveKeep, model.ResolvePrimary, model.ResolveFolder, model.ResolveMerge:
		default:
			return bad("--resolve %q: choice must be keep, primary, folder or merge", r)
		}
		if f.c.Resolve == nil {
			f.c.Resolve = map[string]string{}
		}
		f.c.Resolve[model.ResolveKey(e.tildeArg(left[:j]), left[j+1:])] = res
	}
	switch h := *hub; {
	case h == "":
	case h == model.HubKeep, h == model.HubLater, h == model.HubPull:
		f.c.Hub.Mode = h
	case h == model.HubHere, h == "local":
		f.c.Hub.Mode = model.HubHere
	case strings.HasPrefix(h, "join:"):
		f.c.Hub = model.HubChoice{Mode: model.HubJoin, URL: strings.TrimPrefix(h, "join:")}
	case strings.HasPrefix(h, "https://"), strings.HasPrefix(h, "http://"):
		f.c.Hub = model.HubChoice{Mode: model.HubJoin, URL: h}
	default:
		return bad("--hub must be keep, here, later, pull or join:<url>")
	}
	if f.c.Hub.Mode == model.HubJoin && f.c.Hub.URL == "" {
		return bad("--hub join needs a URL: join:https://hub.example")
	}
	if (f.c.CodeStdin || f.c.Dial != "" || f.c.Reach != "") && f.c.Hub.Mode != model.HubJoin {
		return bad("--code-stdin, --hub-dial and --reach need --hub join:<url>")
	}
	switch f.c.Reach {
	case "", "direct", "forward":
	default:
		return bad("--reach must be direct or forward (--hub pull for a pulled machine)")
	}
	if f.c.Dial != "" {
		if err := config.CheckDial(f.c.Dial); err != nil {
			return bad("--hub-dial: %v", err)
		}
		if f.c.Reach == "direct" {
			return bad("--hub-dial is the forward mode: drop --reach direct")
		}
	}
	if f.c.Code != "" && f.c.Hub.Mode != model.HubJoin {
		return bad("--code needs --hub join:<url> (a pairing code made by `wy hub pair --kind machine` on that hub)")
	}
	switch f.c.Login {
	case "", "ask", "never":
	default:
		return bad("--login must be ask or never")
	}
	for _, t := range terminal {
		switch t {
		case "pane":
			f.term.Pane = true
		case "titles":
			f.term.Titles = true
		case "warp-plugin":
			f.term.WarpPlugin = true
		case "warp-tabs":
			f.term.WarpTabs = true
		default:
			return bad("--terminal: %q is not one of pane, titles, warp-plugin, warp-tabs", t)
		}
	}
	return f, nil
}

func runSetup(args []string, stdout, stderr io.Writer) error {
	e, err := newSetupEnv(stdout, stderr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return setupMain(ctx, e, args)
}

func setupMain(ctx context.Context, e *setupEnv, args []string) error {
	f, err := parseSetup(e, args)
	if err != nil {
		return err
	}
	e.noTimer = f.noTimer
	// In --json mode human text goes to stderr.
	human := e.stdout
	if f.asJSON {
		human = e.stderr
	}
	// Everything that may change the setup holds setup.lock from here on, so the recovery of
	// an interrupted run below never undoes the set of a run still going (7.6).
	if !f.undoList && !f.detect && !f.dryRun {
		unlock, err := e.lock()
		if err != nil {
			return err
		}
		defer unlock()
	}
	switch {
	case f.undoList:
		return undoList(e, f.asJSON, human)
	case f.undo:
		return undoCmd(ctx, e, f, human)
	}

	st, err := e.detect(ctx, f.c.Primary)
	if err != nil {
		return err
	}
	e.liveSets(&st)
	if f.detect {
		if f.asJSON {
			if err := writeJSON(e.stdout, st); err != nil {
				return err
			}
		} else {
			fmt.Fprint(e.stdout, tui.DetectText(st))
		}
		if st.Claude.Path == "" {
			return exitWith(exitNoClaude, errors.New("Claude Code not found"))
		}
		return nil
	}
	if st.Claude.Path == "" {
		fmt.Fprint(human, tui.DetectText(st))
		return exitWith(exitNoClaude, errors.New("Claude Code not found: install it, then run wy setup again"))
	}

	interactive := e.tty && !f.asJSON && !f.yes && !f.dryRun
	// 7.6: a set left in-progress by a crash is undone (default) or marked applied first.
	if len(st.IncompleteSets) > 0 && !f.dryRun {
		undoIt := f.yes
		if interactive {
			undoIt = e.confirm(fmt.Sprintf("An earlier wy setup run was interrupted (%s). Undo it? (no marks it as applied)", strings.Join(st.IncompleteSets, ", ")), true)
		}
		if f.yes || interactive {
			for _, id := range st.IncompleteSets {
				if err := recoverSet(ctx, e, id, undoIt, human); err != nil {
					return err
				}
			}
			if st, err = e.detect(ctx, f.c.Primary); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(human, "! an earlier run was interrupted (%s); wy setup --yes undoes it first\n", strings.Join(st.IncompleteSets, ", "))
		}
	}

	c := f.c
	c.Interactive = interactive
	if interactive {
		restore := e.rawMode()
		c, err = tui.Run(st, c, tui.Options{In: e.stdin, Out: e.stdout, Raw: restore != nil})
		if restore != nil {
			restore()
		}
		if errors.Is(err, tui.ErrQuit) {
			fmt.Fprintln(e.stdout, "Nothing changed.")
			return nil
		}
		if err != nil {
			return err
		}
		// After the labels: the picker mode of each account with none yet (setup.md 8.5,
		// Choosing in wy setup). A non-interactive run asks nothing.
		c.Modes = e.askSetupModes(ctx, st, c)
	}
	p, err := plan.Plan(st, c)
	if err != nil {
		fmt.Fprintf(e.stderr, "wy setup: %v\n", err)
		return exitWith(exitUsage, nil)
	}
	var termFacts *plan.TermFacts
	if f.term.Any() {
		if p, termFacts, err = e.mergeTerminal(st, p, f.term); err != nil {
			return err
		}
	}
	primary := ""
	for _, fo := range st.Folders {
		if fo.Primary {
			primary = fo.Folder
		}
	}
	in := render.Input{Mode: model.ModeDryRun, Primary: primary, State: &st, Plan: p}
	show := func() error {
		if f.asJSON {
			b, err := render.JSON(in)
			if err != nil {
				return err
			}
			_, err = e.stdout.Write(append(b, '\n'))
			return err
		}
		fmt.Fprint(e.stdout, render.Text(in))
		return nil
	}
	if f.dryRun {
		return show()
	}
	if c.ExpectPlan != "" && c.ExpectPlan != p.PlanID {
		if err := show(); err != nil {
			return err
		}
		return exitWith(exitPlanChanged, fmt.Errorf("the plan changed: expected %s, now %s (nothing applied)", c.ExpectPlan, p.PlanID))
	}
	if p.Empty() {
		if f.asJSON {
			return show()
		}
		fmt.Fprintln(e.stdout, "0 changes: everything is already set up as asked.")
		printNeeds(e.stdout, p.Needs)
		return nil
	}
	if !f.yes {
		if !interactive {
			if err := show(); err != nil {
				return err
			}
			return exitWith(exitNeedsPerson, errors.New("no terminal: rerun with --yes to apply this plan"))
		}
		if err := show(); err != nil {
			return err
		}
		if !e.confirm(fmt.Sprintf("Apply these %d changes?", p.Summary.Changes), true) {
			fmt.Fprintln(e.stdout, "Nothing changed.")
			return nil
		}
	}

	opts, err := e.applyOptions(st, "wy setup "+strings.Join(redactArgs(os.Args[min(2, len(os.Args)):]), " "))
	if err != nil {
		return err
	}
	opts.InsecureLAN = c.InsecureLAN
	if hasOp(p, model.OpConnect) && c.Hub.Mode == model.HubJoin {
		tok := ""
		if f.c.CodeStdin {
			// Read only now, after the plan is accepted: a dry run never reads stdin.
			line, rerr := readLine(e.stdin)
			if rerr == nil && line != "" {
				f.c.Code = line
			}
		}
		if f.c.Code != "" {
			// The code is spent here, after the plan is accepted (a dry run never claims).
			deps := connectDeps{dirs: e.dirs, timer: e.timerEnv(), stdin: strings.NewReader(""), http: e.http}
			if c.Dial != "" {
				if hc, derr := client.HTTPClient(c.Dial, 15*time.Second); derr == nil {
					deps.http = hc
				}
			}
			cu, _, uerr := client.CheckURL(c.Hub.URL, c.InsecureLAN)
			if uerr == nil {
				tok, err = claimMachineToken(ctx, deps, cu, f.c.Code)
			} else {
				err = uerr
			}
			if err != nil {
				fmt.Fprintf(e.stderr, "wy setup: --code: %v\n", err)
				tok = ""
			}
		} else if c.TokenStdin {
			tok, err = readLine(e.stdin)
		} else if interactive {
			tok, err = e.readSecretLine("Device token for " + c.Hub.URL + " (wy hub token create --kind machine): ")
		}
		if err != nil || tok == "" {
			fmt.Fprintln(e.stderr, "wy setup: no hub token given; the hub step will be skipped (exit 6)")
		}
		opts.Token = tok
	}
	deps := e.applyDeps(st, p)
	if termFacts != nil {
		deps.Contents = e.termContents(st, p, *termFacts)
	}
	res, aerr := apply.Run(ctx, p, opts, deps)
	if aerr == nil && termFacts != nil {
		e.termAfterApply(p, false)
	}

	if aerr == nil && len(c.Modes) > 0 {
		e.queueModes(st, c.Modes)
	}
	// Logins (6.2 step 6): interactive only, one folder at a time.
	if aerr == nil && interactive && len(res.Logins) > 0 {
		e.runLogins(ctx, st, res.Logins)
	}

	in.Mode = model.ModeApplied
	switch {
	case res.Status == model.StatusRolledBack:
		in.Mode = model.ModeRolledBack
	case res.Hub != nil && res.Hub.Error != "":
		// The local steps stay; only the hub step is missing (exit 6): say so, and how
		// to finish it, instead of "applied".
		in.Mode = model.ModePartial
		res.Hub.Fix = hubFix(c.Hub, c.Listen, *res.Hub)
	}
	in.Backup, in.Applied, in.Hub = res.Backup, res.Applied, res.Hub
	rep := e.doctorReport(ctx)
	if b, err := json.Marshal(rep); err == nil {
		in.Doctor = b
	}
	if f.asJSON {
		if err := show(); err != nil {
			return err
		}
	} else {
		fmt.Fprint(e.stdout, render.Text(in))
		e.summary(ctx, st, res, rep, c.Hub, c.Listen, interactive && aerr == nil)
		termOn := st.Ledger != nil && st.Ledger.Terminal != nil && !st.Ledger.Terminal.Empty()
		if interactive && aerr == nil && !f.term.Any() && !termOn && warp.Detect(e.termEnv(), "").Present {
			fmt.Fprintln(e.stdout, "Warp is here: wy term setup adds the board pane, tab titles and a coloured tab per account.")
		}
	}
	if aerr != nil {
		code := res.ExitCode
		if code == 0 {
			code = apply.ExitFailed
		}
		return exitWith(code, aerr)
	}
	return nil
}

// hubFix is the next step after a failed hub step (setup.md 6.2 step 5): what to check,
// then the command that finishes the hub part without redoing the local steps.
func hubFix(hub model.HubChoice, listen string, r model.HubResult) string {
	url := strings.TrimRight(r.URL, "/")
	if url == "" {
		url = strings.TrimRight(hub.URL, "/")
	}
	if r.Step == string(model.OpService) || (hub.Mode == model.HubHere && url == "") {
		ln := listen
		if ln == "" {
			ln = service.DefaultListen
		}
		return "check the hub service (wy doctor; systemctl --user status watchyard-hub on Linux), then rerun: wy setup --hub here --listen " + ln
	}
	if hub.Mode == model.HubHere {
		return "check that " + url + "/health answers (wy doctor), then rerun: wy setup --hub here"
	}
	if strings.Contains(r.Error, "no hub token") {
		return "make a code on the hub (wy hub pair --kind machine), then rerun: wy setup --hub join:" + url + " --code <code>"
	}
	if strings.Contains(r.Error, "/health") {
		return "check that " + url + "/health answers from this machine (wy doctor), then: wy flush"
	}
	return "check that " + url + " is reachable and the code or token is fresh (wy hub pair --kind machine on the hub), then rerun: wy setup --hub join:" + url + " --code <code>"
}

// mergeTerminal plans the --terminal pieces from the same State and folds them into the
// setup plan (plan.MergeTerminal): one plan, one backup set, one undo. When the setup plan
// writes wy's marketplace itself, the terminal pieces count on it being there (a first setup
// with --terminal pane). Folders that --add creates are not set up yet when this plans, so
// their terminal pieces come from a later `wy term setup`. A refused piece (an old Claude
// Code for the pane, no Warp for tabs) exits 4 and nothing is applied.
func (e *setupEnv) mergeTerminal(st model.State, p model.Plan, c model.TermChoices) (model.Plan, *plan.TermFacts, error) {
	facts, err := e.termFacts(st, c)
	if err != nil {
		return p, nil, err
	}
	if plan.WritesMarketplace(p, filepath.Join(st.StateDir, "plugin", "marketplace")) {
		facts.MarketExists = true
	}
	tp, err := plan.Terminal(st, c, facts)
	if err != nil {
		if errors.Is(err, plan.ErrTermRefused) {
			return p, nil, exitWith(apply.ExitPrecondition, fmt.Errorf("--terminal: %w", err))
		}
		fmt.Fprintf(e.stderr, "wy setup: --terminal: %v\n", err)
		return p, nil, exitWith(exitUsage, nil)
	}
	return plan.MergeTerminal(p, tp), &facts, nil
}

func hasOp(p model.Plan, op model.Op) bool {
	for _, a := range p.Actions {
		if a.Op == op {
			return true
		}
	}
	return false
}

// redactArgs keeps the command recorded in the manifest free of tokens.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.HasPrefix(a, "wy_") || (i > 0 && (args[i-1] == "--code" || args[i-1] == "--token")) {
			a = "<redacted>"
		}
		out[i] = a
	}
	return out
}

func printNeeds(w io.Writer, needs []model.Need) {
	for _, n := range needs {
		if n.Command != "" {
			fmt.Fprintf(w, "  ? %s: %s\n", n.Kind, n.Command)
		}
	}
}

// summary is the end of screen SetupDone (setup.md 3 step 5).
func (e *setupEnv) summary(ctx context.Context, st model.State, res apply.Result, rep doctor.Report, hub model.HubChoice, listen string, offerPhone bool) {
	w := e.stdout
	if res.Backup != "" {
		fmt.Fprintf(w, "\nBackup %s (undo with: wy setup --undo %s)\n", res.Backup, res.Backup)
	}
	for _, n := range res.Notes {
		fmt.Fprintln(w, "  "+n)
	}
	fmt.Fprintf(w, "wy doctor: %d ok, %d warnings, %d failed\n", rep.OK, rep.Warn, rep.Fail)
	open := 0
	for _, f := range st.Folders {
		if !f.LoggedIn {
			continue
		}
		ags, err := e.runner.Agents(ctx, claudecli.Folder{Dir: f.Dir, Unset: f.Default})
		if err == nil {
			open += len(ags)
		}
	}
	if open > 0 {
		fmt.Fprintf(w, "%d Claude sessions are open: restart them to start reporting.\n", open)
	}
	e.ownerStep(ctx, res, hub, listen)
	e.phoneStep(st, hub, listen, offerPhone)
	fmt.Fprintln(w, "Next machine: wy machine add user@host.")
	if st.Hub.URL != nil {
		fmt.Fprintln(w, "Dashboard: "+*st.Hub.URL)
	}
}

// ownerStep prints the first owner link (pairing.md 4.1) after `wy setup --hub here` when
// the hub step worked and the hub never had an owner session: link, QR, board URL and "Open
// this on your computer to become owner. Pair everything else from the board." Nothing
// otherwise; it never fails setup.
func (e *setupEnv) ownerStep(ctx context.Context, res apply.Result, hub model.HubChoice, listen string) {
	if hub.Mode != model.HubHere || (res.Hub != nil && res.Hub.Error != "") {
		return
	}
	if listen == "" {
		listen = service.DefaultListen
	}
	f, ok := e.stdout.(*os.File)
	ansi := ok && term.IsTerminal(int(f.Fd()))
	fmt.Fprintln(e.stdout)
	if _, err := cli.FirstOwnerLinkDB(ctx, filepath.Join(e.dirs.Data.Path, "hub.db"), "http://"+listen, e.stdout, ansi); err != nil {
		fmt.Fprintf(e.stdout, "Owner: could not make an owner link now (%v). Later: `wy hub owner-link`\n", err)
	}
}

// phoneStep is step 5's phone line (setup.md 3 step 5): offer to pair when the hub is on
// this machine and a person is at the terminal, otherwise say what to run. It never fails
// setup.
// listen is --listen of --hub here ("" = the service default).
func (e *setupEnv) phoneStep(st model.State, hub model.HubChoice, listen string, offer bool) {
	w := e.stdout
	url := ""
	switch {
	case hub.Mode == model.HubJoin:
		url = hub.URL
	case hub.Mode == model.HubHere:
		if listen == "" {
			listen = service.DefaultListen
		}
		url = "http://" + listen
	case st.Hub.URL != nil:
		url = *st.Hub.URL
	}
	switch {
	case url == "":
		fmt.Fprintln(w, "Phone: later (connect a hub first, then wy phone pair)")
		return
	case !isLoopbackURL(url):
		fmt.Fprintf(w, "Phone: open %s as owner -> More -> Devices -> Add phone, or run `wy phone pair` on the hub\n", strings.TrimRight(url, "/"))
		return
	case !offer:
		fmt.Fprintln(w, "Phone: run `wy phone pair` on this machine")
		return
	}
	if !e.confirm("Pair a phone now?", false) {
		fmt.Fprintln(w, "Phone: later, run `wy phone pair` on this machine")
		return
	}
	pp := e.phonePair
	if pp == nil {
		pp = cli.PhonePair
	}
	err := pp([]string{"--db", filepath.Join(e.dirs.Data.Path, "hub.db")}, w, e.stderr)
	switch {
	case err == nil:
	case errors.Is(err, pair.ErrNoURL):
		fmt.Fprintf(w, "Phone: a phone needs an https address. Run `%s` on this machine, then `wy phone pair --url https://<name>.ts.net`\n", service.ServeHint(url))
	default:
		fmt.Fprintf(w, "Phone: could not pair now (%v). Later: `wy phone pair`\n", err)
	}
}

func isLoopbackURL(raw string) bool {
	u, err := neturl.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func (e *setupEnv) doctorReport(ctx context.Context) doctor.Report {
	return doctor.Run(ctx, doctor.Options{Home: e.home, Dirs: e.dirs, Getenv: e.getenv, Environ: os.Environ()})
}

// runLogins hands the terminal to `claude auth login` for each folder (8.2).
func (e *setupEnv) runLogins(ctx context.Context, st model.State, folders []string) {
	m := &account.Manager{Home: e.home, Runner: e.runner}
	for _, fo := range folders {
		if !e.confirm("Log in "+fo+" now?", true) {
			fmt.Fprintf(e.stdout, "Later: %s\n", account.NeedsCommand(fo))
			continue
		}
		req := account.LoginRequest{Folder: claudecli.Folder{Dir: e.expand(fo), Unset: false}, Display: fo, Peers: peers(st, fo)}
		if _, err := m.Login(ctx, req, claudecli.Stdio{In: e.stdin, Out: e.stdout, Err: e.stderr}, e.stdout); err != nil {
			fmt.Fprintf(e.stderr, "wy setup: login of %s: %v\n", fo, err)
		}
	}
}

func peers(st model.State, except string) []account.Peer {
	var out []account.Peer
	for _, f := range st.Folders {
		if f.Folder != except && f.LoggedIn {
			out = append(out, account.Peer{Folder: f.Folder, Email: f.Email, Org: f.Org})
		}
	}
	return out
}

// rawMode puts the terminal in raw mode for the screens; nil when that is not possible
// (TERM=dumb, not a terminal), and the screens then use numbered line prompts.
func (e *setupEnv) rawMode() func() {
	if e.stdinFile == nil || e.getenv("TERM") == "dumb" {
		return nil
	}
	old, err := term.MakeRaw(int(e.stdinFile.Fd()))
	if err != nil {
		return nil
	}
	return func() { _ = term.Restore(int(e.stdinFile.Fd()), old) }
}

func (e *setupEnv) confirm(q string, def bool) bool {
	return tui.Confirm(e.stdin, e.stdout, false, q, def)
}

// recoverSet undoes (or marks applied) a set an interrupted run left in-progress (7.6).
func recoverSet(ctx context.Context, e *setupEnv, id string, undoIt bool, w io.Writer) error {
	if !undoIt {
		fmt.Fprintf(w, "Marking %s as applied.\n", id)
		return backup.MarkApplied(e.dirs.State.Path, id)
	}
	s, err := backup.Open(e.dirs.State.Path, id)
	if err != nil {
		return err
	}
	res, err := backup.UndoSet(ctx, s, e.undoDeps(), backup.Options{}, true)
	if err != nil {
		return fmt.Errorf("undo the interrupted run %s: %w", id, err)
	}
	fmt.Fprintf(w, "Undid the interrupted run %s (%d paths restored).\n", id, len(res.Restored))
	return updateLedgerAfterUndo(e, id)
}

func updateLedgerAfterUndo(e *setupEnv, id string) error {
	s, err := backup.Open(e.dirs.State.Path, id)
	if err != nil {
		return err
	}
	prev, err := ledger.Read(e.dirs.State.Path)
	if err != nil {
		return err
	}
	return ledger.Write(e.dirs.State.Path, ledger.Revert(prev, s.Manifest()))
}

// ---- undo ---------------------------------------------------------------------------------

func undoList(e *setupEnv, asJSON bool, w io.Writer) error {
	sets, err := backup.List(e.dirs.State.Path)
	if err != nil {
		return err
	}
	if asJSON {
		if sets == nil {
			sets = []backup.Info{}
		}
		return writeJSON(e.stdout, map[string]any{"v": 1, "sets": sets})
	}
	if len(sets) == 0 {
		fmt.Fprintln(w, "No backup sets.")
		return nil
	}
	for i := len(sets) - 1; i >= 0; i-- {
		s := sets[i]
		fmt.Fprintf(w, "%-20s %-14s %3d entries  %s  %s\n", s.ID, s.Status, s.Entries, s.StartedAt, s.Command)
	}
	return nil
}

func undoCmd(ctx context.Context, e *setupEnv, f setupFlags, w io.Writer) error {
	opts := backup.Options{Force: f.force}
	id, lines, err := backup.UndoPlan(e.dirs.State.Path, f.undoID, opts)
	var se *backup.StackError
	switch {
	case errors.Is(err, backup.ErrNothingToUndo):
		if f.asJSON {
			return writeJSON(e.stdout, map[string]any{"v": 1, "mode": "undo", "status": "nothing-to-undo"})
		}
		fmt.Fprintln(w, "Nothing to undo.")
		return nil
	case errors.As(err, &se):
		return exitWith(backup.ExitStack, se) // printed once as "wy setup: backup … or use --force"
	case err != nil:
		return err
	}
	fmt.Fprintf(w, "wy setup --undo %s: %d entries\n", id, len(lines))
	for _, l := range lines {
		fmt.Fprintln(w, "  "+l)
	}
	if f.dryRun {
		if f.asJSON {
			return writeJSON(e.stdout, map[string]any{"v": 1, "mode": "dry-run", "backup": id, "entries": lines})
		}
		return nil
	}
	if !f.yes {
		if !e.tty || f.asJSON {
			return exitWith(exitNeedsPerson, errors.New("no terminal: rerun with --yes to undo"))
		}
		if !e.confirm("Undo these?", true) {
			fmt.Fprintln(w, "Nothing changed.")
			return nil
		}
	}
	res, err := backup.Undo(ctx, e.dirs.State.Path, id, e.undoDeps(), opts)
	if errors.As(err, &se) {
		return exitWith(backup.ExitStack, err)
	}
	if err != nil {
		return err
	}
	if lerr := updateLedgerAfterUndo(e, id); lerr != nil {
		fmt.Fprintf(e.stderr, "wy setup: ledger: %v\n", lerr)
	}
	if f.asJSON {
		if err := writeJSON(e.stdout, map[string]any{"v": 1, "mode": "undo", "result": res}); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(w, "%s: %s (%d paths restored, %d keys restored with later edits kept)\n", res.ID, res.Status, len(res.Restored), len(res.KeysRestored))
		for _, k := range res.Kept {
			fmt.Fprintf(w, "  kept %s (it holds files wy did not create, maybe a login)\n", k)
		}
		for _, s := range res.Skipped {
			fmt.Fprintf(w, "  skipped %s: %s\n", s.Path, s.Reason)
		}
		for _, n := range res.Notes {
			fmt.Fprintln(w, "  "+n)
		}
	}
	if res.ExitCode != 0 {
		return exitWith(res.ExitCode, fmt.Errorf("undo partly done: %d paths skipped", len(res.Skipped)))
	}
	return nil
}
