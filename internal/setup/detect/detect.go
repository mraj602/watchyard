// Package detect builds the planner's input, a model.State, from the real file system, the
// claude command line and the identity and config packages (setup.md 3, step 1).
//
// Detect is read-only. It never opens `.credentials.json` (every access to a Claude folder
// goes through identity.FS, which refuses it), reads a `.claude.json` only for its
// `mcpServers` key (values are hashed, never kept), and creates nothing.
//
// Depends on: identity, config, claudecli, setup/model, setup/launcher,
// setup/statuswrap.
package detect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/watchyard/internal/claudecli"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// MinClaudeVersion is the version setup.md was checked against; older gives a warning only.
const MinClaudeVersion = "2.1.296"

// AuthTimeout bounds each `claude auth status` call (setup.md 3, step 1.2).
const AuthTimeout = 10 * time.Second

// ProbeTimeout bounds the shell-function probe (step 1.5).
const ProbeTimeout = 3 * time.Second

// Deps is everything Detect touches. Only Home, Claude, ConfigDir and StateDir are
// required; every other field has a real default, and tests inject fakes.
type Deps struct {
	Home string
	GOOS string // default runtime.GOOS
	// Getenv defaults to os.Getenv.
	Getenv func(string) string
	// FS is the guarded view of the Claude folders; default identity.OSFS{}. A test passes a
	// recording FS to prove no forbidden file is opened.
	FS     identity.FS
	Claude claudecli.Runner
	// ConfigDir and StateDir are paths.Dirs.Config.Path and paths.Dirs.State.Path.
	ConfigDir, StateDir string
	// Wy is the absolute path of the wy binary that will be baked into hooks and the status
	// line; empty = os.Executable.
	Wy string
	// Primary is the --primary choice ("~" form or absolute); empty = share.primary of
	// config.toml, else ~/.claude. It must equal Choices.Primary given to the planner.
	Primary string

	LookPath func(string) (string, error) // default exec.LookPath
	// ShellProbe runs `$SHELL -ic 'type <names>'` (stdin closed, TERM=dumb, 3 s) and returns
	// its output. Default: the real probe. Return an error to skip it.
	ShellProbe func(ctx context.Context, names []string) (string, error)
	// HubProbe reports the hub at url (connected hub from config.toml); default asks /health
	// for 2 s and looks for <state>/token. The command layer may add timer/service facts.
	HubProbe func(ctx context.Context, url string) model.HubState
	// Tailscale default: `tailscale status --json` when installed.
	Tailscale func(ctx context.Context) *model.Tailscale
	// SymlinkProbe reports whether file symlinks work (Windows without Developer Mode:
	// false). Default: true off Windows, a try in a temp folder on Windows.
	SymlinkProbe func() bool
}

type detector struct {
	Deps
	home string
	fsys identity.FS
	cfg  *config.File
	st   model.State
	warn []string
}

// Detect collects a State. An error means the home folder could not be read; everything
// else that fails (a folder's login status, a malformed settings file) becomes a warning or
// an AuthError on the folder, so detect always shows what it can.
func Detect(ctx context.Context, d Deps) (model.State, error) {
	if d.Home == "" || d.Claude == nil {
		return model.State{}, errors.New("detect: Home and Claude are required")
	}
	if d.GOOS == "" {
		d.GOOS = runtime.GOOS
	}
	if d.Getenv == nil {
		d.Getenv = os.Getenv
	}
	if d.FS == nil {
		d.FS = identity.OSFS{}
	}
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	if d.Wy == "" {
		d.Wy, _ = os.Executable()
	}
	x := &detector{Deps: d, home: filepath.Clean(d.Home), fsys: d.FS}
	x.st.Home = x.home
	x.st.OS = d.GOOS
	x.st.Wy = d.Wy
	x.st.StateDir = d.StateDir
	x.st.ConfigDir = d.ConfigDir
	return x.run(ctx)
}

func (x *detector) tilde(p string) string { return identity.Tilde(x.home, p) }

func (x *detector) expand(p string) string {
	switch {
	case p == "~":
		return x.home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(x.home, filepath.FromSlash(p[2:]))
	}
	return filepath.Clean(p)
}

