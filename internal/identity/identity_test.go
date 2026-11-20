package identity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const fixtures = "../../testdata/auth"

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func write(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// recFS wraps OSFS and records every path that is opened, statted or listed.
type recFS struct {
	OSFS
	mu    sync.Mutex
	opens []string
	stats []string
	dirs  []string
}

func (r *recFS) Open(name string) (io.ReadCloser, error) {
	r.mu.Lock()
	r.opens = append(r.opens, name)
	r.mu.Unlock()
	return r.OSFS.Open(name)
}
func (r *recFS) Stat(name string) (fs.FileInfo, error) {
	r.mu.Lock()
	r.stats = append(r.stats, name)
	r.mu.Unlock()
	return r.OSFS.Stat(name)
}
func (r *recFS) ReadDir(name string) ([]fs.DirEntry, error) {
	r.mu.Lock()
	r.dirs = append(r.dirs, name)
	r.mu.Unlock()
	return r.OSFS.ReadDir(name)
}

// fakeHome builds a home folder like the laptop in testdata/README.md: slots 1-3 logged
// in, 4 logged out, plus decoys and credential files that must never be read.
func fakeHome(t testing.TB) string {
	home := t.TempDir()
	// slot 1: ~/.claude with its state file at ~/.claude.json
	write(t, filepath.Join(home, ".claude.json"), fixture(t, "oauth-1.json"))
	write(t, filepath.Join(home, ".claude", "settings.json"), []byte("{}"))
	write(t, filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"SECRET"}}`))
	write(t, filepath.Join(home, ".claude", "sessions", "s1.key"), []byte("SECRET"))
	write(t, filepath.Join(home, ".claude", "sessions", "s1.json"), []byte("{}"))
	write(t, filepath.Join(home, ".claude", "daemon", "sock"), []byte("SECRET"))
	for _, n := range []string{"2", "3"} {
		d := filepath.Join(home, ".claude-"+n)
		write(t, filepath.Join(d, ".claude.json"), fixture(t, "oauth-"+n+".json"))
		write(t, filepath.Join(d, ".credentials.json"), []byte("SECRET"))
		write(t, filepath.Join(d, "sessions", "x.key"), []byte("SECRET"))
	}
	write(t, filepath.Join(home, ".claude-4", "settings.json"), []byte("{}")) // logged out, no oauthAccount
	write(t, filepath.Join(home, ".claude-4", ".claude.json"), []byte(`{"numStartups":3}`))
	if err := os.MkdirAll(filepath.Join(home, ".claude-9"), 0o700); err != nil { // empty: not a slot
		t.Fatal(err)
	}
	write(t, filepath.Join(home, ".claudeX", "settings.json"), []byte("{}")) // name does not match
	write(t, filepath.Join(home, ".claude-notes"), []byte("a file, not a folder"))
	return home
}

func noEnv(string) string { return "" }

func TestDiscover(t *testing.T) {
	home := fakeHome(t)
	extra := filepath.Join(home, "work", "cc")
	write(t, filepath.Join(extra, "projects", "p.txt"), []byte("x"))
	l := Locator{Home: home, FS: OSFS{}}
	slots, err := l.Discover([]string{"~/work/cc", "~/missing"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range slots {
		names = append(names, s.Name)
	}
	want := []string{"~/.claude", "~/.claude-2", "~/.claude-3", "~/.claude-4", "~/work/cc"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("slots %v want %v", names, want)
	}
	if !slots[0].Default || slots[0].StateFile != filepath.Join(home, ".claude.json") {
		t.Errorf("~/.claude slot: %+v", slots[0])
	}
	if slots[1].Default || slots[1].StateFile != filepath.Join(home, ".claude-2", ".claude.json") {
		t.Errorf("~/.claude-2 slot: %+v", slots[1])
	}
}

func TestDiscoverCLAUDE_CONFIG_DIR(t *testing.T) {
	home := fakeHome(t)
	other := t.TempDir() // outside home, empty: still counts because the user chose it
	l := Locator{Home: home, FS: OSFS{}}
	slots, err := l.Discover(nil, func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return other
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range slots {
		if s.Dir == other {
			found = true
			if s.Name != filepath.ToSlash(other) || s.Default {
				t.Errorf("outside-home slot %+v", s)
			}
		}
	}
	if !found {
		t.Fatalf("CLAUDE_CONFIG_DIR slot missing: %+v", slots)
	}
	// Set to ~/.claude it is the default slot, once.
	slots, _ = l.Discover(nil, func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return filepath.Join(home, ".claude")
		}
		return ""
	})
	n := 0
	for _, s := range slots {
		if s.Name == "~/.claude" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("~/.claude listed %d times", n)
	}
}

func TestCurrentAndDefaultSlotRule(t *testing.T) {
	home := fakeHome(t)
	l := Locator{Home: home, FS: OSFS{}}
	if s := l.Current(noEnv); s.Name != "~/.claude" || !s.Default || s.StateFile != filepath.Join(home, ".claude.json") {
		t.Errorf("unset: %+v", s)
	}
	env := func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return filepath.Join(home, ".claude-2") + "/"
		}
		return ""
	}
	if s := l.Current(env); s.Name != "~/.claude-2" || s.Default || s.StateFile != filepath.Join(home, ".claude-2", ".claude.json") {
		t.Errorf("set: %+v", s)
	}
	// A symlink to ~/.claude is the same slot for the state-file rule, the name stays as chosen.
	link := filepath.Join(home, ".claude-link")
	if err := os.Symlink(filepath.Join(home, ".claude"), link); err != nil {
		t.Skip("no symlinks:", err)
	}
	s := l.SlotFor(link)
	if !s.Default || s.StateFile != filepath.Join(home, ".claude.json") || s.Name != "~/.claude-link" {
		t.Errorf("symlinked slot: %+v", s)
	}
}

func TestTilde(t *testing.T) {
	for _, tc := range []struct{ home, dir, want string }{
		{"/home/u", "/home/u/.claude-2", "~/.claude-2"},
		{"/home/u", "/home/u", "~"},
		{"/home/u", "/home/user/.claude", "/home/user/.claude"},
		{"/home/u", "/opt/cc", "/opt/cc"},
		{"/home/u/", "/home/u/a/b/", "~/a/b"},
	} {
		if got := Tilde(tc.home, tc.dir); got != tc.want {
			t.Errorf("Tilde(%q,%q)=%q want %q", tc.home, tc.dir, got, tc.want)
		}
	}
}

func TestLoginFromFixtures(t *testing.T) {
	home := fakeHome(t)
	state := t.TempDir()
	l := Locator{Home: home, FS: OSFS{}}
	st := &Store{StateDir: state, FS: OSFS{}}
	slots, _ := l.Discover(nil, noEnv)
	want := map[string]Seat{
		"~/.claude":   {Account: "aaaaaaaa-0000-4000-8000-000000000001", Org: "bbbbbbbb-0000-4000-8000-000000000001", Email: "user1@example.com", Src: "claude_json"},
		"~/.claude-2": {Account: "aaaaaaaa-0000-4000-8000-000000000002", Org: "bbbbbbbb-0000-4000-8000-000000000002", Email: "user2@example.com", Src: "claude_json"},
		"~/.claude-3": {Account: "aaaaaaaa-0000-4000-8000-000000000003", Org: "bbbbbbbb-0000-4000-8000-000000000003", Email: "user3@example.com", Src: "claude_json"},
	}
	for _, s := range slots {
		got, err := st.Login(s)
		if err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		w, ok := want[s.Name]
		if !ok {
			if got.HasSeat {
				t.Errorf("%s: logged-out slot has a seat %+v", s.Name, got.Seat)
			}
			continue
		}
		if !got.HasSeat || got.Seat != w {
			t.Errorf("%s: seat %+v want %+v", s.Name, got.Seat, w)
		}
	}
	// The cache files hold uuids and emails only.
	files, _ := filepath.Glob(filepath.Join(state, "identity", "*.json"))
	if len(files) != 4 {
		t.Fatalf("want 4 cache files, got %d", len(files))
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "SECRET") {
			t.Errorf("%s holds a secret", f)
		}
		if !regexp.MustCompile(`/[0-9a-f]{16}\.json$`).MatchString(f) {
			t.Errorf("odd cache name %s", f)
		}
	}
}

func TestOAuthReadsOnlyThatKey(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".claude-2", ".claude.json")
	write(t, state, []byte(`{"mcpServers":{"x":{"env":{"API_KEY":"leak"}}},"primaryApiKey":"leak",
		"oauthAccount":{"accountUuid":"a1","organizationUuid":"o1","emailAddress":"e@x","accessToken":"leak"}}`))
	oa, err := ReadOAuth(OSFS{}, state)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(oa)
	if strings.Contains(string(b), "leak") || oa.AccountUUID != "a1" || oa.OrganizationUUID != "o1" || oa.EmailAddress != "e@x" {
		t.Errorf("oauth = %s", b)
	}
	write(t, state, []byte(`{"oauthAccount":null}`))
	if oa, err := ReadOAuth(OSFS{}, state); err != nil || oa != nil {
		t.Errorf("null: %v %v", oa, err)
	}
	write(t, state, []byte(`{not json`))
	if _, err := ReadOAuth(OSFS{}, state); err == nil {
		t.Error("want error on broken json")
	}
}

func TestCacheKeyedBySizeAndMtime(t *testing.T) {
	home := fakeHome(t)
	rec := &recFS{}
	st := &Store{StateDir: t.TempDir(), FS: rec}
	slot := Locator{Home: home, FS: rec}.SlotFor("~/.claude-2")

	opensOf := func() int {
		n := 0
		for _, o := range rec.opens {
			if o == slot.StateFile {
				n++
			}
		}
		return n
	}
	if _, err := st.Login(slot); err != nil {
		t.Fatal(err)
	}
	if opensOf() != 1 {
		t.Fatalf("first read opens: %d", opensOf())
	}
	for i := 0; i < 5; i++ {
		if _, err := st.Login(slot); err != nil {
			t.Fatal(err)
		}
	}
	if opensOf() != 1 {
		t.Fatalf("cached reads must not reopen the state file, opens: %d", opensOf())
	}
	// Same size, new mtime: reread.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(slot.StateFile, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Login(slot); err != nil || opensOf() != 2 {
		t.Fatalf("mtime change: err=%v opens=%d", err, opensOf())
	}
	// New content (re-login as account 3): different size and mtime, new seat.
	write(t, slot.StateFile, fixture(t, "oauth-3.json"))
	got, err := st.Login(slot)
	if err != nil || got.Seat.Account != "aaaaaaaa-0000-4000-8000-000000000003" || opensOf() != 3 {
		t.Fatalf("size change: %+v err=%v opens=%d", got.Seat, err, opensOf())
	}
	// Logged out: oauthAccount removed.
	write(t, slot.StateFile, []byte(`{}`))
	got, _ = st.Login(slot)
	if got.HasSeat {
		t.Errorf("seat after logout: %+v", got.Seat)
	}
}

func TestMissingStateFileAndReadOnlyState(t *testing.T) {
	home := t.TempDir()
	slot := Locator{Home: home, FS: OSFS{}}.SlotFor("~/.claude-7")
	st := &Store{StateDir: t.TempDir(), FS: OSFS{}}
	if l, err := st.Login(slot); err != nil || l.HasSeat {
		t.Fatalf("missing state file: %+v %v", l, err)
	}
	// An unwritable state folder (a file where the folder should be) still returns the login.
	write(t, slot.StateFile, fixture(t, "oauth-2.json"))
	blocked := filepath.Join(t.TempDir(), "state")
	write(t, blocked, []byte("i am a file"))
	st = &Store{StateDir: blocked, FS: OSFS{}}
	if l, err := st.Login(slot); err != nil || !l.HasSeat {
		t.Fatalf("unwritable cache: %+v %v", l, err)
	}
}

// Never open .credentials.json or sessions/*.key, or anything under daemon/: a recording
// FS sees every call made while discovering slots and reading logins.
func TestNeverTouchesCredentials(t *testing.T) {
	home := fakeHome(t)
	rec := &recFS{}
	l := Locator{Home: home, FS: rec}
	st := &Store{StateDir: t.TempDir(), FS: rec}
	slots, err := l.Discover(nil, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range slots {
		for i := 0; i < 2; i++ {
			if _, err := st.Login(s); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(rec.opens) == 0 {
		t.Fatal("nothing recorded")
	}
	bad := func(p string) bool {
		return strings.HasSuffix(p, ".credentials.json") || strings.HasSuffix(p, ".key") ||
			strings.Contains(p, "/daemon") || strings.Contains(p, "/sessions/")
	}
	for _, group := range [][]string{rec.opens, rec.stats, rec.dirs} {
		for _, p := range group {
			if bad(p) {
				t.Errorf("touched %s", p)
			}
		}
	}
	for _, p := range rec.opens {
		if filepath.Base(p) != ".claude.json" {
			t.Errorf("opened %s: only .claude.json may be opened", p)
		}
	}
}

// The default FS refuses forbidden files even if a caller asks for them.
func TestOSFSGuard(t *testing.T) {
	home := fakeHome(t)
	for _, p := range []string{
		filepath.Join(home, ".claude", ".credentials.json"),
		filepath.Join(home, ".claude-2", "sessions", "x.key"),
		filepath.Join(home, ".claude", "daemon", "sock"),
		// Case-insensitive file systems (macOS, Windows) open these as the files above.
		filepath.Join(home, ".claude", ".Credentials.JSON"),
		filepath.Join(home, ".Claude-2", "Sessions", "x.KEY"),
		filepath.Join(home, ".CLAUDE", "Daemon", "sock"),
	} {
		if _, err := (OSFS{}).Open(p); !errors.Is(err, ErrForbidden) {
			t.Errorf("Open(%s): %v", p, err)
		}
	}
	for _, p := range []string{
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".claude", "sessions", "s1.json"),
		"/home/daemon/work/a.txt",
	} {
		if Forbidden(p) {
			t.Errorf("Forbidden(%s)", p)
		}
	}
}

func TestAuthEnv(t *testing.T) {
	environ := []string{"A=1", "CLAUDE_CONFIG_DIR=/old", "B=2"}
	def := Slot{Dir: "/h/.claude", Default: true}
	if got := AuthEnv(environ, def); !reflect.DeepEqual(got, []string{"A=1", "B=2"}) {
		t.Errorf("default slot env %v", got)
	}
	other := Slot{Dir: "/h/.claude-2"}
	if got := AuthEnv(environ, other); !reflect.DeepEqual(got, []string{"A=1", "B=2", "CLAUDE_CONFIG_DIR=/h/.claude-2"}) {
		t.Errorf("other slot env %v", got)
	}
}

func fakeRunner(t *testing.T, out []byte, err error, called *[]string) Runner {
	return func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		*called = append(*called, name+" "+strings.Join(args, " "))
		if _, ok := ctx.Deadline(); !ok {
			t.Error("no timeout on auth status")
		}
		*called = append(*called, env...)
		return out, err
	}
}

func TestRefreshAuthStatus(t *testing.T) {
	home := fakeHome(t)
	l := Locator{Home: home, FS: OSFS{}}
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	st := &Store{StateDir: t.TempDir(), FS: OSFS{}, Now: func() time.Time { return now }}

	// Slot 1 (~/.claude): CLAUDE_CONFIG_DIR must be absent from the command's environment.
	var called []string
	slot1 := l.SlotFor("~/.claude")
	got, err := st.RefreshAuthStatus(context.Background(), slot1, []string{"CLAUDE_CONFIG_DIR=/x", "K=v"}, fakeRunner(t, fixture(t, "status-1.json"), nil, &called))
	if err != nil {
		t.Fatal(err)
	}
	if called[0] != "claude auth status --json" || strings.Contains(strings.Join(called, "|"), "CLAUDE_CONFIG_DIR") {
		t.Errorf("slot 1 call %v", called)
	}
	if !got.LoggedIn || got.Email != "user1@example.com" || got.OrgID != "bbbbbbbb-0000-4000-8000-000000000001" || got.SubscriptionType != "max" {
		t.Errorf("status %+v", got)
	}
	login, _ := st.Login(slot1)
	if login.Auth != got || !login.AuthStatusAt.Equal(now) || login.Seat.Src != "claude_json" {
		t.Errorf("login after refresh %+v", login)
	}

	// A later oauthAccount reread keeps the stored auth status.
	write(t, slot1.StateFile, fixture(t, "oauth-1.json"))
	later := time.Now().Add(time.Minute)
	os.Chtimes(slot1.StateFile, later, later)
	if login, _ = st.Login(slot1); login.Auth != got {
		t.Errorf("auth status lost on reread: %+v", login.Auth)
	}

	// Slot 2: CLAUDE_CONFIG_DIR is set to the folder.
	called = nil
	slot2 := l.SlotFor("~/.claude-2")
	if _, err := st.RefreshAuthStatus(context.Background(), slot2, nil, fakeRunner(t, fixture(t, "status-2.json"), nil, &called)); err != nil {
		t.Fatal(err)
	}
	if called[1] != "CLAUDE_CONFIG_DIR="+slot2.Dir {
		t.Errorf("slot 2 env %v", called)
	}

	// Logged-out slot: JSON printed with a non-zero exit is still used; no seat results.
	called = nil
	slot4 := l.SlotFor("~/.claude-4")
	s4, err := st.RefreshAuthStatus(context.Background(), slot4, nil, fakeRunner(t, fixture(t, "status-4.json"), errors.New("exit status 1"), &called))
	if err != nil || s4.LoggedIn {
		t.Fatalf("status-4: %+v %v", s4, err)
	}
	if lg, _ := st.Login(slot4); lg.HasSeat {
		t.Errorf("logged-out slot has a seat %+v", lg.Seat)
	}

	// Garbage output with an error: error, cache untouched.
	if _, err := st.RefreshAuthStatus(context.Background(), slot2, nil, fakeRunner(t, []byte("boom"), errors.New("exit 1"), &called)); err == nil {
		t.Error("want error")
	}
}

// With no oauthAccount (macOS keeps the login in the Keychain) the seat comes from auth
// status: account empty, org = orgId, src = auth_status (identity.md section 4).
func TestSeatFromAuthStatusOnly(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude-2", ".claude.json"), []byte(`{}`))
	slot := Locator{Home: home, FS: OSFS{}}.SlotFor("~/.claude-2")
	st := &Store{StateDir: t.TempDir(), FS: OSFS{}}
	var called []string
	if _, err := st.RefreshAuthStatus(context.Background(), slot, nil, fakeRunner(t, fixture(t, "status-2.json"), nil, &called)); err != nil {
		t.Fatal(err)
	}
	l, _ := st.Login(slot)
	want := Seat{Account: "", Org: "bbbbbbbb-0000-4000-8000-000000000002", Email: "user2@example.com", Src: "auth_status"}
	if !l.HasSeat || l.Seat != want {
		t.Errorf("seat %+v", l.Seat)
	}
}

func TestRefreshAsync(t *testing.T) {
	home := fakeHome(t)
	slot := Locator{Home: home, FS: OSFS{}}.SlotFor("~/.claude-3")
	st := &Store{StateDir: t.TempDir(), FS: OSFS{}}
	var called []string
	res := <-st.RefreshAuthStatusAsync(context.Background(), slot, nil, fakeRunner(t, fixture(t, "status-3.json"), nil, &called))
	if res.Err != nil || res.Status.SubscriptionType != "team" {
		t.Errorf("%+v", res)
	}
}

func TestMachineID(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	opt := MachineOptions{
		Now:      func() time.Time { return time.Date(2026, 10, 11, 13, 59, 0, 0, time.UTC) },
		Hostname: func() (string, error) { return "Laptop.Local", nil },
	}
	m, err := LoadOrCreateMachine(state, opt)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^m_[0-9A-HJKMNP-TV-Z]{26}$`).MatchString(m.ID) {
		t.Errorf("id %q", m.ID)
	}
	if m.Name != "laptop" || m.Created != "2026-10-11T13:59:00.000Z" {
		t.Errorf("machine %+v", m)
	}
	// Second call, even with different options, returns the stored machine.
	m2, err := LoadOrCreateMachine(state, MachineOptions{Hostname: func() (string, error) { return "other", nil }})
	if err != nil || m2 != m {
		t.Errorf("second call %+v %v", m2, err)
	}
	if st, _ := os.Stat(filepath.Join(state, "machine.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	// Hostname failure falls back to "machine".
	m3, _ := LoadOrCreateMachine(filepath.Join(t.TempDir(), "s"), MachineOptions{Hostname: func() (string, error) { return "", errors.New("x") }})
	if m3.Name != "machine" {
		t.Errorf("name %q", m3.Name)
	}
	// A corrupt file is an error and is left alone.
	bad := t.TempDir()
	write(t, filepath.Join(bad, "machine.json"), []byte("garbage"))
	if _, err := LoadOrCreateMachine(bad, MachineOptions{}); err == nil {
		t.Error("want error for corrupt machine.json")
	}
	if b, _ := os.ReadFile(filepath.Join(bad, "machine.json")); string(b) != "garbage" {
		t.Error("corrupt machine.json was overwritten")
	}
}

func TestULIDOrderAndShape(t *testing.T) {
	zero := strings.NewReader(strings.Repeat("\x00", 10))
	a, _ := newULID(zero, time.UnixMilli(1000))
	b, _ := newULID(strings.NewReader(strings.Repeat("\x00", 10)), time.UnixMilli(1001))
	if len(a) != 26 || a >= b {
		t.Errorf("ulids %s %s", a, b)
	}
	if !strings.HasSuffix(a, strings.Repeat("0", 16)) {
		t.Errorf("random part of zero input should be zeros: %s", a)
	}
	full, _ := newULID(strings.NewReader(strings.Repeat("\xff", 10)), time.UnixMilli(1<<48-1))
	if full != "7ZZZZZZZZZZZZZZZZZZZZZZZZZ" {
		t.Errorf("max ulid %s", full)
	}
}

// helper process for TestMachineIDConcurrentProcesses: creates the id and prints it.
func TestMachineHelperProcess(t *testing.T) {
	dir := os.Getenv("WY_TEST_MACHINE_STATE")
	if dir == "" {
		t.Skip("helper process")
	}
	m, err := LoadOrCreateMachine(dir, MachineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("ID=" + m.ID + "\n")
}

// Many processes and goroutines racing on a fresh state folder must agree on one id.
func TestMachineIDConcurrent(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	const procs, routines = 12, 16
	ids := make(chan string, procs+routines)
	var wg sync.WaitGroup
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestMachineHelperProcess$")
			cmd.Env = append(os.Environ(), "WY_TEST_MACHINE_STATE="+state)
			out, err := cmd.Output()
			if err != nil {
				t.Errorf("helper: %v", err)
				return
			}
			for _, line := range strings.Split(string(out), "\n") {
				if id, ok := strings.CutPrefix(line, "ID="); ok {
					ids <- id
				}
			}
		}()
	}
	for i := 0; i < routines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := LoadOrCreateMachine(state, MachineOptions{})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- m.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]int{}
	for id := range ids {
		seen[id]++
	}
	if len(seen) != 1 {
		t.Fatalf("got %d different machine ids: %v", len(seen), seen)
	}
	for _, n := range seen {
		if n != procs+routines {
			t.Errorf("only %d of %d callers reported", n, procs+routines)
		}
	}
}

