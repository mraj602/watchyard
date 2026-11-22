// Package plan is the pure planner of `wy setup`: Plan(State, Choices) gives the ordered
// actions, what is kept, the conflicts, what needs the person, a summary and a plan id
// (setup.md 5, 6, 9.1, 10). It reads no file and runs no command; everything it knows is in
// the State that detect built.
//
// Depends on: setup/model, setup/launcher, setup/statuswrap, identity (Tilde).
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/version"
)

// Options carries the facts that are not part of State or Choices.
type Options struct {
	// Version is the wy version baked into the plugin marketplace; default version.Version.
	Version string
}

// ErrPrimaryMismatch is returned when Choices.Primary names a different folder from the one
// detect marked primary: detect must be run with the same primary (Deps.Primary).
var ErrPrimaryMismatch = errors.New("plan: primary differs from the one detect used")

// PluginID is the plugin wy installs through its own marketplace.
const (
	MarketplaceName = brand.Marketplace
	PluginID        = brand.Slug + "@" + brand.Marketplace
)

// Plan computes the plan with the running binary's version.
func Plan(st model.State, c model.Choices) (model.Plan, error) {
	return PlanWith(st, c, Options{Version: version.Version})
}

// Account is one folder the plan keeps or creates, with its final label (setup.md 8.5,
// Choosing in wy setup and wy account add).
type Account struct {
	Folder   string // "~/.claude-3"
	Label    string
	Email    string // the login's email ("" when not logged in or new)
	LoggedIn bool
	New      bool // created by this plan
}

// Accounts runs only the folder and label part of the planner on st and c.
func Accounts(st model.State, c model.Choices) ([]Account, error) {
	p := &planner{st: st, c: c, opt: Options{Version: version.Version}, home: cleanPath(st.Home),
		steps: map[model.Step][]model.Action{}, warned: map[string]bool{}}
	p.win = st.OS == "windows"
	p.symlinks = st.SymlinkOK == nil || *st.SymlinkOK
	if err := p.buildFolders(); err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(p.nodes))
	for _, n := range p.nodes {
		out = append(out, Account{Folder: n.f.Folder, Label: n.label, Email: n.f.Email, LoggedIn: n.f.LoggedIn, New: n.isNew})
	}
	return out, nil
}

// fnode is one folder in the plan: a detected one or one created in this run.
type fnode struct {
	f       model.Folder
	label   string
	colour  string
	isNew   bool
	primary bool
	links   map[string]bool // item -> linked to the primary now or after the plan
}

func (n *fnode) item(name string) model.ItemState {
	if it, ok := n.f.Items[name]; ok {
		return it
	}
	return model.ItemState{State: model.ItemAbsent}
}

type planner struct {
	st   model.State
	c    model.Choices
	opt  Options
	home string

	nodes    []*fnode
	prim     *fnode
	primDir  string
	share    model.ShareSet
	steps    map[model.Step][]model.Action
	out      model.Plan
	win      bool
	symlinks bool

	settingsCreated bool
	warned          map[string]bool
	rootOf          map[string]string
	touched         map[string]bool
	envTargets      []string
	ownPlugins      []ownPlugin
}

// PlanWith is Plan with explicit options.
func PlanWith(st model.State, c model.Choices, opt Options) (model.Plan, error) {
	if opt.Version == "" {
		opt.Version = version.Version
	}
	p := &planner{st: st, c: c, opt: opt, home: cleanPath(st.Home), steps: map[model.Step][]model.Action{},
		warned: map[string]bool{}}
	p.win = st.OS == "windows"
	p.symlinks = st.SymlinkOK == nil || *st.SymlinkOK
	var err error
	if p.share, err = model.ResolveShare(c.Share, st.MCP.HandMade); err != nil {
		return model.Plan{}, err
	}
	if err := p.buildFolders(); err != nil {
		return model.Plan{}, err
	}
	for _, step := range []func() error{
		p.planNewFolders, p.planSharing, p.planPlugin, p.planStatusLine, p.planMCP,
		p.planLaunchers, p.planConfig, p.planHub, p.planLogins,
	} {
		if err := step(); err != nil {
			return model.Plan{}, err
		}
	}
	return p.finish(), nil
}

func (p *planner) tilde(path string) string { return identity.Tilde(p.home, path) }

