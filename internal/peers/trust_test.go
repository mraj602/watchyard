package peers_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/peers"
	"github.com/mraj602/watchyard/internal/peers/doorbelltest"
)

// doorbellVenv makes a virtual environment at dir holding the agent-doorbell distribution and
// its console script, whose bin/python is a symlink to a real interpreter file elsewhere (as uv
// and pipx make them). It returns the resolved interpreter.
func (f *fx) doorbellVenv(dir string, shebang string) string {
	f.t.Helper()
	real := filepath.Join(f.base, "py", filepath.Base(dir), "bin", "python3.12")
	f.write(real, "#!fake interpreter\n")
	if err := os.Chmod(real, 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.write(filepath.Join(dir, "pyvenv.cfg"), "home = "+filepath.Dir(real)+"\n")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "bin", "python")); err != nil {
		f.t.Fatal(err)
	}
	doorbelltest.WriteDist(f.t, dir, shebang)
	return resolved(f.t, real)
}

// resolved is path with every symlink resolved (macOS's temp folder is under /private).
func resolved(t *testing.T, path string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// procArgv adds a process with an exact argv and a working folder to the fake /proc.
func (f *fx) procArgv(pid, ppid int, comm, exe string, argv []string, cwd string, start int) {
	f.proc(pid, ppid, comm, exe, "x", start)
	d := filepath.Join(f.procRoot, fmt.Sprint(pid))
	f.write(filepath.Join(d, "cmdline"), strings.Join(argv, "\x00")+"\x00")
	if cwd != "" {
		if err := os.Symlink(cwd, filepath.Join(d, "cwd")); err != nil {
			f.t.Fatal(err)
		}
	}
}

// The uv tool layout: ~/.local/bin/doorbell is a symlink into the tool's venv, whose python is
// a symlink to a managed interpreter. Everything is recorded resolved.
func TestResolveDoorbellUVLayout(t *testing.T) {
	f := newFx(t)
	venv := filepath.Join(f.home, ".local/share/uv/tools/agent-doorbell")
	interp := f.doorbellVenv(venv, "")
	link := filepath.Join(f.home, ".local/bin/doorbell")
	venv = resolved(t, venv)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(venv, "bin/doorbell"), link); err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{link, venv, filepath.Join(venv, "bin/doorbell")} {
		in, err := peers.ResolveDoorbellInstall(from)
		if err != nil {
			t.Fatalf("%s: %v", from, err)
		}
		if in.Venv != venv || in.Program != filepath.Join(venv, "bin/doorbell") || in.Interpreter != interp {
			t.Errorf("%s: %+v", from, in)
		}
	}
}

// The pipx layout, with the #!/bin/sh trampoline some installers write: the interpreter is the
// venv's python, resolved.
func TestResolveDoorbellPipxTrampoline(t *testing.T) {
	f := newFx(t)
	venv := filepath.Join(f.home, ".local/pipx/venvs/agent-doorbell")
	interp := f.doorbellVenv(venv, "#!/bin/sh\n'''exec' \""+filepath.Join(venv, "bin/python")+"\" \"$0\" \"$@\"\n' '''")
	venv = resolved(t, venv)
	in, err := peers.ResolveDoorbellInstall(filepath.Join(venv, "bin/doorbell"))
	if err != nil {
		t.Fatal(err)
	}
	if in.Venv != venv || in.Interpreter != interp {
		t.Errorf("%+v", in)
	}
}

