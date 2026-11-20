package main

// `wy account list|add|login|label|share|remove|next` (docs/spec/setup.md 8). add, label and
// share plan and apply through the setup planner and apply (a backup set each); remove is
// journaled path by path in its own backup set; login is never journaled. next is in
// account_next.go.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/account"
	"github.com/mraj602/watchyard/internal/claudecli"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/record"
	"github.com/mraj602/watchyard/internal/setup/apply"
	"github.com/mraj602/watchyard/internal/setup/backup"
	"github.com/mraj602/watchyard/internal/setup/launcher"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/plan"
	"github.com/mraj602/watchyard/internal/setup/render"
	"github.com/mraj602/watchyard/internal/share"
	"github.com/mraj602/watchyard/internal/version"
)

func accountUsage(w io.Writer) {
	fmt.Fprint(w, `usage: wy account list [--json]
       wy account add [<label>] [--colour <c>] [--no-login] [--share <switches>] [--yes] [--dry-run] [--json]
       wy account login <folder|label>
       wy account label <folder|email|label> <new label> [--colour <c>] [--yes] [--dry-run] [--json]
       wy account share [<folder|label>] [--share ...] [--conflicts ...] [--yes] [--dry-run] [--json]
       wy account remove <folder|label> [--unshare] [--yes] [--dry-run] [--json]
       wy account next [--strategy <s>] [--model <m>] [--env | --json | --explain | --exec [-- <cmd>...]] [--no-reserve]
`)
}

func runAccount(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		accountUsage(stderr)
		return errUsage
	}
	if args[0] == "next" { // reads and recommends only: no setup environment
		return runAccountNext(args[1:], stdout, stderr)
	}
	e, err := newSetupEnv(stdout, stderr)
	if err != nil {
		return err
	}
	return accountMain(context.Background(), e, args)
}

// accountFlags are the flags shared by the account subcommands.
type accountFlags struct {
	yes, dryRun, asJSON, noLogin, unshare bool
	colour, conflicts                     string
	share                                 listFlag
	pos                                   []string
}

func parseAccount(e *setupEnv, sub string, args []string) (accountFlags, error) {
	var f accountFlags
	fs := flag.NewFlagSet("account "+sub, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() { accountUsage(e.stderr) }
	fs.BoolVar(&f.yes, "yes", false, "apply without asking")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print the plan, change nothing")
	fs.BoolVar(&f.asJSON, "json", false, "machine output on stdout")
	fs.BoolVar(&f.noLogin, "no-login", false, "add: do not log in afterwards")
	fs.BoolVar(&f.unshare, "unshare", false, "remove: turn each link into an independent copy")
	fs.StringVar(&f.colour, "colour", "", "a palette name or #rrggbb")
	fs.StringVar(&f.colour, "color", "", "alias of --colour")
	fs.StringVar(&f.conflicts, "conflicts", "", "share: keep|primary|merge")
	fs.Var(&f.share, "share", "switches to share")
	pos, err := parseInterspersed(fs, args)
	f.pos = pos
	return f, err
}

func accountMain(ctx context.Context, e *setupEnv, args []string) error {
	sub := args[0]
	f, err := parseAccount(e, sub, args[1:])
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		return accountList(ctx, e, f)
	case "add":
		return accountAdd(ctx, e, f)
	case "login":
		return accountLogin(ctx, e, f)
	case "label":
		return accountLabel(ctx, e, f)
	case "share":
		return accountShare(ctx, e, f)
	case "remove":
		return accountRemove(ctx, e, f)
	}
	fmt.Fprintf(e.stderr, "wy account: unknown subcommand %q\n", sub)
	accountUsage(e.stderr)
	return errUsage
}

// findFolder resolves a folder, a label or a login email to a detected folder.
func findFolder(e *setupEnv, st model.State, key string) (model.Folder, error) {
	t := e.tildeArg(key)
	for _, fo := range st.Folders {
		if fo.Folder == t || fo.Folder == key || (fo.Label != "" && fo.Label == key) || (fo.Email != "" && fo.Email == key) {
			return fo, nil
		}
	}
	for _, fo := range st.Folders {
		if fo.Label == "" && strings.TrimPrefix(filepath.Base(fo.Dir), ".") == key {
			return fo, nil
		}
	}
	return model.Folder{}, fmt.Errorf("no folder, label or login %q", key)
}