func (p *planner) expand(s string) string {
	switch {
	case s == "~":
		return p.home
	case strings.HasPrefix(s, "~/") && posixRooted(p.home):
		return path.Join(p.home, s[2:])
	case strings.HasPrefix(s, "~/"):
		return filepath.Join(p.home, filepath.FromSlash(s[2:]))
	}
	return cleanPath(s)
}

// cleanPath is filepath.Clean that leaves a POSIX-rooted path in its own separator.
func cleanPath(s string) string {
	if posixRooted(s) {
		return path.Clean(s)
	}
	return filepath.Clean(s)
}

// posixRooted reports whether s is a /-rooted path with no backslash: it keeps its own
// separator even where the host's is `\` (a test pretending to be a POSIX machine).
func posixRooted(s string) bool { return strings.HasPrefix(s, "/") && !strings.Contains(s, `\`) }

func (p *planner) warn(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	if !p.warned[s] {
		p.warned[s] = true
		p.out.Warnings = append(p.out.Warnings, s)
	}
}

func (p *planner) add(step model.Step, a model.Action) {
	a.Step = step
	p.steps[step] = append(p.steps[step], a)
}

func (p *planner) others() []*fnode {
	var out []*fnode
	for _, n := range p.nodes {
		if !n.primary {
			out = append(out, n)
		}
	}
	return out
}

func (p *planner) folderNames(ns []*fnode) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.f.Folder)
	}
	return out
}

// --- folders, labels, colours -------------------------------------------------------------

func (p *planner) buildFolders() error {
	primDir := ""
	switch {
	case p.c.Primary != "":
		primDir = p.expand(p.c.Primary)
	default:
		for _, f := range p.st.Folders {
			if f.Primary {
				primDir = f.Dir
			}
		}
		if primDir == "" {
			primDir = filepath.Join(p.home, ".claude")
		}
	}
	p.primDir = primDir
	for _, f := range p.st.Folders {
		n := &fnode{f: f, links: map[string]bool{}}
		n.primary = filepath.Clean(f.Dir) == primDir
		if n.primary != f.Primary {
			return ErrPrimaryMismatch
		}
		if n.primary {
			p.prim = n
		}
		for name, it := range f.Items {
			if it.State == model.ItemLinked {
				n.links[name] = true
			}
		}
		p.nodes = append(p.nodes, n)
	}
	if p.prim == nil {
		return fmt.Errorf("primary folder %s not found (has Claude Code been run once?)", p.tilde(primDir))
	}

	// Labels of existing folders: --labels (by folder or email), then --accounts, then the
	// label already in config.toml, then the folder name without the dot.
	used := map[string]string{} // label -> folder
	labelFor := map[string]model.LabelColour{}
	matched := map[string]bool{}
	for key, lc := range p.c.Labels {
		var hit *fnode
		for _, n := range p.nodes {
			if key == n.f.Folder || p.expand(key) == n.f.Dir || (n.f.Email != "" && key == n.f.Email) {
				hit = n
				break
			}
		}
		if hit == nil {
			return fmt.Errorf("--labels: no folder or login matches %q", key)
		}
		matched[key] = true
		labelFor[hit.f.Folder] = lc
	}
	accounts := p.c.Accounts
	adds := append([]model.LabelColour(nil), p.c.Add...)
	if len(accounts) > len(p.nodes) {
		var extra []model.LabelColour
		for _, l := range accounts[len(p.nodes):] {
			extra = append(extra, model.LabelColour{Label: l})
		}
		adds = append(extra, adds...)
		accounts = accounts[:len(p.nodes)]
	}
	for i, l := range accounts {
		if _, ok := labelFor[p.nodes[i].f.Folder]; !ok {
			labelFor[p.nodes[i].f.Folder] = model.LabelColour{Label: l}
		}
	}
	usedColours := map[string]bool{}
	for _, n := range p.nodes {
		lc, explicit := labelFor[n.f.Folder]
		switch {
		case explicit && lc.Label != "":
			if !model.ValidLabel(lc.Label) {
				return fmt.Errorf("invalid label %q (use a-z, 0-9 and -, at most 24 characters)", lc.Label)
			}
			n.label = lc.Label
		case n.f.Label != "" && model.ValidLabel(n.f.Label):
			// detect already drops invalid config labels; checked again because the label
			// becomes a launcher file name and launcher text.
			n.label = n.f.Label
		default:
			n.label = defaultLabel(n.f.Dir)
		}
		if other, dup := used[n.label]; dup {
			return fmt.Errorf("label %q is used by both %s and %s", n.label, other, n.f.Folder)
		}
		used[n.label] = n.f.Folder
		switch {
		case lc.Colour != "":
			c, ok := model.ResolveColour(lc.Colour)
			if !ok {
				return fmt.Errorf("invalid colour %q", lc.Colour)
			}
			n.colour = c
		case n.f.Colour != "":
			n.colour = n.f.Colour
		}
		if n.colour != "" {
			usedColours[n.colour] = true
		}
	}

	// New folders (5.6).
	taken := map[string]bool{}
	for _, name := range p.st.HomeNames {
		taken[name] = true
	}
	for _, n := range p.nodes {
		taken[filepath.Base(n.f.Dir)] = true
	}
	for _, a := range adds {
		if !model.ValidLabel(a.Label) {
			return fmt.Errorf("invalid label %q (use a-z, 0-9 and -, at most 24 characters)", a.Label)
		}
		if other, dup := used[a.Label]; dup {
			return fmt.Errorf("label %q is already used by %s", a.Label, other)
		}
		used[a.Label] = "a new folder"
		colour := ""
		if a.Colour != "" {
			c, ok := model.ResolveColour(a.Colour)
			if !ok {
				return fmt.Errorf("invalid colour %q", a.Colour)
			}
			colour = c
			usedColours[c] = true
		}
		name := nextFree(taken)
		taken[name] = true
		dir := filepath.Join(p.home, name)
		n := &fnode{isNew: true, label: a.Label, colour: colour, links: map[string]bool{}}
		n.f = model.Folder{Folder: p.tilde(dir), Dir: dir, New: true, SettingsTarget: filepath.Join(dir, "settings.json"),
			Items: map[string]model.ItemState{}}
		p.nodes = append(p.nodes, n)
	}
	// Colours not given: the next free palette colour, in folder order.
	next := 0
	for _, n := range p.nodes {
		if n.colour != "" {
			continue
		}
		for tries := 0; tries < len(model.Palette); tries++ {
			c := model.Palette[next%len(model.Palette)].Hex
			next++
			if !usedColours[c] {
				n.colour = c
				usedColours[c] = true
				break
			}
		}
		if n.colour == "" {
			n.colour = model.Palette[(next-1)%len(model.Palette)].Hex
		}
	}
	return nil
}

// nextFree is the next free folder name: .claude-N for the smallest N >= 2 such that neither
// .claude-N nor any .claude-N<non-digit>... entry exists.
func nextFree(taken map[string]bool) string {
	for n := 2; ; n++ {
		base := fmt.Sprintf(".claude-%d", n)
		free := true
		for name := range taken {
			if rest, ok := strings.CutPrefix(name, base); ok && (rest == "" || rest[0] < '0' || rest[0] > '9') {
				free = false
				break
			}
		}
		if free {
			return base
		}
	}
}

func (p *planner) planNewFolders() error {
	for _, n := range p.nodes {
		if !n.isNew {
			continue
		}
		p.add(model.StepFolders, model.Action{Op: model.OpMkdir, Path: n.f.Dir, Mode: "0700",
			Note: n.label, Pre: []model.Precondition{preAbsent(n.f.Dir)}})
	}
	return nil
}

// --- preconditions ------------------------------------------------------------------------

func preAbsent(path string) model.Precondition {
	return model.Precondition{Path: path, State: model.PathState{Type: model.TypeAbsent}}
}

func preType(path string, t model.PathType) model.Precondition {
	return model.Precondition{Path: path, State: model.PathState{Type: t}}
}

// preItem is the precondition for an item as detect saw it; ok is false for states that
// cannot be expressed (other).
func preItem(path string, it model.ItemState) (model.Precondition, bool) {
	switch it.State {
	case model.ItemAbsent, "":
		return preAbsent(path), true
	case model.ItemFile:
		return model.Precondition{Path: path, State: model.PathState{Type: model.TypeFile, Size: it.Size, SHA256: it.SHA256}}, true
	case model.ItemDir:
		return preType(path, model.TypeDir), true
	case model.ItemLinked, model.ItemElsewhere:
		return model.Precondition{Path: path, State: model.PathState{Type: model.TypeSymlink, Target: it.LinkRaw}}, true
	}
	return model.Precondition{}, false
}

func pres(ps ...model.Precondition) []model.Precondition { return ps }

func jstr(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// --- finishing ----------------------------------------------------------------------------

func (p *planner) finish() model.Plan {
	out := p.out
	out.Actions = []model.Action{}
	for _, step := range model.StepOrder {
		out.Actions = append(out.Actions, p.steps[step]...)
	}
	for i := range out.Actions {
		out.Actions[i].ID = fmt.Sprintf("a%d", i+1)
	}
	if out.Keep == nil {
		out.Keep = []model.Keep{}
	}
	if out.Conflicts == nil {
		out.Conflicts = []model.Conflict{}
	}
	if out.Needs == nil {
		out.Needs = []model.Need{}
	}
	out.Summary = summarize(out.Actions)
	out.PlanID = PlanID(out.Actions, out.Conflicts)
	return out
}

// PlanID is "p_" + the first 12 hex characters of SHA-256 over the canonical JSON (keys
// sorted, no whitespace) of actions and conflicts, preconditions included.
func PlanID(actions []model.Action, conflicts []model.Conflict) string {
	if actions == nil {
		actions = []model.Action{}
	}
	if conflicts == nil {
		conflicts = []model.Conflict{}
	}
	raw, _ := json.Marshal(struct {
		Actions   []model.Action   `json:"actions"`
		Conflicts []model.Conflict `json:"conflicts"`
	}{actions, conflicts})
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	_ = dec.Decode(&v)
	canon, _ := json.Marshal(v) // map keys are sorted by encoding/json
	sum := sha256.Sum256(canon)
	return "p_" + hex.EncodeToString(sum[:])[:12]
}

// summarize counts changes (every action but logins), the distinct existing paths saved in
// the backup set, and links changed (an existing link replaced or re-pointed: never planned).
func summarize(actions []model.Action) model.Summary {
	var s model.Summary
	saved := map[string]bool{}
	exists := func(a model.Action, path string) bool {
		for _, pre := range a.Pre {
			if pre.Path == path {
				return pre.State.Type == model.TypeFile || pre.State.Type == model.TypeDir
			}
		}
		return false
	}
	linkAt := func(a model.Action, path string) bool {
		for _, pre := range a.Pre {
			if pre.Path == path {
				return pre.State.Type == model.TypeSymlink || pre.State.Type == model.TypeJunction
			}
		}
		return false
	}
	for _, a := range actions {
		if a.Op != model.OpLogin {
			s.Changes++
		}
		var paths []string
		switch a.Op {
		case model.OpReplaceWithLink, model.OpMoveToBackup, model.OpConfigSet, model.OpWriteFile, model.OpWriteTree:
			paths = []string{a.Path}
		case model.OpMergeLines:
			paths = []string{a.Path, a.From}
		case model.OpMove:
			paths = []string{a.To}
		case model.OpJSONSet, model.OpJSONDelete:
			paths = []string{a.File}
		case model.OpRun:
			paths = a.Touches
		case model.OpMCPSync:
			paths = a.Files
		}
		for _, path := range paths {
			if path != "" && exists(a, path) {
				saved[path] = true
			}
		}
		switch a.Op {
		case model.OpReplaceWithLink, model.OpMove:
			for _, pre := range a.Pre {
				if linkAt(a, pre.Path) && (pre.Path == a.Path || pre.Path == a.To) {
					s.LinksChanged++
				}
			}
		}
	}
	s.BackedUp = len(saved)
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// defaultLabel is the label of a folder with none: its name without the dot ("claude-2"),
// normalised to the label rule (lower case, anything else becomes "-", at most 24
// characters) so a folder such as "~/.Claude Work" still yields a valid label.
func defaultLabel(dir string) string {
	name := strings.ToLower(strings.TrimPrefix(filepath.Base(dir), "."))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	l := strings.TrimLeft(b.String(), "-")
	if len(l) > 24 {
		l = l[:24]
	}
	if !model.ValidLabel(l) {
		return "account"
	}
	return l
}