func cachedLoginSetup(t testing.TB) (*Store, Slot) {
	home := fakeHome(t)
	st := &Store{StateDir: t.TempDir(), FS: OSFS{}}
	slot := Locator{Home: home, FS: OSFS{}}.SlotFor("~/.claude")
	// Make the state file big, like a real one with many projects.
	big := map[string]any{"projects": strings.Repeat("x", 2<<20)}
	var oa map[string]any
	_ = json.Unmarshal(fixture(t, "oauth-1.json"), &oa)
	big["oauthAccount"] = oa["oauthAccount"]
	b, _ := json.Marshal(big)
	write(t, slot.StateFile, b)
	if _, err := st.Login(slot); err != nil { // warm the cache
		t.Fatal(err)
	}
	return st, slot
}

func BenchmarkCachedLogin(b *testing.B) {
	st, slot := cachedLoginSetup(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.Login(slot); err != nil {
			b.Fatal(err)
		}
	}
}

// Acceptance: a cached read takes under 1 ms (a benchmark would show about 20 us). Wall-clock
// asserts flake under -race and load, so the numbers are only logged; the test fails only
// above a bound 25 times looser (an uncached read of the big state file is far above it),
// or above 1 ms when WY_TIMING_STRICT=1. testing.Short skips the assert.
func TestCachedLoginUnderOneMillisecond(t *testing.T) {
	st, slot := cachedLoginSetup(t)
	const n = 200
	var worst time.Duration
	var total time.Duration
	for i := 0; i < n; i++ {
		t0 := time.Now()
		if _, err := st.Login(slot); err != nil {
			t.Fatal(err)
		}
		d := time.Since(t0)
		total += d
		if d > worst {
			worst = d
		}
	}
	avg := total / n
	t.Logf("cached login read: average %v, worst %v over %d reads", avg, worst, n)
	bound := 25 * time.Millisecond
	if os.Getenv("WY_TIMING_STRICT") == "1" {
		bound = time.Millisecond
	}
	if !testing.Short() && avg > bound {
		t.Errorf("cached read averages %v (worst %v), want under %v", avg, worst, bound)
	}
}