func TestResolveDoorbellRefuses(t *testing.T) {
	f := newFx(t)
	// No distribution in the venv.
	venv := filepath.Join(f.home, "v1")
	f.doorbellVenv(venv, "")
	if err := os.RemoveAll(filepath.Join(venv, "lib")); err != nil {
		t.Fatal(err)
	}
	if _, err := peers.ResolveDoorbellInstall(venv); err == nil || !strings.Contains(err.Error(), "agent-doorbell") {
		t.Errorf("no dist: %v", err)
	}
	// Another distribution's METADATA under the right folder name.
	venv = filepath.Join(f.home, "v2")
	f.doorbellVenv(venv, "")
	f.write(filepath.Join(venv, "lib/python3.12/site-packages/agent_doorbell-0.4.1.dist-info/METADATA"), "Name: something-else\n")
	if _, err := peers.ResolveDoorbellInstall(venv); err == nil {
		t.Error("wrong METADATA name accepted")
	}
	// A script that is not the venv's bin/doorbell.
	venv = filepath.Join(f.home, "v3")
	f.doorbellVenv(venv, "")
	other := filepath.Join(f.home, "elsewhere/doorbell")
	f.write(other, "#!"+filepath.Join(venv, "bin/python")+"\n")
	if _, err := peers.ResolveDoorbellInstall(other); err == nil {
		t.Error("a script outside a venv accepted")
	}
	// A script that runs something other than Python.
	venv = filepath.Join(f.home, "v4")
	f.doorbellVenv(venv, "#!/usr/bin/env node")
	if _, err := peers.ResolveDoorbellInstall(venv); err == nil {
		t.Error("a non-Python interpreter accepted")
	}
	// Not a venv at all.
	if _, err := peers.ResolveDoorbellInstall(f.home); err == nil {
		t.Error("a folder that is not a venv accepted")
	}
}

func TestParsePythonArgv(t *testing.T) {
	for _, c := range []struct {
		argv   []string
		ok     bool
		script string
		module string
		iso    bool
	}{
		{[]string{"/v/bin/python", "/v/bin/doorbell", "run"}, true, "/v/bin/doorbell", "", false},
		{[]string{"/v/bin/python", "-u", "-X", "utf8", "/v/bin/doorbell"}, true, "/v/bin/doorbell", "", false},
		{[]string{"/v/bin/python", "-m", "doorbell", "run"}, true, "", "doorbell", false},
		{[]string{"/v/bin/python", "-I", "-m", "doorbell"}, true, "", "doorbell", true},
		{[]string{"/v/bin/python", "-P", "-mdoorbell"}, true, "", "doorbell", true},
		{[]string{"/v/bin/python", "-c", "import doorbell"}, false, "", "", false},
		{[]string{"/v/bin/python"}, false, "", "", false},
		{[]string{"/v/bin/python", "-W"}, false, "", "", false},
	} {
		run, ok := peers.ParsePythonArgv(c.argv)
		if ok != c.ok || (ok && (run.Script != c.script || run.Module != c.module || run.Isolated != c.iso)) {
			t.Errorf("%q: %+v %v", c.argv, run, ok)
		}
	}
}

// trustWorld: a machine with the work account logged in, Doorbell installed with uv and
// approved, and a process tree python (doorbell) → sh → wy (pid 702).
type trustWorld struct {
	*fx
	venv, interp, script string
	rec                  peers.TrustRecord
}

