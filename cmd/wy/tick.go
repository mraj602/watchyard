package main

// `wy tick` is what the OS timer runs every minute (timer.Command): the share sync of
// setup.md 5.4/5.7 (MCP servers and Windows synced copies, a no-op when nothing changed),
// `wy scan`, `wy agents` at most every 5 minutes, then the hub.json refresh every
// 6 hours, machine_info on change or every 6 hours, the hourly direct-push policy, then
// `wy flush`, then the direct-push check (machines.md 5.4, 6.3, 7.2).

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/detach"
	"github.com/mraj602/watchyard/internal/directpush"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/machineinfo"
	"github.com/mraj602/watchyard/internal/paths"
	peerspkg "github.com/mraj602/watchyard/internal/peers"
	"github.com/mraj602/watchyard/internal/setup/apply"
	"github.com/mraj602/watchyard/internal/setup/ledger"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/share"
)

// agentsEvery is the least time between two `claude agents` passes of the timer.
const agentsEvery = 5 * time.Minute

// moveEvery is the least time between two Smart Account Switch passes of the timer: every
// tick (the timer runs once a minute; setup.md 8.6, a pass every 60 s).
const moveEvery = 50 * time.Second

// runTick implements `wy tick [--quiet]`. With --quiet (the timer) it prints nothing and
// always exits 0; each part runs even when an earlier one failed.
func runTick(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tick", flag.ContinueOnError)
	fs.SetOutput(stderr)
	quiet := fs.Bool("quiet", false, "print nothing and always exit 0 (for the timer)")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "wy tick: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	out, errw := stdout, stderr
	if *quiet {
		out, errw = io.Discard, io.Discard
	}
	var first error
	keep := func(what string, err error) {
		if err != nil {
			fmt.Fprintf(errw, "wy tick: %s: %v\n", what, err)
			if first == nil {
				first = fmt.Errorf("%s: %w", what, err)
			}
		}
	}

	pe := paths.FromOS()
	d, err := paths.Resolve(pe)
	if err != nil {
		return quietErr(*quiet, err)
	}
	res, err := tickShare(context.Background(), pe.Home, d)
	keep("share sync", err)
	if err == nil && !res.Idle() {
		fmt.Fprintf(out, "share: mcp %d file(s) written, %d skipped; copies %d synced, %d in conflict\n",
			len(res.MCP.Written), len(res.MCP.Skipped), len(res.Copies.Synced), len(res.Copies.Conflicts))
	}
	keep("scan", runScan(nil, out, errw))
	if detach.Debounce(filepath.Join(d.State.Path, "agents.stamp"), agentsEvery, time.Now()) {
		keep("agents", runAgents(nil, out, errw))
	}
	cfg := config.Defaults()
	if f, err := config.Load(d.Config.Path); err == nil {
		if c, err := f.Typed(); err == nil {
			cfg = c
		}
	}
	ctx := context.Background()
	keep("hub info", tickHubInfo(ctx, d, cfg, time.Now()))
	if _, err := machineinfo.Send(machineInfoOptions(d, pe.Home, cfg), false); err != nil {
		keep("machine_info", err)
	}
	keep("direct-push policy", tickPolicy(ctx, d, cfg, time.Now()))
	fa := []string{}
	if *quiet {
		fa = append(fa, "--quiet")
	}
	keep("flush", runFlush(fa, out, errw))
	// Smart Account Switch (setup.md 8.6): one pass every moveEvery while it is on.
	if cfg.Account.SmartSwitch && detach.Debounce(filepath.Join(d.State.Path, "move.stamp"), moveEvery, time.Now()) {
		keep("smart account switch", tickMove(out, errw))
	}
	keep("direct push", tickDirectPush(ctx, d, cfg, errw))
	keep("title files", tickPruneTitles(d.State.Path, time.Now()))
	peerspkg.PruneSessionFiles(d.State.Path, time.Now())
	return quietErr(*quiet, first)
}

// tickMove starts one Smart Account Switch pass in the background: a pass may wait minutes
// for a session's step to end (setup.md 8.6, Between steps), and tick must not wait with it.
// Its moves go to <state>/move/moves.jsonl. WY_NO_SPAWN=1 runs it here instead (tests).
func tickMove(out, errw io.Writer) error {
	if os.Getenv("WY_NO_SPAWN") != "1" {
		if exe, err := os.Executable(); err == nil {
			return detach.Start(exe, []string{"move", "--auto"}, nil)
		}
	}
	return runMove([]string{"--auto"}, out, errw)
}

// titleFileTTL is how long `wy hook --title`'s per-session file is kept after its last
// write (docs/spec/terminal.md T6): a session that quiet for a week is gone.
const titleFileTTL = 7 * 24 * time.Hour