func accountList(ctx context.Context, e *setupEnv, f accountFlags) error {
	st, err := e.detect(ctx, "")
	if err != nil {
		return err
	}
	if f.asJSON {
		fs := st.Folders
		if fs == nil {
			fs = []model.Folder{}
		}
		return writeJSON(e.stdout, fs)
	}
	cfg := config.Defaults()
	if cf, err := config.Load(e.dirs.Config.Path); err == nil {
		cfg, _ = cf.Typed()
	}
	excluded := map[string]bool{}
	for _, x := range cfg.Share.MCPExclude {
		excluded[x] = true
	}
	for _, fo := range st.Folders {
		label := fo.Label
		if label == "" {
			label = strings.TrimPrefix(filepath.Base(fo.Dir), ".")
		}
		login := "not logged in"
		if fo.LoggedIn {
			login = strings.TrimSpace(fo.Email + " " + fo.Org + " " + fo.Plan)
		}
		counts := map[string]int{}
		for _, it := range fo.Items {
			switch {
			case it.State == model.ItemLinked:
				counts["linked"]++
			case it.State == model.ItemElsewhere:
				counts["elsewhere"]++
			case it.State == model.ItemCopy:
				counts["copy"]++
			case it.Compare == model.CompareDiffers:
				counts["separate"]++
			}
		}
		var parts []string
		for _, k := range []string{"linked", "elsewhere", "separate", "copy"} {
			if counts[k] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
			}
		}
		launch := "-"
		if fo.Launcher != nil {
			launch = fo.Launcher.Name
		}
		mcp := "on"
		if cfg.MCPMode() != "true" || excluded[fo.Folder] {
			mcp = "off"
		}
		fmt.Fprintf(e.stdout, "%-10s %-8s %-12s %-10s %-34s %-22s mcp %s\n", label, fo.Colour, fo.Folder, launch, login, strings.Join(parts, ", "), mcp)
	}
	return nil
}

// ---- plan helpers -------------------------------------------------------------------------

// subPlan keeps the actions keep accepts and recomputes the ids, the plan id and the count.
func subPlan(p model.Plan, keep func(a model.Action) (model.Action, bool)) model.Plan {
	out := p
	out.Actions = []model.Action{}
	for _, a := range p.Actions {
		if b, ok := keep(a); ok {
			out.Actions = append(out.Actions, b)
		}
	}
	for i := range out.Actions {
		out.Actions[i].ID = fmt.Sprintf("a%d", i+1)
	}
	out.Summary = model.Summary{}
	for _, a := range out.Actions {
		if a.Op != model.OpLogin {
			out.Summary.Changes++
		}
		for _, pre := range a.Pre {
			if pre.State.Type == model.TypeFile || pre.State.Type == model.TypeDir {
				out.Summary.BackedUp++
				break
			}
		}
	}
	out.PlanID = plan.PlanID(out.Actions, out.Conflicts)
	return out
}

