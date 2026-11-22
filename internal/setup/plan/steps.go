package plan

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/setup/launcher"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/statuswrap"
)

// --- status line (9.1) --------------------------------------------------------------------

func (p *planner) planStatusLine() error {
	if p.c.NoStatusLine {
		return nil
	}
	S := p.st.Wy
	seen := map[string]bool{}
	for _, n := range p.nodes {
		t := p.finalTarget(n)
		if seen[t] {
			continue
		}
		seen[t] = true
		if !p.targetExists(t) {
			continue
		}
		var sl *model.StatusLine
		if ts := p.targetState(t); ts != nil {
			sl = ts.StatusLine
		}
		cov := p.covers(t)
		set := func(pointer string, before, value json.RawMessage) {
			p.add(model.StepStatusLine, model.Action{Op: model.OpJSONSet, File: t, Via: p.viaOf(t), Pointer: pointer,
				Before: before, Value: value, Covers: cov, Pre: pres(preType(t, model.TypeFile))})
		}
		switch {
		case sl == nil:
			v, _ := json.Marshal(struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			}{"command", statuswrap.Wrap("", S)})
			set("/statusLine", nil, v)
		case sl.Type != "" && sl.Type != "command":
			p.warn("%s: statusLine has type %q; left alone", p.tilde(t), sl.Type)
		case sl.Command == "":
			set("/statusLine/command", nil, jstr(statuswrap.Wrap("", S)))
		case statuswrap.IsOurs(sl.Command):
			want := statuswrap.Wrap(sl.Command, S)
			if want == sl.Command {
				p.out.Keep = append(p.out.Keep, model.Keep{What: "statusline", Path: p.tilde(t), Note: "already ours"})
			} else {
				set("/statusLine/command", jstr(sl.Command), jstr(want))
			}
		default:
			set("/statusLine/command", jstr(sl.Command), jstr(statuswrap.Wrap(sl.Command, S)))
		}
	}
	return nil
}

// --- MCP (5.4) ----------------------------------------------------------------------------

func (p *planner) planMCP() error {
	if !p.share.On[model.SwitchMCP] {
		if p.st.MCP.HandMade {
			p.out.Keep = append(p.out.Keep, model.Keep{What: "mcp", Path: p.st.MCP.Script, Note: "external"})
		}
		return nil
	}
	var set []*fnode
	for _, n := range p.nodes {
		if !n.isNew && n.f.LoggedIn && n.f.StateFile != "" && n.f.MCPNames != nil {
			set = append(set, n)
		}
	}
	if len(set) < 2 {
		return nil
	}
	ref := set[0]
	for _, n := range set {
		if n.primary {
			ref = n
		}
	}
	// The reference hash of each name: the primary's, else the first folder that has it.
	refHash := map[string]string{}
	for _, name := range sortedKeys(ref.f.MCPNames) {
		refHash[name] = ref.f.MCPNames[name]
	}
	for _, n := range set {
		for _, name := range sortedKeys(n.f.MCPNames) {
			if _, ok := refHash[name]; !ok {
				refHash[name] = n.f.MCPNames[name]
			}
		}
	}
	added, changed := map[string]bool{}, map[string]bool{}
	var files []string
	var pre []model.Precondition
	for _, n := range set {
		dirty := false
		for name, h := range refHash {
			cur, has := n.f.MCPNames[name]
			switch {
			case !has:
				added[name], dirty = true, true
			case cur != h:
				changed[name], dirty = true, true
			}
		}
		if dirty {
			files = append(files, n.f.StateFile)
			pre = append(pre, preType(n.f.StateFile, model.TypeFile))
		}
	}
	if len(files) == 0 {
		return nil
	}
	p.add(model.StepMCP, model.Action{Op: model.OpMCPSync, Files: files,
		Names: &model.NameChanges{Added: sortedKeys(added), Changed: sortedKeys(changed)}, Pre: pre})
	return nil
}

// --- launchers (8.3) ----------------------------------------------------------------------

func (p *planner) launcherTaken(name string) bool {
	for _, l := range p.st.Launchers {
		if l.Name == name && l.Kind != model.LauncherScript {
			return true
		}
	}
	return false
}