func newTrustWorld(t *testing.T) *trustWorld {
	f := newFx(t)
	w := &trustWorld{fx: f}
	f.slot(".claude-2", "acc-work")
	w.venv = filepath.Join(f.home, ".local/share/uv/tools/agent-doorbell")
	w.interp = f.doorbellVenv(w.venv, "")
	w.venv = resolved(t, w.venv)
	w.script = filepath.Join(f.home, ".local/bin/doorbell")
	if err := os.MkdirAll(filepath.Dir(w.script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(w.venv, "bin/doorbell"), w.script); err != nil {
		t.Fatal(err)
	}
	e := f.env()
	accts := e.ManagedAccounts()
	if len(accts) != 1 || accts[0].Account != "acc-work" || accts[0].Label != "work" {
		t.Fatalf("managed accounts: %+v", accts)
	}
	rec, err := e.ApproveDoorbell(w.script, "acc-work")
	if err != nil {
		t.Fatal(err)
	}
	w.rec = rec
	return w
}

// tree adds doorbell (700, under parent), sh (701) and wy (702).
func (w *trustWorld) tree(parent int, exe string, argv []string) {
	w.procArgv(700, parent, "doorbell", exe, argv, w.home, 7000)
	w.procArgv(701, 700, "sh", "/usr/bin/sh", []string{"/bin/sh", "-c", "wy peers send tag:ci --stdin"}, w.home, 7001)
	w.procArgv(702, 701, "wy", "/usr/local/bin/wy", []string{"wy", "peers", "send", "tag:ci", "--stdin"}, w.home, 7002)
}

func (w *trustWorld) check() (peers.TrustRecord, *peers.Refusal) {
	e := w.env()
	e.StartPID = 702
	return e.TrustedSender()
}

func wantNotTrusted(t *testing.T, rf *peers.Refusal, why string) {
	t.Helper()
	if rf == nil {
		t.Fatal("accepted")
	}
	if rf.Code != peers.CodeNotASession || !strings.Contains(rf.Detail, "not the approved Doorbell") || !strings.Contains(rf.Detail, why) {
		t.Errorf("refusal: %+v (want %q)", rf, why)
	}
}

func TestApproveDoorbellRecord(t *testing.T) {
	w := newTrustWorld(t)
	got, err := w.env().LoadTrust()
	if err != nil || got == nil {
		t.Fatalf("load: %v %v", got, err)
	}
	if got.Venv != w.venv || got.Program != filepath.Join(w.venv, "bin/doorbell") || got.Interpreter != w.interp ||
		got.Machine != "m_TEST" || got.Account != "acc-work" || got.AccountLabel != "work" || !got.ApprovedAt.Equal(w.now) {
		t.Errorf("record: %+v", got)
	}
	if st, err := os.Stat(w.env().TrustPath()); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("record mode: %v %v", st, err)
	}
	audit, _ := os.ReadFile(w.env().AuditPath())
	if !strings.Contains(string(audit), `"status":"trust"`) || !strings.Contains(string(audit), `"from_kind":"service:doorbell"`) {
		t.Errorf("audit: %s", audit)
	}
	// An account no managed folder is logged in to is refused.
	if _, err := w.env().ApproveDoorbell(w.script, "acc-elsewhere"); err == nil {
		t.Error("an unmanaged account approved")
	}
}

func TestTrustedSenderScriptForm(t *testing.T) {
	w := newTrustWorld(t)
	// As the kernel runs a console script: argv[1] is the path it was started by (the symlink).
	w.tree(1, w.interp, []string{filepath.Join(w.venv, "bin/python"), w.script, "watch"})
	rec, rf := w.check()
	if rf != nil {
		t.Fatalf("refused: %+v", rf)
	}
	if rec.Account != "acc-work" || rec.Venv != w.venv {
		t.Errorf("record: %+v", rec)
	}
}