func (x *detector) run(ctx context.Context) (model.State, error) {
	x.loadConfig()
	x.detectClaude(ctx)

	// Names in home starting with ".claude": the planner needs them for the next free folder.
	ents, err := x.fsys.ReadDir(x.home)
	if err != nil {
		return model.State{}, fmt.Errorf("list home folder: %w", err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".claude") {
			x.st.HomeNames = append(x.st.HomeNames, e.Name())
		}
	}
	sort.Strings(x.st.HomeNames)

	if err := x.detectFolders(ctx); err != nil {
		return model.State{}, err
	}
	x.detectItems()
	x.detectPlugins()
	x.detectLaunchers(ctx)
	x.detectMCP()
	x.detectWatchyard(ctx)

	if x.GOOS == "windows" {
		ok := false
		if x.SymlinkProbe != nil {
			ok = x.SymlinkProbe()
		} else {
			ok = probeSymlinks()
		}
		x.st.SymlinkOK = &ok
	} else {
		ok := true
		x.st.SymlinkOK = &ok
	}
	x.st.Warnings = append(x.st.Warnings, x.warn...)
	return x.st, nil
}

// loadConfig reads config.toml (never creating it).
func (x *detector) loadConfig() {
	if x.ConfigDir == "" {
		return
	}
	f, err := config.Load(x.ConfigDir)
	if err != nil {
		x.warn = append(x.warn, "config.toml could not be read: "+err.Error())
		return
	}
	x.cfg = f
	if _, err := os.Stat(f.Path()); err == nil {
		x.st.ConfigPresent = true
	}
	if v, err := f.Get("share.primary"); err == nil {
		if s, ok := v.(string); ok {
			x.st.Share.Primary = s
		}
	}
	if v, err := f.Get("share.mcp"); err == nil {
		switch m := v.(type) {
		case bool:
			x.st.Share.MCP = strconv.FormatBool(m)
		case string:
			x.st.Share.MCP = m
		}
	}
}

func (x *detector) cfgStrings(key string) []string {
	if x.cfg == nil {
		return nil
	}
	v, err := x.cfg.Get(key)
	if err != nil {
		return nil
	}
	arr, _ := v.([]any)
	var out []string
	for _, a := range arr {
		if s, ok := a.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (x *detector) cfgMap(key string) map[string]string {
	out := map[string]string{}
	if x.cfg == nil {
		return out
	}
	v, err := x.cfg.Get(key)
	if err != nil {
		return out
	}
	m, _ := v.(map[string]any)
	for k, a := range m {
		if s, ok := a.(string); ok {
			out[k] = s
		}
	}
	return out
}

// detectClaude is step 1.1.
func (x *detector) detectClaude(ctx context.Context) {
	path, err := x.LookPath("claude")
	if err != nil {
		alt := filepath.Join(x.home, ".local", "bin", "claude")
		if fi, e := os.Stat(alt); e == nil && !fi.IsDir() {
			path, err = alt, nil
		}
	}
	info, verr := x.Claude.Version(ctx)
	switch {
	case err != nil && (verr != nil || info.Path == ""):
		return // not found: the command layer exits 7
	case verr != nil:
		x.st.Claude.Path = x.tilde(path)
		x.warn = append(x.warn, "claude --version failed: "+verr.Error())
		return
	}
	if path == "" {
		path = info.Path
	}
	x.st.Claude = model.ClaudeInfo{Path: x.tilde(path), Version: info.Version}
	if versionLess(info.Version, MinClaudeVersion) {
		x.warn = append(x.warn, fmt.Sprintf("Claude Code %s is older than %s, which this setup was checked against", info.Version, MinClaudeVersion))
	}
}

func versionLess(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func versionParts(v string) [3]int {
	var out [3]int
	for i, p := range strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3) {
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out
}

// primaryDir resolves which folder is the primary.
func (x *detector) primaryDir() string {
	switch {
	case x.Primary != "":
		return x.expand(x.Primary)
	case x.st.Share.Primary != "":
		return x.expand(x.st.Share.Primary)
	}
	return filepath.Join(x.home, ".claude")
}

// detectFolders is step 1.2: discovery, then one `claude auth status` per folder in parallel.
func (x *detector) detectFolders(ctx context.Context) error {
	loc := identity.Locator{Home: x.home, FS: x.fsys}
	slots, err := loc.Discover(x.cfgStrings("slots.extra"), x.Getenv)
	if err != nil {
		return err
	}
	labels, colours, modes := x.cfgMap("slots.labels"), x.cfgMap("slots.colours"), x.cfgMap("slots.modes")
	for _, k := range sortedKeys(labels) {
		l := labels[k]
		// A hand-edited config.toml can hold anything; a label reaches launcher names and
		// launcher text, so an invalid one is ignored (the folder gets its default label).
		if !model.ValidLabel(l) {
			delete(labels, k)
			x.warn = append(x.warn, fmt.Sprintf("config.toml: ignoring invalid label %q for %s", l, k))
		}
	}
	prim := x.primaryDir()
	for _, s := range slots {
		f := model.Folder{
			Folder:    s.Name,
			Dir:       s.Dir,
			StateFile: s.StateFile,
			Default:   s.Default,
			Primary:   sameDir(s.Dir, prim),
			Label:     labels[s.Name],
			Colour:    colours[s.Name],
			Mode:      modes[s.Name],
		}
		x.st.Folders = append(x.st.Folders, f)
	}
	var wg sync.WaitGroup
	for i := range x.st.Folders {
		wg.Add(1)
		go func(f *model.Folder, slot identity.Slot) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, AuthTimeout)
			defer cancel()
			st, err := x.Claude.AuthStatus(cctx, claudecli.Folder{Dir: slot.Dir, Unset: slot.Default})
			if err != nil {
				f.AuthError = err.Error()
				return
			}
			f.LoggedIn, f.Email, f.Org, f.Plan = st.LoggedIn, st.Email, st.Org, st.Plan
		}(&x.st.Folders[i], slots[i])
	}
	wg.Wait()
	return nil
}

func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, e1 := filepath.EvalSymlinks(a)
	rb, e2 := filepath.EvalSymlinks(b)
	return e1 == nil && e2 == nil && ra == rb
}

