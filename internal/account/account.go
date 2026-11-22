// Package account is the folder side of `wy account` and the account step of `wy setup`
// (setup.md 5.6, 8): finding the next free `~/.claude-N`, creating it with the enabled items
// linked through a model.Sharer, labels and colours, the login handoff to
// `claude auth login --claudeai`, the `label`/`colour` fields of `slot_info` (record.md 4.7),
// and `remove [--unshare]`.
//
// Rules from setup.md section 0 that this package keeps: it never reads, writes or moves
// `.credentials.json` or any state file, never creates `.claude.json` (Claude Code makes it on
// first run), never deletes anything from a Claude folder, and never passes `--email`,
// `--sso` or `--console` to claude. `--relay` (8.2) is not implemented: it is a stretch task
// behind a build tag until proven, and the `needs` command is the fallback.
//
// Depends on: claudecli, model.
package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/mraj602/watchyard/internal/claudecli"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// Manager does the folder work. Sharer is internal/share's implementation in production.
type Manager struct {
	Home   string // absolute home folder
	Runner claudecli.Runner
	Sharer model.Sharer
}

// ---- next free folder (5.6) ----------------------------------------------------------------------

var folderNumRE = regexp.MustCompile(`^\.claude-(\d+)`)

// NextFolder returns the next free folder name: `<home>/.claude-N` with the smallest N >= 2
// such that neither the path nor any `.claude-N*` entry exists (a file named `.claude-N`
// blocks N; `.claude-20` does not block 2). Entries are read with os.ReadDir.
func NextFolder(home string) (string, error) {
	ents, err := os.ReadDir(home)
	if err != nil {
		return "", fmt.Errorf("account: %w", err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return NextFolderFrom(home, names), nil
}

// NextFolderFrom is NextFolder over a list of entry names (also folders planned earlier in
// the same run).
func NextFolderFrom(home string, names []string) string {
	taken := map[int]bool{}
	for _, n := range names {
		m := folderNumRE.FindStringSubmatch(n)
		if m == nil {
			continue
		}
		// ".claude-2" and ".claude-2.bak" or ".claude-2-old" block 2; ".claude-20" is 20.
		taken[atoi(m[1])] = true
	}
	for n := 2; ; n++ {
		if !taken[n] {
			return filepath.Join(home, ".claude-"+strconv.Itoa(n))
		}
	}
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

// ---- labels and colours ----------------------------------------------------------------------------

// ErrBadLabel is returned for a label that does not match `^[a-z0-9][a-z0-9-]{0,23}$`.
var ErrBadLabel = errors.New("account: invalid label (use a-z, 0-9 and -, up to 24 characters, starting with a letter or digit)")

// ErrBadColour is returned for a colour that is neither a palette name nor #rrggbb.
var ErrBadColour = errors.New("account: invalid colour (a palette name or #rrggbb)")

// ValidateLabel checks a label.
func ValidateLabel(label string) error {
	if !model.ValidLabel(label) {
		return ErrBadLabel
	}
	return nil
}

// ParseLabelColour splits "work", "work:teal" or "work:#14b8a6" into a validated label and a
// "#rrggbb" colour ("" when none was given).
func ParseLabelColour(s string) (label, colour string, err error) {
	label, c, has := strings.Cut(s, ":")
	if err := ValidateLabel(label); err != nil {
		return "", "", err
	}
	if !has || c == "" {
		return label, "", nil
	}
	hex, ok := model.ResolveColour(c)
	if !ok {
		return "", "", ErrBadColour
	}
	return label, hex, nil
}

// NextColour returns the first palette colour not in used ("#rrggbb", case-insensitive); when
// the palette is used up it starts over from the least used entry.
func NextColour(used []string) string {
	count := map[string]int{}
	for _, u := range used {
		count[strings.ToLower(u)]++
	}
	best, bestN := "", -1
	for _, c := range model.Palette {
		n := count[c.Hex]
		if n == 0 {
			return c.Hex
		}
		if bestN < 0 || n < bestN {
			best, bestN = c.Hex, n
		}
	}
	return best
}

// SlotInfoExtra returns the optional `label`, `colour` and `mode` fields of a slot_info
// payload (record.md 4.7), validated; empty values are left out, and so is a mode other than
// protected or aggressive (the hub would ignore it). It is the same type as record.Extra, for
// SlotInfoPayload.Extra.
func SlotInfoExtra(label, colour, mode string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if mode == "protected" || mode == "aggressive" {
		b, _ := json.Marshal(mode)
		out["mode"] = b
	}
	if label != "" {
		if err := ValidateLabel(label); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(label)
		out["label"] = b
	}
	if colour != "" {
		hex, ok := model.ResolveColour(colour)
		if !ok {
			return nil, ErrBadColour
		}
		b, _ := json.Marshal(hex)
		out["colour"] = b
	}
	return out, nil
}

// ---- create (5.6) ----------------------------------------------------------------------------------

// Link is one item to link into a new folder.
type Link struct {
	Name   string // item name relative to the folder, e.g. "skills"
	Target string // absolute target (the primary's item)
	Kind   string // model.LinkSymlink, LinkJunction or LinkCopy
}

// CreateRequest asks for a new folder. Dir empty means NextFolder(Home).
type CreateRequest struct {
	Dir   string
	Links []Link
}

// CreateResult lists what Create made.
type CreateResult struct {
	Dir    string
	Linked []string // paths of the links made
}

func safeItem(name string) error {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("account: bad item name %q", name)
	}
	for _, n := range model.NeverShared {
		if name == n {
			return fmt.Errorf("account: %s is never shared", name)
		}
	}
	if strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("account: %s is never shared", name)
	}
	return nil
}

// Create makes the folder (mode 0700; it must not exist) and links the items through the
// Sharer. It creates no `.claude.json`. If a link fails, the links made so far and the
// folder are removed again (only if nothing else is in it) and the error is returned.
func (m *Manager) Create(req CreateRequest) (CreateResult, error) {
	var res CreateResult
	dir := req.Dir
	if dir == "" {
		d, err := NextFolder(m.Home)
		if err != nil {
			return res, err
		}
		dir = d
	}
	if !filepath.IsAbs(dir) {
		return res, fmt.Errorf("account: folder %q is not absolute", dir)
	}
	for _, l := range req.Links {
		if err := safeItem(l.Name); err != nil {
			return res, err
		}
		if !filepath.IsAbs(l.Target) {
			return res, fmt.Errorf("account: link target %q is not absolute", l.Target)
		}
	}
	if _, err := os.Lstat(dir); err == nil {
		return res, fmt.Errorf("account: %s already exists", dir)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return res, fmt.Errorf("account: %w", err)
	}
	res.Dir = dir
	rollback := func(err error) (CreateResult, error) {
		for i := len(res.Linked) - 1; i >= 0; i-- {
			_ = os.Remove(res.Linked[i])
		}
		_ = os.Remove(dir) // fails (and keeps it) if anything else is inside
		return CreateResult{}, err
	}
	for _, l := range req.Links {
		p := filepath.Join(dir, l.Name)
		kind := l.Kind
		if kind == "" {
			kind = model.LinkSymlink
		}
		if err := m.Sharer.Link(p, l.Target, kind); err != nil {
			return rollback(fmt.Errorf("account: link %s: %w", l.Name, err))
		}
		res.Linked = append(res.Linked, p)
	}
	return res, nil
}

// ---- login handoff (8.2) ---------------------------------------------------------------------------

// Peer is another folder's login, to tell the person when two folders share one seat.
type Peer struct {
	Folder string // "~/.claude-2"
	Email  string
	Org    string
}

// LoginRequest asks for one login.
type LoginRequest struct {
	Folder  claudecli.Folder
	Display string // "~/.claude-4", for messages
	Label   string // optional, for the first line
	Peers   []Peer // other folders and their logins
}

// LoginResult is the confirmed state after the login command exited.
type LoginResult struct {
	Status claudecli.AuthStatus
	Same   []string // peers that now have the same login (allowed: two folders, one seat)
}

// ErrLoginIncomplete means `claude auth login` exited but `auth status` says logged out.
var ErrLoginIncomplete = errors.New("account: login did not complete")

// NeedsCommand is the exact command for a login that has to happen in a terminal (the `needs`
// list of `--json` output).
func NeedsCommand(display string) string { return "wy account login " + display }

// Login runs `claude auth login --claudeai` as the folder with the terminal handed over
// (stdin/stdout/stderr inherited), then confirms with `claude auth status --json`. out gets
// the one line before and the confirmation after (nil: discarded). It never passes --email,
// --sso or --console.
func (m *Manager) Login(ctx context.Context, req LoginRequest, tty claudecli.Stdio, out io.Writer) (LoginResult, error) {
	if out == nil {
		out = io.Discard
	}
	who := req.Display
	if req.Label != "" {
		who += " (" + req.Label + ")"
	}
	fmt.Fprintf(out, "Logging in %s. Claude Code will open a link; on a server, open it on any device and paste the code here.\n", who)
	if err := m.Runner.AuthLogin(ctx, req.Folder, tty); err != nil {
		return LoginResult{}, fmt.Errorf("login %s: %w", req.Display, err)
	}
	st, err := m.Runner.AuthStatus(ctx, req.Folder)
	if err != nil {
		return LoginResult{}, fmt.Errorf("confirm login %s: %w", req.Display, err)
	}
	if !st.LoggedIn {
		return LoginResult{Status: st}, fmt.Errorf("%w: %s is still logged out", ErrLoginIncomplete, req.Display)
	}
	res := LoginResult{Status: st}
	line := fmt.Sprintf("%s is now %s", req.Display, st.Email)
	if st.Plan != "" {
		line += " (" + titleCase(st.Plan) + ")"
	}
	fmt.Fprintln(out, line)
	for _, p := range req.Peers {
		if p.Folder != req.Display && p.Email != "" && strings.EqualFold(p.Email, st.Email) && (p.Org == "" || st.Org == "" || p.Org == st.Org) {
			res.Same = append(res.Same, p.Folder)
		}
	}
	if len(res.Same) > 0 {
		fmt.Fprintf(out, "That login is also used by %s (two folders, one seat: allowed).\n", strings.Join(res.Same, ", "))
	}
	return res, nil
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ---- remove (8.1) ----------------------------------------------------------------------------------

// LauncherFile is a launcher `wy` wrote, with the SHA-256 of the content it wrote. A launcher
// whose content differs is the person's now and is left alone.
type LauncherFile struct {
	Path   string
	SHA256 string
}

// RemoveRequest asks for `wy account remove`.
type RemoveRequest struct {
	Dir       string // the folder; it, its login and its links stay
	Unshare   bool   // convert each link into an independent copy
	Items     []string
	Launchers []LauncherFile
	// Sum computes the SHA-256 of a launcher file; nil uses the real file system (tests).
	Sum func(path string) (string, error)
}

// RemoveResult reports what Remove did. Label, colour and MCP-sync-set removal are config
// edits made by the caller (internal/config and the share set); Remove only does file work.
type RemoveResult struct {
	Unshared         []string // items converted from a link into a copy
	LaunchersRemoved []string
	LaunchersKept    []string // changed by the person
}

// UnshareSizes returns, per link among items in dir, the size in bytes of what a copy would
// hold (the plan shows sizes). Items that are not links are skipped.
func UnshareSizes(dir string, items []string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, it := range items {
		p := filepath.Join(dir, it)
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		var n int64
		_ = filepath.WalkDir(real, func(_ string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if i, e := d.Info(); e == nil {
					n += i.Size()
				}
			}
			return nil
		})
		out[it] = n
	}
	return out, nil
}

// Unsharer turns a link into an independent copy of its resolved target, never absent in
// between. internal/share's Sharer implements it. Unlike Link with LinkCopy it does not
// register a synced copy: the copy is the folder's own, and `wy tick` must never copy its
// edits over the primary.
type Unsharer interface {
	Unshare(path string) error
}

// Remove stops managing a folder: with Unshare it converts each listed link into an
// independent copy (through the Sharer's Unshare), and it deletes the launchers `wy` wrote
// that are unchanged. Nothing is ever deleted from the folder.
func (m *Manager) Remove(req RemoveRequest) (RemoveResult, error) {
	var res RemoveResult
	if req.Unshare {
		u, ok := m.Sharer.(Unsharer)
		if !ok {
			return res, errors.New("account: this sharer cannot unshare")
		}
		for _, it := range req.Items {
			if err := safeItem(it); err != nil {
				return res, err
			}
			p := filepath.Join(req.Dir, it)
			fi, err := os.Lstat(p)
			if err != nil || fi.Mode()&os.ModeSymlink == 0 {
				continue // not a link: nothing to unshare
			}
			if _, err := filepath.EvalSymlinks(p); err != nil {
				return res, fmt.Errorf("account: %s is a dangling link: %w", it, err)
			}
			if err := u.Unshare(p); err != nil {
				return res, fmt.Errorf("account: unshare %s: %w", it, err)
			}
			res.Unshared = append(res.Unshared, it)
		}
	}
	sum := req.Sum
	if sum == nil {
		sum = fileSum
	}
	for _, l := range req.Launchers {
		got, err := sum(l.Path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			return res, err
		case got != l.SHA256:
			res.LaunchersKept = append(res.LaunchersKept, l.Path)
			continue
		}
		if err := os.Remove(l.Path); err != nil {
			return res, err
		}
		res.LaunchersRemoved = append(res.LaunchersRemoved, l.Path)
	}
	return res, nil
}