func TestNaturalLess(t *testing.T) {
	order := []string{"~/.claude-2", "~/.claude-10", "~/.claude-a", "~/work/cc"}
	for i := 0; i+1 < len(order); i++ {
		if !naturalLess(order[i], order[i+1]) || naturalLess(order[i+1], order[i]) {
			t.Errorf("%s should sort before %s", order[i], order[i+1])
		}
	}
}

// Login.LoggedIn (identity.md 4): oauthAccount stays in the state file after a logout, so a
// later `claude auth status` saying logged out wins, until oauthAccount changes again.
func TestLoggedInFollowsAuthStatus(t *testing.T) {
	out := []byte(`{"loggedIn":false}`)
	in := []byte(`{"loggedIn":true,"email":"user2@example.com"}`)
	cases := []struct {
		name  string
		steps func(t *testing.T, st *Store, slot Slot, tick func())
		want  bool
	}{
		{"oauthAccount, never asked", func(t *testing.T, st *Store, slot Slot, tick func()) {}, true},
		{"oauthAccount, auth status logged in", func(t *testing.T, st *Store, slot Slot, tick func()) {
			refresh(t, st, slot, in)
		}, true},
		{"oauthAccount kept, auth status logged out", func(t *testing.T, st *Store, slot Slot, tick func()) {
			refresh(t, st, slot, out)
		}, false},
		{"logged out, state file rewritten with the same oauthAccount", func(t *testing.T, st *Store, slot Slot, tick func()) {
			refresh(t, st, slot, out)
			tick()
			write(t, slot.StateFile, []byte(`{"numStartups":9,"oauthAccount":{"accountUuid":"a2","organizationUuid":"o2","emailAddress":"user2@example.com"}}`))
			touch(t, slot.StateFile, time.Hour)
		}, false},
		{"logged out, then a new login changed oauthAccount", func(t *testing.T, st *Store, slot Slot, tick func()) {
			refresh(t, st, slot, out)
			tick()
			write(t, slot.StateFile, []byte(`{"oauthAccount":{"accountUuid":"a3","organizationUuid":"o3","emailAddress":"user3@example.com"}}`))
			touch(t, slot.StateFile, time.Hour)
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			slot := Locator{Home: home, FS: OSFS{}}.SlotFor("~/.claude-2")
			write(t, slot.StateFile, []byte(`{"oauthAccount":{"accountUuid":"a2","organizationUuid":"o2","emailAddress":"user2@example.com"}}`))
			now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
			st := &Store{StateDir: t.TempDir(), FS: OSFS{}, Now: func() time.Time { return now }}
			if _, err := st.Login(slot); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			c.steps(t, st, slot, func() { now = now.Add(time.Minute) })
			l, err := st.Login(slot)
			if err != nil {
				t.Fatal(err)
			}
			if !l.HasSeat || l.LoggedIn() != c.want {
				t.Errorf("LoggedIn %v, want %v (%+v)", l.LoggedIn(), c.want, l)
			}
		})
	}
}

func refresh(t *testing.T, st *Store, slot Slot, out []byte) {
	t.Helper()
	var called []string
	if _, err := st.RefreshAuthStatus(context.Background(), slot, nil, fakeRunner(t, out, nil, &called)); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, p string, d time.Duration) {
	t.Helper()
	at := time.Now().Add(d)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}