func (p *planner) planLaunchers() error {
	if p.c.NoLaunchers {
		return nil
	}
	dir := filepath.Join(p.home, ".local", "bin")
	if p.c.LauncherDir != "" {
		dir = p.expand(p.c.LauncherDir)
	}
	written := map[string]bool{}
	write := func(name string, n *fnode) error {
		if written[name] {
			return nil
		}
		written[name] = true
		path := filepath.Join(dir, launcher.FileName(p.st.OS, name))
		if !inside(dir, path) {
			return fmt.Errorf("launcher %q would be written outside %s", name, dir)
		}
		spec := launcher.Spec{Slot: n.f.Folder, Label: n.label}
		if err := launcher.Check(p.st.OS, spec); err != nil {
			p.warn("no launcher %s for %s: %v", name, n.f.Folder, err)
			return nil
		}
		body := launcher.Render(p.st.OS, spec)
		p.add(model.StepLaunchers, model.Action{Op: model.OpWriteFile, Path: path, Mode: "0755",
			ContentSHA256: launcher.Hash(body), Note: name + " for " + n.f.Folder, Pre: pres(preAbsent(path))})
		return nil
	}
	for _, n := range p.nodes {
		if filepath.Clean(n.f.Dir) == filepath.Join(p.home, ".claude") {
			continue
		}
		name := launcher.Name(n.f.Dir)
		if name == "claude" || name == "claude-1" {
			continue
		}
		if n.f.Launcher == nil && !p.launcherTaken(name) {
			if err := write(name, n); err != nil {
				return err
			}
		}
		if p.c.NamedLaunchers {
			named := "claude-" + n.label
			if named != name && !p.launcherTaken(named) {
				if err := write(named, n); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// --- config.toml (9.2) --------------------------------------------------------------------

func (p *planner) planConfig() error {
	want := map[string]any{}
	if cur := p.st.Share.Primary; cur != p.tilde(p.primDir) {
		want["share.primary"] = p.tilde(p.primDir)
	}
	mcp := "false"
	switch {
	case p.share.On[model.SwitchMCP]:
		mcp = "true"
	case p.st.MCP.HandMade:
		mcp = "external"
	}
	if p.st.Share.MCP != mcp {
		switch mcp {
		case "true":
			want["share.mcp"] = true
		case "false":
			want["share.mcp"] = false
		default:
			want["share.mcp"] = mcp
		}
	}
	for _, n := range p.nodes {
		if n.f.Label != n.label {
			want["slots.labels."+n.f.Folder] = n.label
		}
		if n.f.Colour != n.colour {
			want["slots.colours."+n.f.Folder] = n.colour
		}
	}
	// The picker modes answered in an interactive run (setup.md 8.5, Choosing in wy setup).
	for k, v := range p.c.Modes {
		if !model.ValidModeKey(k) || (v != "protected" && v != "aggressive") {
			return fmt.Errorf("mode %s = %q: want slots.modes.<folder> or next.accounts.<key>.mode, protected or aggressive", k, v)
		}
		want[k] = v
	}
	if len(want) == 0 {
		return nil
	}
	path := filepath.Join(p.st.ConfigDir, "config.toml")
	v, _ := json.Marshal(want) // keys sorted
	pre := preAbsent(path)
	if p.st.ConfigPresent {
		pre = preType(path, model.TypeFile)
	}
	p.add(model.StepConfig, model.Action{Op: model.OpConfigSet, Path: path, Value: v, Pre: pres(pre)})
	return nil
}

// --- hub (4, 6.2 step 5) ------------------------------------------------------------------

func (p *planner) planHub() error {
	mode := p.c.Hub.Mode
	h := p.st.Hub
	connected := h.URL != nil
	if mode == "" || mode == model.HubKeep {
		if connected {
			mode = model.HubKeep
		} else {
			mode = model.HubLater
		}
	}
	switch mode {
	case model.HubKeep:
		note := "keep " + *h.URL
		if !h.Healthy {
			note += " (not answering /health now)"
		}
		p.out.Keep = append(p.out.Keep, model.Keep{What: "hub", Path: *h.URL, Note: note})
	case model.HubLater:
		p.out.Keep = append(p.out.Keep, model.Keep{What: "hub", Note: "later"})
	case model.HubHere:
		listen := p.c.Listen
		if listen == "" {
			listen = "127.0.0.1:8787"
		}
		url := "http://" + listen
		if connected && *h.URL == url && h.Healthy && h.ServiceActive {
			p.out.Keep = append(p.out.Keep, model.Keep{What: "hub", Path: url, Note: "hub already running here"})
			return nil
		}
		unit, name := p.serviceUnit()
		if !(h.ServiceInstalled && h.ServiceActive) {
			p.add(model.StepHub, model.Action{Op: model.OpService, Name: name, Unit: unit, Listen: p.c.Listen,
				Note: "wy hub serve --listen " + listen, Pre: pres(preAbsent(unit))})
		}
		p.add(model.StepHub, model.Action{Op: model.OpConnect, URL: url, Note: "token minted locally; installs the timer"})
		if p.st.OS == "linux" && h.LingerOff {
			p.warn("the hub stops when you log out; run `loginctl enable-linger`")
		}
	case model.HubPull:
		// (machines.md 3.4): the hub pulls this machine over SSH. No URL, no token.
		if !connected && h.Reach == model.HubPull && h.TimerActive {
			p.out.Keep = append(p.out.Keep, model.Keep{What: "hub", Note: "pulled over SSH (already set up)"})
			return nil
		}
		p.add(model.StepHub, model.Action{Op: model.OpConnect, Reach: model.HubPull,
			Note: "pulled over SSH (run `wy machine add --reach pull` from your laptop, or `wy hub pull add` on the hub); installs the timer"})
	case model.HubJoin:
		url := p.c.Hub.URL
		if url == "" {
			return fmt.Errorf("--hub join needs a URL")
		}
		// reach and dial as asked (forward when a dial is given), compared with now.
		reach := p.c.Reach
		if reach == "" && p.c.Dial != "" {
			reach = "forward"
		}
		curReach := h.Reach
		if curReach == "" {
			curReach = "direct"
		}
		sameLink := p.c.Dial == h.Dial && (reach == "" || reach == curReach)
		if connected && *h.URL == url && h.Healthy && h.TokenPresent && sameLink {
			p.out.Keep = append(p.out.Keep, model.Keep{What: "hub", Path: url, Note: "already connected"})
			return nil
		}
		if p.c.TokenStdin || p.c.Code != "" || p.c.CodeStdin || p.c.Interactive {
			note := "token from stdin; installs the timer"
			if p.c.Code != "" || p.c.CodeStdin {
				note = "pairing code; installs the timer"
			}
			if reach == "forward" {
				note = strings.Replace(note, ";", ", through an SSH forward;", 1)
			}
			p.add(model.StepHub, model.Action{Op: model.OpConnect, URL: url, Dial: p.c.Dial, Reach: reach, Note: note})
		} else {
			p.out.Needs = append(p.out.Needs, model.Need{Kind: "hub-token", Command: "wy connect " + url + " --token-stdin"})
			p.out.Keep = append(p.out.Keep, model.Keep{What: "hub", Path: url, Note: "needs a token from you"})
		}
	default:
		return fmt.Errorf("unknown hub mode %q", mode)
	}
	return nil
}

// serviceUnit is where internal/service installs the hub (service.UnitPath of DefaultName):
// the plist is named after its label, com.<slug>.hub.
func (p *planner) serviceUnit() (unit, name string) {
	name = brand.Slug + "-hub"
	switch p.st.OS {
	case "darwin":
		return filepath.Join(p.home, "Library", "LaunchAgents", "com."+brand.Slug+".hub.plist"), name
	case "windows":
		return "", name
	}
	return filepath.Join(p.home, ".config", "systemd", "user", name+".service"), name
}

// --- logins -------------------------------------------------------------------------------

func (p *planner) planLogins() error {
	login := p.c.Login
	if login == "" {
		login = "never"
		if p.c.Interactive {
			login = "ask"
		}
	}
	for _, n := range p.nodes {
		if n.f.LoggedIn {
			continue
		}
		cmd := "claude auth login --claudeai"
		if !n.f.Default {
			cmd = "CLAUDE_CONFIG_DIR=" + n.f.Folder + " " + cmd
		}
		p.out.Needs = append(p.out.Needs, model.Need{Kind: "login", Folder: n.f.Folder, Command: cmd})
		if login == "ask" && n.isNew {
			p.add(model.StepLogin, model.Action{Op: model.OpLogin, Folder: n.f.Folder})
		}
	}
	return nil
}

// inside reports whether path is a direct child of dir (no "..", no separator in the name):
// a launcher name comes from a folder name or a label and must never leave the launcher dir.
func inside(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && filepath.Base(rel) == rel
}