// onlyKeys narrows a config_set value to the keys keep accepts.
func onlyKeys(a model.Action, keep func(k string) bool) (model.Action, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(a.Value, &m) != nil {
		return a, false
	}
	for k := range m {
		if !keep(k) {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return a, false
	}
	a.Value, _ = json.Marshal(m)
	return a, true
}

func under(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// runPlan shows a sub-plan and applies it as `wy setup` does (dry run, --yes, no terminal).
func runPlan(ctx context.Context, e *setupEnv, st model.State, p model.Plan, f accountFlags, command string) (apply.Result, error) {
	in := render.Input{Mode: model.ModeDryRun, State: &st, Plan: p}
	for _, fo := range st.Folders {
		if fo.Primary {
			in.Primary = fo.Folder
		}
	}
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
		return apply.Result{}, show()
	}
	if p.Empty() {
		if f.asJSON {
			return apply.Result{}, show()
		}
		fmt.Fprintln(e.stdout, "0 changes.")
		return apply.Result{}, nil
	}
	if !f.yes {
		if err := show(); err != nil {
			return apply.Result{}, err
		}
		if !e.tty || f.asJSON {
			return apply.Result{}, exitWith(exitNeedsPerson, errors.New("no terminal: rerun with --yes to apply"))
		}
		if !e.confirm(fmt.Sprintf("Apply these %d changes?", p.Summary.Changes), true) {
			fmt.Fprintln(e.stdout, "Nothing changed.")
			return apply.Result{}, nil
		}
	}
	opts, err := e.applyOptions(st, command)
	if err != nil {
		return apply.Result{}, err
	}
	res, aerr := apply.Run(ctx, p, opts, e.applyDeps(st, p))
	in.Mode, in.Backup, in.Applied = model.ModeApplied, res.Backup, res.Applied
	if res.Status == model.StatusRolledBack {
		in.Mode = model.ModeRolledBack
	}
	if err := show(); err != nil {
		return res, err
	}
	if aerr != nil {
		code := res.ExitCode
		if code == 0 {
			code = apply.ExitFailed
		}
		return res, exitWith(code, aerr)
	}
	if res.Backup != "" && !f.asJSON {
		fmt.Fprintf(e.stdout, "Backup %s (undo with: wy setup --undo %s)\n", res.Backup, res.Backup)
	}
	return res, nil
}

// ---- add ----------------------------------------------------------------------------------

func accountAdd(ctx context.Context, e *setupEnv, f accountFlags) error {
	if len(f.pos) > 1 {
		accountUsage(e.stderr)
		return errUsage
	}
	st, err := e.detect(ctx, "")
	if err != nil {
		return err
	}
	if st.Claude.Path == "" {
		return exitWith(exitNoClaude, errors.New("Claude Code not found"))
	}
	label := ""
	if len(f.pos) == 1 {
		label = f.pos[0]
	} else {
		label = strings.TrimPrefix(filepath.Base(account.NextFolderFrom(e.home, st.HomeNames)), ".")
	}
	if err := account.ValidateLabel(label); err != nil {
		return exitWith(exitUsage, err)
	}
	if f.colour != "" {
		if _, ok := model.ResolveColour(f.colour); !ok {
			return exitWith(exitUsage, account.ErrBadColour)
		}
	}
	interactive := e.tty && !f.yes && !f.asJSON && !f.dryRun
	c := model.Choices{Add: []model.LabelColour{{Label: label, Colour: f.colour}}, Share: f.share,
		Hub: model.HubChoice{Mode: model.HubKeep}, Interactive: interactive, Login: "never"}
	if interactive && !f.noLogin {
		c.Login = "ask"
	}
	full, err := plan.Plan(st, c)
	if err != nil {
		return exitWith(exitUsage, err)
	}
	newDir := ""
	for _, a := range full.Actions {
		if a.Op == model.OpMkdir {
			newDir = a.Path
		}
	}
	if newDir == "" {
		return errors.New("the planner made no new folder")
	}
	nt := e.tilde(newDir)
	usesMarket := false
	p := subPlan(full, func(a model.Action) (model.Action, bool) {
		switch a.Op {
		case model.OpMkdir:
			return a, a.Path == newDir
		case model.OpLink, model.OpReplaceWithLink, model.OpMove:
			return a, under(a.Path, newDir) || under(a.From, newDir)
		case model.OpRun:
			mine := false
			for _, c := range a.Covers {
				mine = mine || c == nt
			}
			if v := a.Env["CLAUDE_CONFIG_DIR"]; v != nil && *v == newDir {
				mine = true
			}
			if mine {
				usesMarket = true
			}
			return a, mine
		case model.OpJSONSet:
			for _, c := range a.Covers {
				if c == nt && strings.HasPrefix(a.Pointer, "/env/") {
					return a, true
				}
			}
			return a, false
		case model.OpWriteFile:
			return a, strings.HasSuffix(a.Note, " for "+nt)
		case model.OpConfigSet:
			return onlyKeys(a, func(k string) bool { return strings.HasSuffix(k, "."+nt) })
		case model.OpLogin:
			return a, a.Folder == nt
		}
		return a, false
	})
	if usesMarket {
		// The plugin commands for the new folder need the marketplace tree: keep it first.
		for _, a := range full.Actions {
			if a.Op == model.OpWriteTree {
				p = subPlan(model.Plan{Actions: append([]model.Action{a}, p.Actions...), Keep: p.Keep, Conflicts: p.Conflicts, Needs: p.Needs},
					func(a model.Action) (model.Action, bool) { return a, true })
				break
			}
		}
	}
	p.Needs = nil
	for _, n := range full.Needs {
		if n.Folder == nt {
			p.Needs = append(p.Needs, n)
		}
	}
	res, err := runPlan(ctx, e, st, p, f, "wy account add "+label)
	if err != nil || f.dryRun || res.Backup == "" {
		return err
	}
	fmt.Fprintf(e.stdout, "Added %s (%s).\n", nt, label)
	if interactive && !f.noLogin {
		e.runLogins(ctx, st, []string{nt})
	} else {
		fmt.Fprintf(e.stdout, "Log in when you want it: %s\n", account.NeedsCommand(nt))
	}
	return nil
}

// ---- login --------------------------------------------------------------------------------

func accountLogin(ctx context.Context, e *setupEnv, f accountFlags) error {
	if len(f.pos) != 1 {
		accountUsage(e.stderr)
		return errUsage
	}
	if e.claude == "" {
		return exitWith(exitNoClaude, errors.New("Claude Code not found"))
	}
	st, err := e.detect(ctx, "")
	if err != nil {
		return err
	}
	fo, err := findFolder(e, st, f.pos[0])
	if err != nil {
		return exitWith(exitUsage, err)
	}
	m := &account.Manager{Home: e.home, Runner: e.runner}
	_, err = m.Login(ctx, account.LoginRequest{Folder: claudecli.Folder{Dir: fo.Dir, Unset: fo.Default},
		Display: fo.Folder, Label: fo.Label, Peers: peers(st, fo.Folder)},
		claudecli.Stdio{In: e.stdin, Out: e.stdout, Err: e.stderr}, e.stdout)
	return err
}

// ---- label --------------------------------------------------------------------------------

func accountLabel(ctx context.Context, e *setupEnv, f accountFlags) error {
	if len(f.pos) != 2 {
		accountUsage(e.stderr)
		return errUsage
	}
	st, err := e.detect(ctx, "")
	if err != nil {
		return err
	}
	fo, err := findFolder(e, st, f.pos[0])
	if err != nil {
		return exitWith(exitUsage, err)
	}
	label := f.pos[1]
	if err := account.ValidateLabel(label); err != nil {
		return exitWith(exitUsage, err)
	}
	for _, o := range st.Folders {
		if o.Folder != fo.Folder && o.Label == label {
			return exitWith(exitUsage, fmt.Errorf("label %q is used by %s", label, o.Folder))
		}
	}
	want := map[string]string{"slots.labels." + fo.Folder: label}
	colour := fo.Colour
	if f.colour != "" {
		c, ok := model.ResolveColour(f.colour)
		if !ok {
			return exitWith(exitUsage, account.ErrBadColour)
		}
		want["slots.colours."+fo.Folder] = c
		colour = c
	}
	v, _ := json.Marshal(want)
	cp := e.configPath()
	pre := model.Precondition{Path: cp, State: model.PathState{Type: model.TypeAbsent}}
	if st.ConfigPresent {
		pre.State.Type = model.TypeFile
	}
	p := subPlan(model.Plan{Keep: []model.Keep{}, Conflicts: []model.Conflict{}, Needs: []model.Need{}, Actions: []model.Action{{
		Step: model.StepConfig, Op: model.OpConfigSet, Path: cp, Value: v, Pre: []model.Precondition{pre}}}},
		func(a model.Action) (model.Action, bool) { return a, true })
	if fo.Label == label && (f.colour == "" || fo.Colour == colour) {
		p = subPlan(p, func(model.Action) (model.Action, bool) { return model.Action{}, false })
	}
	res, err := runPlan(ctx, e, st, p, f, "wy account label "+fo.Folder+" "+label)
	if err != nil || f.dryRun || res.Backup == "" {
		return err
	}
	if err := sendSlotInfo(e, fo, label, colour); err != nil {
		fmt.Fprintf(e.stderr, "wy account label: slot_info not queued: %v\n", err)
	}
	return nil
}

// sendSlotInfo queues a slot_info record with the label and colour (setup.md 8.4).
func sendSlotInfo(e *setupEnv, fo model.Folder, label, colour string) error {
	m, err := identity.LoadOrCreateMachine(e.dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return err
	}
	extra, err := account.SlotInfoExtra(label, colour)
	if err != nil {
		return err
	}
	r, err := record.NewSlotInfo(record.Meta{Machine: m.ID, Slot: fo.Folder, Client: "wy/" + version.Version},
		record.SlotInfoPayload{Folder: fo.Folder, Default: fo.Default, LoggedIn: fo.LoggedIn, Extra: extra})
	if err != nil {
		return err
	}
	_, err = outbox.New(filepath.Join(e.dirs.State.Path, "outbox")).Append(r)
	return err
}

// ---- share --------------------------------------------------------------------------------

func accountShare(ctx context.Context, e *setupEnv, f accountFlags) error {
	if len(f.pos) > 1 {
		accountUsage(e.stderr)
		return errUsage
	}
	st, err := e.detect(ctx, "")
	if err != nil {
		return err
	}
	var only *model.Folder
	if len(f.pos) == 1 {
		fo, err := findFolder(e, st, f.pos[0])
		if err != nil {
			return exitWith(exitUsage, err)
		}
		only = &fo
	}
	c := model.Choices{Share: f.share, Conflicts: f.conflicts, Hub: model.HubChoice{Mode: model.HubKeep}, Login: "never"}
	full, err := plan.Plan(st, c)
	if err != nil {
		return exitWith(exitUsage, err)
	}
	p := subPlan(full, func(a model.Action) (model.Action, bool) {
		if a.Step != model.StepSharing && a.Step != model.StepMCP {
			return a, false
		}
		if only == nil || a.Op == model.OpMCPSync {
			return a, true
		}
		for _, x := range []string{a.Path, a.From, a.To} {
			if x != "" && under(x, only.Dir) {
				return a, true
			}
		}
		return a, false
	})
	_, err = runPlan(ctx, e, st, p, f, "wy account share")
	return err
}

// ---- remove -------------------------------------------------------------------------------

func accountRemove(ctx context.Context, e *setupEnv, f accountFlags) error {
	if len(f.pos) != 1 {
		accountUsage(e.stderr)
		return errUsage
	}
	st, err := e.detect(ctx, "")
	if err != nil {
		return err
	}
	fo, err := findFolder(e, st, f.pos[0])
	if err != nil {
		return exitWith(exitUsage, err)
	}
	if fo.Primary {
		return exitWith(exitUsage, fmt.Errorf("%s is the primary folder; it cannot be removed", fo.Folder))
	}
	led, err := ledger.Read(e.dirs.State.Path)
	if err != nil {
		return fmt.Errorf("read the ledger: %w", err)
	}
	// Windows synced copies in the folder stop being synced (they stay, as the folder's own).
	var copies []string
	for _, c := range led.Copies {
		if under(c, fo.Dir) {
			copies = append(copies, c)
		}
	}
	var launchers []account.LauncherFile
	for _, l := range led.Launchers {
		b, err := os.ReadFile(l.Path)
		if err != nil {
			continue
		}
		if info, ok := launcher.ParseMarker(b); ok && info.Slot == fo.Folder {
			launchers = append(launchers, account.LauncherFile{Path: l.Path, SHA256: l.SHA256})
		}
	}
	var items []string
	sizes := map[string]int64{}
	if f.unshare {
		var names []string
		for _, it := range model.ShareItems {
			names = append(names, it.Name)
		}
		sizes, _ = account.UnshareSizes(fo.Dir, names)
		for n := range sizes {
			items = append(items, n)
		}
		sort.Strings(items)
	}
	// The plan, as text: remove never deletes anything from the folder.
	var lines []string
	for _, l := range launchers {
		lines = append(lines, "− launcher "+e.tilde(l.Path)+" (if unchanged)")
	}
	for _, it := range items {
		lines = append(lines, fmt.Sprintf("~ %s/%s   link → independent copy (%s)", fo.Folder, it, humanBytes(sizes[it])))
	}
	for _, c := range copies {
		lines = append(lines, "~ "+e.tilde(c)+"   synced copy → independent file (no longer synced)")
	}
	lines = append(lines, "~ config.toml   label and colour of "+fo.Folder+" removed, out of MCP sync")
	if f.asJSON {
		if err := writeJSON(e.stdout, map[string]any{"v": 1, "mode": map[bool]string{true: "dry-run", false: "plan"}[f.dryRun],
			"folder": fo.Folder, "changes": lines}); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(e.stdout, "wy account remove %s: the folder, its login and its links stay\n", fo.Folder)
		for _, l := range lines {
			fmt.Fprintln(e.stdout, "  "+l)
		}
	}
	if f.dryRun {
		return nil
	}
	if !f.yes {
		if !e.tty || f.asJSON {
			return exitWith(exitNeedsPerson, errors.New("no terminal: rerun with --yes"))
		}
		if !e.confirm("Apply?", true) {
			return nil
		}
	}
	unlock, err := e.lock()
	if err != nil {
		return err
	}
	defer unlock()
	// re-read under the lock: what it changes is what this run decides from
	if led, err = ledger.Read(e.dirs.State.Path); err != nil {
		return fmt.Errorf("read the ledger: %w", err)
	}
	m, err := identity.LoadOrCreateMachine(e.dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return err
	}
	set, err := backup.Create(e.dirs.State.Path, backup.Header{Command: "wy account remove " + fo.Folder,
		WyVersion: version.Version, Machine: m.ID}, time.Now())
	if err != nil {
		return err
	}
	mgr := &account.Manager{Home: e.home, Runner: e.runner, Sharer: share.New(e.dirs.State.Path)}
	n := 0
	step := func(op model.Op, spec backup.Spec, act func() error) error {
		n++
		tx, err := set.Begin(fmt.Sprintf("a%d", n), op, spec)
		if err != nil {
			return err
		}
		aerr := act()
		if ferr := tx.Finish(nil); aerr == nil {
			aerr = ferr
		}
		return aerr
	}
	var runErr error
	for _, it := range items {
		p := filepath.Join(fo.Dir, it)
		if runErr = step(model.OpReplaceWithLink, backup.Spec{Paths: []backup.PathSpec{{Path: p}}}, func() error {
			_, err := mgr.Remove(account.RemoveRequest{Dir: fo.Dir, Unshare: true, Items: []string{it}})
			return err
		}); runErr != nil {
			break
		}
	}
	if runErr == nil && len(copies) > 0 {
		cj := share.CopiesPath(e.dirs.State.Path)
		runErr = step(model.OpWriteFile, backup.Spec{Paths: []backup.PathSpec{{Path: cj, Save: backup.SaveCopy}}}, func() error {
			_, err := share.ForgetCopies(e.dirs.State.Path, func(p string) bool { return under(p, fo.Dir) })
			return err
		})
	}
	removed := map[string]bool{}
	if runErr == nil {
		for _, l := range launchers {
			if runErr = step(model.OpWriteFile, backup.Spec{Paths: []backup.PathSpec{{Path: l.Path, Save: backup.SaveCopy}}}, func() error {
				r, err := mgr.Remove(account.RemoveRequest{Dir: fo.Dir, Launchers: []account.LauncherFile{l}})
				for _, x := range r.LaunchersRemoved {
					removed[x] = true
				}
				return err
			}); runErr != nil {
				break
			}
		}
	}
	if runErr == nil {
		cp := e.configPath()
		runErr = step(model.OpConfigSet, backup.Spec{Paths: []backup.PathSpec{{Path: cp, Save: backup.SaveCopy}}}, func() error {
			cf, err := config.Load(e.dirs.Config.Path)
			if err != nil {
				return err
			}
			_ = cf.Delete("slots.labels." + fo.Folder)
			_ = cf.Delete("slots.colours." + fo.Folder)
			cur, _ := cf.Get("share.mcp_exclude")
			var ex []string
			if arr, ok := cur.([]any); ok {
				for _, x := range arr {
					if s, ok := x.(string); ok && s != fo.Folder {
						ex = append(ex, s)
					}
				}
			}
			ex = append(ex, fo.Folder)
			if err := cf.Set("share.mcp_exclude", ex); err != nil {
				return err
			}
			return cf.Save()
		})
	}
	if runErr != nil {
		rb, _ := backup.UndoSet(context.WithoutCancel(ctx), set, e.undoDeps(), backup.Options{}, true)
		return exitWith(apply.ExitFailed, fmt.Errorf("%v (rolled back: %d paths restored)", runErr, len(rb.Restored)))
	}
	if err := set.SetStatus(model.StatusApplied); err != nil {
		return err
	}
	var keepL []model.LedgerLauncher
	for _, l := range led.Launchers {
		if !removed[l.Path] {
			keepL = append(keepL, l)
		}
	}
	led.Launchers = keepL
	var keepM []string
	for _, s := range led.MCPSync {
		if s != fo.StateFile {
			keepM = append(keepM, s)
		}
	}
	led.MCPSync = keepM
	var keepC []string
	for _, c := range led.Copies {
		if !under(c, fo.Dir) {
			keepC = append(keepC, c)
		}
	}
	led.Copies = keepC
	if led.V == 0 {
		led.V = 1
	}
	if err := ledger.Write(e.dirs.State.Path, led); err != nil {
		return err
	}
	if !f.asJSON {
		fmt.Fprintf(e.stdout, "Stopped managing %s. Backup %s (undo with: wy setup --undo %s)\n", fo.Folder, set.ID(), set.ID())
	}
	return nil
}
