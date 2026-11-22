package account

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/claudecli"
	"github.com/mraj602/watchyard/internal/setup/model"
)

// testSharer implements Link with real symlinks and copies; everything else is unimplemented.
type testSharer struct {
	model.UnimplementedSharer
	failOn string
	links  []string
}

func (s *testSharer) Link(path, target, kind string) error {
	if filepath.Base(path) == s.failOn {
		return errors.New("boom")
	}
	s.links = append(s.links, filepath.Base(path)+":"+kind)
	if kind == model.LinkCopy {
		tmp := path + ".tmp"
		if err := copyAny(target, tmp); err != nil {
			return err
		}
		_ = os.RemoveAll(path)
		return os.Rename(tmp, path)
	}
	return os.Symlink(target, path)
}

// Unshare replaces the link at path by a copy of its target (account.Unsharer).
func (s *testSharer) Unshare(path string) error {
	if filepath.Base(path) == s.failOn {
		return errors.New("boom")
	}
	s.links = append(s.links, filepath.Base(path)+":unshare")
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := copyAny(real, tmp); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func copyAny(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	ents, _ := os.ReadDir(src)
	for _, e := range ents {
		if err := copyAny(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func skipWin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
}

func touch(t *testing.T, p string, mode os.FileMode, dir bool) {
	t.Helper()
	if dir {
		if err := os.MkdirAll(p, mode); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), mode); err != nil {
		t.Fatal(err)
	}
}

func TestNextFolder(t *testing.T) {
	home := "/h"
	cases := []struct {
		names []string
		want  string
	}{
		{nil, "/h/.claude-2"},
		{[]string{".claude", ".claude.json"}, "/h/.claude-2"},
		{[]string{".claude-2"}, "/h/.claude-3"},
		{[]string{".claude-2", ".claude-3", ".claude-5"}, "/h/.claude-4"},
		{[]string{".claude-2.bak"}, "/h/.claude-3"},   // any .claude-N* entry blocks N
		{[]string{".claude-2-old"}, "/h/.claude-3"},   //
		{[]string{".claude-20"}, "/h/.claude-2"},      // 20 does not block 2
		{[]string{".claude-api-env"}, "/h/.claude-2"}, // not a number
		{[]string{".claude-1", ".claude-2"}, "/h/.claude-3"},
	}
	for _, c := range cases {
		if got := NextFolderFrom(home, c.names); got != filepath.FromSlash(c.want) {
			t.Errorf("%v: got %s want %s", c.names, got, c.want)
		}
	}
	dir := t.TempDir()
	touch(t, filepath.Join(dir, ".claude-2"), 0o644, false) // a file blocks too
	got, err := NextFolder(dir)
	if err != nil || got != filepath.Join(dir, ".claude-3") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestLabelsAndColours(t *testing.T) {
	for _, ok := range []string{"work", "a", "r2-d2", "0x"} {
		if ValidateLabel(ok) != nil {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "Work", "-x", "a b", strings.Repeat("a", 25), "é"} {
		if !errors.Is(ValidateLabel(bad), ErrBadLabel) {
			t.Errorf("%q accepted", bad)
		}
	}
	l, c, err := ParseLabelColour("work:teal")
	if err != nil || l != "work" || c != "#14b8a6" {
		t.Fatal(l, c, err)
	}
	if _, c, _ = ParseLabelColour("work:#ABCDEF"); c != "#abcdef" {
		t.Fatal(c)
	}
	if _, c, err = ParseLabelColour("work"); err != nil || c != "" {
		t.Fatal(c, err)
	}
	if _, _, err = ParseLabelColour("work:chartreuse"); !errors.Is(err, ErrBadColour) {
		t.Fatal(err)
	}
	if _, _, err = ParseLabelColour("Bad:teal"); !errors.Is(err, ErrBadLabel) {
		t.Fatal(err)
	}
	if NextColour(nil) != "#14b8a6" || NextColour([]string{"#14B8A6"}) != "#f59e0b" {
		t.Fatal("palette order")
	}
	var all []string
	for _, p := range model.Palette {
		all = append(all, p.Hex)
	}
	if NextColour(all) != "#14b8a6" || NextColour(append(all, "#14b8a6")) != "#f59e0b" {
		t.Fatal("palette exhausted")
	}
}

func TestSlotInfoExtra(t *testing.T) {
	x, err := SlotInfoExtra("work", "teal", "protected")
	if err != nil || string(x["label"]) != `"work"` || string(x["colour"]) != `"#14b8a6"` || string(x["mode"]) != `"protected"` {
		t.Fatalf("%v %v", x, err)
	}
	if x, _ = SlotInfoExtra("", "", ""); len(x) != 0 {
		t.Fatal(x)
	}
	if x, _ = SlotInfoExtra("", "", "normal"); len(x) != 0 {
		t.Fatal("a mode other than protected or aggressive is sent", x)
	}
	if _, err = SlotInfoExtra("Bad", "", ""); err == nil {
		t.Fatal("bad label")
	}
	if _, err = SlotInfoExtra("ok", "nope", ""); err == nil {
		t.Fatal("bad colour")
	}
}

func TestCreate(t *testing.T) {
	skipWin(t)
	home := t.TempDir()
	primary := filepath.Join(home, ".claude")
	touch(t, filepath.Join(primary, "settings.json"), 0o644, false)
	touch(t, filepath.Join(primary, "skills"), 0o755, true)
	touch(t, filepath.Join(home, ".claude-2"), 0o755, true)
	sh := &testSharer{}
	m := &Manager{Home: home, Sharer: sh}
	res, err := m.Create(CreateRequest{Links: []Link{
		{Name: "settings.json", Target: filepath.Join(primary, "settings.json")},
		{Name: "skills", Target: filepath.Join(primary, "skills"), Kind: model.LinkSymlink},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dir != filepath.Join(home, ".claude-3") {
		t.Fatalf("dir %s", res.Dir)
	}
	fi, _ := os.Stat(res.Dir)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	for _, n := range []string{"settings.json", "skills"} {
		if l, err := os.Readlink(filepath.Join(res.Dir, n)); err != nil || l != filepath.Join(primary, n) {
			t.Fatalf("%s: %q %v", n, l, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(res.Dir, ".claude.json")); err == nil {
		t.Fatal("must not create .claude.json")
	}
	if _, err := os.Lstat(filepath.Join(res.Dir, ".credentials.json")); err == nil {
		t.Fatal("must not create credentials")
	}
	// existing folder is refused
	if _, err := m.Create(CreateRequest{Dir: res.Dir}); err == nil {
		t.Fatal("existing folder accepted")
	}
	// never-shared and unsafe names are refused before anything is made
	for _, bad := range []string{".credentials.json", ".claude.json", "sessions", "../x", "a/b", "x.lock", ""} {
		dir := filepath.Join(home, ".claude-9")
		if _, err := m.Create(CreateRequest{Dir: dir, Links: []Link{{Name: bad, Target: primary}}}); err == nil {
			t.Errorf("%q accepted", bad)
		}
		if _, err := os.Lstat(dir); err == nil {
			t.Errorf("%q: folder left behind", bad)
		}
	}
}

func TestCreateRollsBack(t *testing.T) {
	skipWin(t)
	home := t.TempDir()
	primary := filepath.Join(home, ".claude")
	touch(t, filepath.Join(primary, "settings.json"), 0o644, false)
	touch(t, filepath.Join(primary, "skills"), 0o755, true)
	m := &Manager{Home: home, Sharer: &testSharer{failOn: "skills"}}
	_, err := m.Create(CreateRequest{Links: []Link{
		{Name: "settings.json", Target: filepath.Join(primary, "settings.json")},
		{Name: "skills", Target: filepath.Join(primary, "skills")},
	}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude-2")); err == nil {
		t.Fatal("folder not rolled back")
	}
	if _, err := os.Stat(filepath.Join(primary, "settings.json")); err != nil {
		t.Fatal("rollback touched the target")
	}
}

func TestLogin(t *testing.T) {
	fake := claudecli.NewFake()
	m := &Manager{Runner: fake}
	f := claudecli.Folder{Dir: "/h/.claude-4"}
	var term, out bytes.Buffer
	res, err := m.Login(context.Background(), LoginRequest{
		Folder: f, Display: "~/.claude-4", Label: "research",
		Peers: []Peer{{Folder: "~/.claude-2", Email: "Me@Work.example"}, {Folder: "~/.claude-3", Email: "other@x.example"}},
	}, claudecli.Stdio{In: strings.NewReader("me@work.example:Team\n"), Out: &term, Err: &term}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Status.LoggedIn || res.Status.Email != "me@work.example" {
		t.Fatalf("%+v", res)
	}
	o := out.String()
	for _, want := range []string{"Logging in ~/.claude-4 (research). Claude Code will open a link", "~/.claude-4 is now me@work.example", "also used by ~/.claude-2"} {
		if !strings.Contains(o, want) {
			t.Errorf("output lacks %q:\n%s", want, o)
		}
	}
	if strings.Contains(o, "~/.claude-3") {
		t.Error("unrelated peer mentioned")
	}
	if !strings.Contains(term.String(), "Paste code") {
		t.Error("terminal not handed over")
	}
	// calls: login then status, both as the folder; no other arguments exist in the interface
	calls := fake.Calls()
	if len(calls) != 2 || calls[0].Method != "AuthLogin" || calls[1].Method != "AuthStatus" || calls[0].Folder != f {
		t.Fatalf("%+v", calls)
	}
}

func TestLoginFailures(t *testing.T) {
	fake := claudecli.NewFake()
	m := &Manager{Runner: fake}
	f := claudecli.Folder{Dir: "/h/.claude-4"}
	fake.FailOn("AuthLogin", errors.New("cancelled"))
	if _, err := m.Login(context.Background(), LoginRequest{Folder: f, Display: "~/.claude-4"}, claudecli.Stdio{}, nil); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("got %v", err)
	}
	fake.FailOn("AuthLogin", nil)
	fake.FailOn("AuthStatus", errors.New("no status"))
	if _, err := m.Login(context.Background(), LoginRequest{Folder: f, Display: "~/.claude-4"}, claudecli.Stdio{In: strings.NewReader("a@b.c\n")}, nil); err == nil {
		t.Fatal("status failure ignored")
	}
	if got := NeedsCommand("~/.claude-4"); got != "wy account login ~/.claude-4" {
		t.Fatal(got)
	}
}

func TestRemoveUnshareAndLaunchers(t *testing.T) {
	skipWin(t)
	home := t.TempDir()
	primary := filepath.Join(home, ".claude")
	touch(t, filepath.Join(primary, "skills", "a", "SKILL.md"), 0o644, false)
	touch(t, filepath.Join(primary, "CLAUDE.md"), 0o644, false)
	dir := filepath.Join(home, ".claude-2")
	touch(t, dir, 0o700, true)
	for _, n := range []string{"skills", "CLAUDE.md"} {
		if err := os.Symlink(filepath.Join(primary, n), filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	touch(t, filepath.Join(dir, "rules"), 0o755, true) // a real dir in the list: skipped
	touch(t, filepath.Join(dir, ".credentials.json"), 0o600, false)

	ours := filepath.Join(home, "bin", "claude-2")
	changed := filepath.Join(home, "bin", "claude-work")
	touch(t, ours, 0o755, false)
	touch(t, changed, 0o755, false)
	h := sha256.Sum256([]byte("x"))
	good := hex.EncodeToString(h[:])

	sizes, _ := UnshareSizes(dir, []string{"skills", "CLAUDE.md", "rules"})
	if sizes["skills"] != 1 || sizes["CLAUDE.md"] != 1 || len(sizes) != 2 {
		t.Fatalf("sizes %v", sizes)
	}

	m := &Manager{Home: home, Sharer: &testSharer{}}
	res, err := m.Remove(RemoveRequest{
		Dir: dir, Unshare: true, Items: []string{"skills", "CLAUDE.md", "rules"},
		Launchers: []LauncherFile{{ours, good}, {changed, "deadbeef"}, {filepath.Join(home, "bin", "gone"), good}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Unshared, ",") != "skills,CLAUDE.md" {
		t.Fatalf("%+v", res)
	}
	for _, n := range []string{"skills", "CLAUDE.md"} {
		fi, err := os.Lstat(filepath.Join(dir, n))
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s still a link: %v", n, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "skills", "a", "SKILL.md")); err != nil || string(b) != "x" {
		t.Fatal("copy content")
	}
	if _, err := os.Stat(filepath.Join(primary, "skills", "a", "SKILL.md")); err != nil {
		t.Fatal("primary touched")
	}
	if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); err != nil {
		t.Fatal("login touched")
	}
	if _, err := os.Stat(ours); err == nil {
		t.Fatal("our launcher not removed")
	}
	if _, err := os.Stat(changed); err != nil {
		t.Fatal("the person's launcher was removed")
	}
	if len(res.LaunchersRemoved) != 1 || len(res.LaunchersKept) != 1 || res.LaunchersKept[0] != changed {
		t.Fatalf("%+v", res)
	}
	// without --unshare, links stay
	if err := os.Remove(filepath.Join(dir, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(primary, "CLAUDE.md"), filepath.Join(dir, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if res, err = m.Remove(RemoveRequest{Dir: dir, Items: []string{"CLAUDE.md"}}); err != nil || len(res.Unshared) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if fi, _ := os.Lstat(filepath.Join(dir, "CLAUDE.md")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("link removed without --unshare")
	}
}