// tickPruneTitles removes <state>/term/title-*.json (and a temp file a crashed hook left,
// .title-*) last written more than titleFileTTL before now. Nothing else in <state>/term is
// touched.
func tickPruneTitles(stateDir string, now time.Time) error {
	dir := filepath.Join(stateDir, "term")
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var first error
	for _, en := range ents {
		name := en.Name()
		title := strings.HasPrefix(name, "title-") && strings.HasSuffix(name, ".json")
		if !en.Type().IsRegular() || !(title || strings.HasPrefix(name, ".title-")) {
			continue
		}
		fi, err := en.Info()
		if err != nil || now.Sub(fi.ModTime()) <= titleFileTTL {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}

// tickNetTimeout bounds each of tick's own hub calls, so a hub that is down never makes
// the minute timer pile up; tickRetry is the least time between two failed attempts.
const (
	tickNetTimeout = 5 * time.Second
	tickRetry      = 10 * time.Minute
)

// tickClient is the hub client of this machine (hub.dial honoured), nil without a hub or a
// token (a pulled machine has neither).
func tickClient(d paths.Dirs, cfg config.Config) *client.Client {
	if cfg.Hub.URL == "" {
		return nil
	}
	token, err := client.ReadToken(d.State.Path)
	if err != nil {
		return nil
	}
	cl, err := client.ForConfig(cfg, token)
	if err != nil {
		return nil
	}
	return cl
}

// tickHubInfo re-reads /health into <state>/hub.json every 6 hours (sooner while the hub is
// frozen, machines.md 7.2, 8.3). A failure is retried after tickRetry.
func tickHubInfo(ctx context.Context, d paths.Dirs, cfg config.Config, now time.Time) error {
	cl := tickClient(d, cfg)
	if cl == nil || !client.HubRefreshDue(d.State.Path, cfg.Hub.URL, now) {
		return nil
	}
	if !detach.Debounce(filepath.Join(d.State.Path, "hub.json.try"), tickRetry, now) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, tickNetTimeout)
	defer cancel()
	_, err := client.RefreshHub(ctx, cl, d.State.Path, now)
	if err == nil {
		_ = os.Remove(filepath.Join(d.State.Path, "hub.json.try"))
	}
	return err
}

// tickPolicy fetches the direct-push policy at most hourly (machines.md 5.3) while direct
// push is on and the machine has a token; a failure is retried after tickRetry.
func tickPolicy(ctx context.Context, d paths.Dirs, cfg config.Config, now time.Time) error {
	if !cfg.Push.Direct.Enabled {
		return nil
	}
	cl := tickClient(d, cfg)
	if cl == nil {
		return nil
	}
	if p, ok := directpush.LoadPolicy(d.State.Path); ok {
		if at, err := time.Parse(time.RFC3339, p.FetchedAt); err == nil && now.Sub(at) < directpush.PolicyEvery && !at.After(now) {
			return nil
		}
	}
	if !detach.Debounce(filepath.Join(directpush.PushDir(d.State.Path), "policy.try"), tickRetry, now) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, tickNetTimeout)
	defer cancel()
	_, _, err := directpush.RefreshPolicy(ctx, d.State.Path, cl, now, false)
	if err == nil {
		_ = os.Remove(filepath.Join(directpush.PushDir(d.State.Path), "policy.try"))
	}
	return err
}

// tickDirectPush runs the direct-push decision once (machines.md 5.4). Off, it does nothing.
func tickDirectPush(ctx context.Context, d paths.Dirs, cfg config.Config, errw io.Writer) error {
	if !cfg.Push.Direct.Enabled {
		return nil
	}
	o, err := directEnv{dirs: d, cfg: cfg}.options()
	if err != nil {
		return err
	}
	o.Logf = func(format string, args ...any) { fmt.Fprintf(errw, "wy tick: "+format+"\n", args...) }
	_, err = directpush.Check(ctx, o)
	return err
}

// tickShare runs share.Sync with the MCP sync set from config.toml (share.mcp = true):
// every logged-in folder not in share.mcp_exclude, plus ~/.claude/.claude.json when it
// exists (5.4), and the synced copies the ledger lists (5.7). It never runs claude. While
// a command that changes the setup holds setup.lock it does nothing (the next tick runs):
// it must not sync files that command is changing or undoing.
func tickShare(ctx context.Context, home string, d paths.Dirs) (share.SyncResult, error) {
	unlock, err := apply.Lock(d.State.Path)
	if errors.Is(err, apply.ErrLocked) {
		return share.SyncResult{}, nil
	}
	if err != nil {
		return share.SyncResult{}, err
	}
	defer unlock()
	sh := share.New(d.State.Path)
	cfg := config.Defaults()
	if f, err := config.Load(d.Config.Path); err == nil {
		cfg, _ = f.Typed()
	}
	// The ledger is the authority on which copies are synced: copies.json alone never makes
	// wy write a file. Without a readable ledger the copies pass is skipped.
	sc := share.SyncConfig{}
	if led, err := ledger.Read(d.State.Path); err == nil {
		sc.Copies, sc.CopyPaths = true, led.Copies
	}
	if cfg.MCPMode() == "true" && home != "" {
		req, err := mcpRequest(home, d, cfg)
		if err != nil {
			return share.SyncResult{}, err
		}
		if len(req.Files) >= 2 {
			sc.MCP = &req
		}
	}
	return sh.Sync(ctx, sc)
}

func mcpRequest(home string, d paths.Dirs, cfg config.Config) (model.MCPSyncRequest, error) {
	req := model.MCPSyncRequest{StateDir: d.State.Path}
	loc := identity.Locator{Home: home, FS: identity.OSFS{}}
	slots, err := loc.Discover(cfg.Slots.Extra, func(string) string { return "" })
	if err != nil {
		return req, err
	}
	excluded := map[string]bool{}
	for _, x := range cfg.Share.MCPExclude {
		excluded[x] = true
	}
	primary := cfg.Share.Primary
	if primary == "" {
		primary = "~/.claude"
	}
	store := &identity.Store{StateDir: d.State.Path, FS: identity.OSFS{}}
	for _, s := range slots {
		if excluded[s.Name] {
			continue
		}
		l, err := store.Login(s)
		if err != nil || !l.HasSeat {
			continue
		}
		req.Files = append(req.Files, model.MCPFile{Folder: s.Name, StateFile: s.StateFile, Dir: s.Dir, Primary: s.Name == primary})
		if s.Default {
			alt := filepath.Join(s.Dir, ".claude.json")
			if _, err := os.Stat(alt); err == nil && alt != s.StateFile {
				req.Files = append(req.Files, model.MCPFile{Folder: s.Name, StateFile: alt, Dir: s.Dir})
			}
		}
	}
	return req, nil
}