func TestTrustedSenderModuleForm(t *testing.T) {
	w := newTrustWorld(t)
	w.tree(1, w.interp, []string{filepath.Join(w.venv, "bin/python"), "-m", "doorbell", "watch"})
	if _, rf := w.check(); rf != nil {
		t.Fatalf("refused: %+v", rf)
	}
	// A doorbell package in the working folder would shadow the venv's: refused, unless -I.
	if err := os.MkdirAll(filepath.Join(w.home, "doorbell"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, rf := w.check()
	wantNotTrusted(t, rf, "another doorbell package")
	if err := os.RemoveAll(filepath.Join(w.procRoot, "700")); err != nil {
		t.Fatal(err)
	}
	w.procArgv(700, 1, "doorbell", w.interp, []string{filepath.Join(w.venv, "bin/python"), "-I", "-m", "doorbell"}, w.home, 7000)
	if _, rf := w.check(); rf != nil {
		t.Fatalf("isolated refused: %+v", rf)
	}
}

func TestTrustedSenderModuleFromPATHRefused(t *testing.T) {
	w := newTrustWorld(t)
	w.tree(1, w.interp, []string{"python", "-m", "doorbell"})
	_, rf := w.check()
	wantNotTrusted(t, rf, "PATH")
}

func TestTrustedSenderWrongPath(t *testing.T) {
	w := newTrustWorld(t)
	// The recorded interpreter running another script.
	other := filepath.Join(w.home, "bin/doorbell")
	w.write(other, "#!/x\n")
	w.tree(1, w.interp, []string{filepath.Join(w.venv, "bin/python"), other})
	_, rf := w.check()
	wantNotTrusted(t, rf, "not the recorded venv's bin/doorbell")
}

func TestTrustedSenderWrongInterpreter(t *testing.T) {
	w := newTrustWorld(t)
	w.tree(1, "/usr/bin/python3", []string{"/usr/bin/python3", w.script})
	_, rf := w.check()
	wantNotTrusted(t, rf, "no Doorbell process")
}

// A Claude Code session above Doorbell makes the command a session's: never the trusted sender.
func TestTrustedSenderUnderClaudeIsASession(t *testing.T) {
	w := newTrustWorld(t)
	slot := filepath.Join(w.home, ".claude-2")
	w.claudeProc(500, 1, slot, 4242)
	w.registry(slot, 500, "sess-a", "api", w.home, filepath.Join(w.runtime, peers.SocksDirName, "500.sock"), 4242)
	w.tree(500, w.interp, []string{filepath.Join(w.venv, "bin/python"), w.script})
	_, rf := w.check()
	wantNotTrusted(t, rf, "a Claude Code process (pid 500)")
	e := w.env()
	e.StartPID = 702
	if c, rf := e.Whoami(); rf != nil || c.Entry.SessionID != "sess-a" {
		t.Errorf("whoami: %+v %+v", c, rf)
	}
}

func TestTrustedSenderNoDoorbellAncestor(t *testing.T) {
	w := newTrustWorld(t)
	w.procArgv(702, 1, "wy", "/usr/local/bin/wy", []string{"wy", "peers", "send", "tag:ci", "--stdin"}, w.home, 7002)
	_, rf := w.check()
	wantNotTrusted(t, rf, "no Doorbell process above this command")
}

func TestTrustedSenderRevoked(t *testing.T) {
	w := newTrustWorld(t)
	w.tree(1, w.interp, []string{filepath.Join(w.venv, "bin/python"), w.script})
	if ok, err := w.env().RevokeDoorbell("cli"); !ok || err != nil {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	_, rf := w.check()
	wantNotTrusted(t, rf, "no trusted sender is approved")
	if ok, _ := w.env().RevokeDoorbell("cli"); ok {
		t.Error("a second revoke reported a change")
	}
	audit, _ := os.ReadFile(w.env().AuditPath())
	if !strings.Contains(string(audit), `"status":"trust_off"`) {
		t.Errorf("audit: %s", audit)
	}
}

// Removing the package from the venv, or a record from another machine, stops the sender.
func TestTrustedSenderRecordNoLongerHolds(t *testing.T) {
	w := newTrustWorld(t)
	w.tree(1, w.interp, []string{filepath.Join(w.venv, "bin/python"), w.script})
	rec := w.rec
	rec.Machine = "m_OTHER"
	if err := w.env().SaveTrust(&rec); err != nil {
		t.Fatal(err)
	}
	_, rf := w.check()
	wantNotTrusted(t, rf, "another machine")
	if err := w.env().SaveTrust(&w.rec); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(w.venv, "lib")); err != nil {
		t.Fatal(err)
	}
	_, rf = w.check()
	wantNotTrusted(t, rf, "no longer holds")
}

// Doorbell never sends to a session by name; the refusal comes before anything else.
func TestSendServiceRefusesHandle(t *testing.T) {
	w := newTrustWorld(t)
	for _, to := range []string{"worker", "api@work.laptop", "sess-b"} {
		_, rf := w.env().SendService(w.rec, to, "hello", false, false, peers.SendOpts{})
		if rf == nil || rf.Code != peers.CodeNotAllowed || !strings.Contains(rf.Detail, "only to a tag") {
			t.Errorf("%s: %+v", to, rf)
		}
	}
	_, rf := w.env().SendService(w.rec, "tag:ci", "hello", false, false, peers.SendOpts{})
	if rf == nil || rf.Code != peers.CodeHubUnreachable {
		t.Errorf("no hub: %+v", rf)
	}
}

func TestServiceHeader(t *testing.T) {
	h := peers.ServiceHeader("laptop", "work", "ci", "t_1")
	if !strings.HasPrefix(h, "[Watchyard] from doorbell (service) on laptop · account work · tag ci · thread t_1\n") ||
		!strings.Contains(h, "reads no replies") || strings.Contains(h, "Reply with") {
		t.Errorf("header: %q", h)
	}
	if peers.ServiceSessionID("m_X") != "svc-doorbell-m_X" {
		t.Error("service session id")
	}
}