// detectWatchyard is step 1.7.
func (x *detector) detectWatchyard(ctx context.Context) {
	if x.cfg != nil {
		if v, err := x.cfg.Get("hub.url"); err == nil {
			if s, _ := v.(string); s != "" {
				u := s
				x.st.Hub.URL = &u
			}
		}
	}
	if x.st.Hub.URL != nil {
		var h model.HubState
		if x.HubProbe != nil {
			h = x.HubProbe(ctx, *x.st.Hub.URL)
		} else {
			h.Healthy = healthy(ctx, *x.st.Hub.URL)
		}
		url := x.st.Hub.URL
		x.st.Hub = h
		x.st.Hub.URL = url
	}
	// how the hub is reached (machines.md 3). "direct" is the default and is not
	// recorded,.
	if x.cfg != nil {
		if v, err := x.cfg.Get("hub.reach"); err == nil {
			if s, _ := v.(string); s != "" && s != "direct" {
				x.st.Hub.Reach = s
			}
		}
		if v, err := x.cfg.Get("hub.dial"); err == nil {
			if s, _ := v.(string); s != "" {
				x.st.Hub.Dial = s
			}
		}
	}
	if x.st.Hub.URL == nil && x.st.Hub.Reach == "pull" && x.HubProbe != nil {
		// Pulled: no URL to ask, but the timer still matters.
		x.st.Hub.TimerActive = x.HubProbe(ctx, "").TimerActive
	}
	if x.StateDir != "" {
		if _, err := os.Stat(filepath.Join(x.StateDir, "token")); err == nil {
			x.st.Hub.TokenPresent = true
		}
		x.st.Ledger = readLedger(filepath.Join(x.StateDir, "setup", "ledger.json"))
		x.st.IncompleteSets = incompleteSets(filepath.Join(x.StateDir, "backup"))
	}
	if x.Tailscale != nil {
		x.st.Tailscale = x.Tailscale(ctx)
	} else if _, err := x.LookPath("tailscale"); err == nil {
		x.st.Tailscale = realTailscale(ctx)
	}
}

func healthy(ctx context.Context, url string) bool {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, strings.TrimRight(url, "/")+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func probeSymlinks() bool {
	dir, err := os.MkdirTemp("", "wy-symlink-probe")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	target := filepath.Join(dir, "t")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		return false
	}
	return os.Symlink(target, filepath.Join(dir, "l")) == nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
