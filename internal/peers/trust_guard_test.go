package peers_test

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mraj602/watchyard/internal/peers"
	"github.com/mraj602/watchyard/internal/peers/doorbelltest"
)

// setStat rewrites pid's session and controlling terminal (tty_nr) in the fake /proc.
func (f *fx) setStat(pid, session, tty int) {
	f.t.Helper()
	p := filepath.Join(f.procRoot, fmt.Sprint(pid), "stat")
	b, err := os.ReadFile(p)
	if err != nil {
		f.t.Fatal(err)
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	fields := strings.Fields(s[i+1:])
	fields[3], fields[4] = fmt.Sprint(session), fmt.Sprint(tty)
	f.write(p, s[:i+1]+" "+strings.Join(fields, " ")+"\n")
}

// drop removes pid from the fake /proc, so a test can add it again differently.
func (f *fx) drop(pid int) {
	if err := os.RemoveAll(filepath.Join(f.procRoot, fmt.Sprint(pid))); err != nil {
		f.t.Fatal(err)
	}
}

// setEnv replaces pid's environment in the fake /proc.
func (f *fx) setEnv(pid int, env ...string) {
	f.write(filepath.Join(f.procRoot, fmt.Sprint(pid), "environ"), strings.Join(env, "\x00")+"\x00")
}

// flipProc is the fake /proc whose stat for pid reports another start time from the after-th
// read on: the process was replaced (its pid reused) while it was being checked.
type flipProc struct {
	peers.LinuxProc
	pid, after int
	mu         sync.Mutex
	n          int
}

func (p *flipProc) Stat(pid int) (peers.ProcStat, error) {
	st, err := p.LinuxProc.Stat(pid)
	if pid != p.pid || err != nil {
		return st, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	if p.n >= p.after {
		st.Start += "9"
	}
	return st, nil
}

const tty = 34816 // /dev/pts/0

// approverWorld is a terminal: bash (100, the session leader, under the terminal emulator 90)
// and wy (101) on /dev/pts/0.
func approverWorld(t *testing.T) *fx {
	f := newFx(t)
	f.proc(90, 1, "gnome-terminal-", "/usr/libexec/gnome-terminal-server", "gnome-terminal-server", 900, "HOME="+f.home)
	f.proc(100, 90, "bash", "/usr/bin/bash", "bash", 1000, "HOME="+f.home, "TERM=xterm")
	f.setStat(100, 100, tty)
	f.proc(101, 100, "wy", "/usr/local/bin/wy", "wy peers trust doorbell", 1001, "HOME="+f.home, "TERM=xterm")
	f.setStat(101, 100, tty)
	return f
}

func wantApproverRefused(t *testing.T, rf *peers.Refusal, why string) {
	t.Helper()
	if rf == nil {
		t.Fatalf("accepted (want %q)", why)
	}
	if rf.Code != peers.CodeNotASession || !strings.Contains(rf.Detail, why) {
		t.Errorf("refusal: %+v (want %q)", rf, why)
	}
}

func TestCheckApproverAtATerminal(t *testing.T) {
	f := approverWorld(t)
	p := peers.LinuxProc{Root: f.procRoot}
	if rf := peers.CheckApprover(p, 101, nil); rf != nil {
		t.Fatalf("refused: %+v", rf)
	}
	if rf := peers.CheckApprover(p, 101, func() bool { return true }); rf != nil {
		t.Fatalf("refused with a live pidfd: %+v", rf)
	}
	wantApproverRefused(t, peers.CheckApprover(p, 101, func() bool { return false }), "ended while it was checked")
}

// env -i setsid -f wy …, systemd-run --user wy …, cron, a pipe: no controlling terminal.
func TestCheckApproverNeedsATerminal(t *testing.T) {
	f := approverWorld(t)
	f.proc(200, 1, "wy", "/usr/local/bin/wy", "wy peers trust doorbell", 2000)
	f.setStat(200, 200, 0)
	wantApproverRefused(t, peers.CheckApprover(peers.LinuxProc{Root: f.procRoot}, 200, nil), "no controlling terminal")
}

func TestCheckApproverRefusesClaudeEnvironmentNames(t *testing.T) {
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_MESSAGING_SOCKET"} {
		f := approverWorld(t)
		f.setEnv(101, "HOME="+f.home, name+"=1")
		wantApproverRefused(t, peers.CheckApprover(peers.LinuxProc{Root: f.procRoot}, 101, nil), "has "+name)
		// On the session leader too.
		f = approverWorld(t)
		f.setEnv(100, "HOME="+f.home, name+"=1")
		wantApproverRefused(t, peers.CheckApprover(peers.LinuxProc{Root: f.procRoot}, 101, nil), "session leader (pid 100) has "+name)
	}
	f := approverWorld(t)
	if err := os.Remove(filepath.Join(f.procRoot, "101", "environ")); err != nil {
		t.Fatal(err)
	}
	wantApproverRefused(t, peers.CheckApprover(peers.LinuxProc{Root: f.procRoot}, 101, nil), "cannot be read")
}

func TestCheckApproverRefusesClaudeAbove(t *testing.T) {
	f := approverWorld(t)
	f.claudeProc(50, 1, "", 500)
	f.drop(100)
	f.proc(100, 50, "bash", "/usr/bin/bash", "bash", 1000, "HOME="+f.home)
	f.setStat(100, 100, tty)
	wantApproverRefused(t, peers.CheckApprover(peers.LinuxProc{Root: f.procRoot}, 101, nil), "a Claude Code process (pid 50) is above this command")
}

// A session that starts its own terminal (script, a pty) and re-parents wy away from itself:
// the requester's walk is clean, the terminal's session leader is still under Claude Code.
func TestCheckApproverRefusesSessionLeaderUnderClaude(t *testing.T) {
	f := approverWorld(t)
	f.claudeProc(50, 1, "", 500)
	f.proc(60, 50, "script", "/usr/bin/script", "script -q -c sh", 600)
	f.drop(100)
	f.proc(100, 60, "sh", "/usr/bin/sh", "sh", 1000)
	f.setStat(100, 100, tty)
	f.drop(101)
	f.proc(101, 1, "wy", "/usr/local/bin/wy", "wy peers trust doorbell", 1001)
	f.setStat(101, 100, tty)
	wantApproverRefused(t, peers.CheckApprover(peers.LinuxProc{Root: f.procRoot}, 101, nil), "a Claude Code process (pid 50) is above its terminal's session leader")
}

func TestCheckApproverSessionLeader(t *testing.T) {
	f := approverWorld(t)
	f.setStat(101, 555, tty) // the leader is gone
	wantApproverRefused(t, peers.CheckApprover(peers.LinuxProc{Root: f.procRoot}, 101, nil), "session leader of its terminal (pid 555) cannot be read")
	f = approverWorld(t)
	f.setStat(100, 100, tty+1)
	wantApproverRefused(t, peers.CheckApprover(peers.LinuxProc{Root: f.procRoot}, 101, nil), "not on this command's terminal")
}

// A walk that cannot reach the top cannot rule out a session above: refused, not taken as clean.
func TestCheckApproverIncompleteWalk(t *testing.T) {
	f := approverWorld(t)
	f.drop(100)
	f.proc(100, 999, "bash", "/usr/bin/bash", "bash", 1000) // 999 cannot be read
	f.setStat(100, 100, tty)
	wantApproverRefused(t, peers.CheckApprover(peers.LinuxProc{Root: f.procRoot}, 101, nil), "cannot all be read")

	f = approverWorld(t)
	for pid := 300; pid < 300+peers.MaxWalk; pid++ { // 64 shells above the leader: 66 levels
		f.proc(pid, pid+1, "sh", "/usr/bin/sh", "sh", pid)
	}
	f.proc(300+peers.MaxWalk, 1, "sh", "/usr/bin/sh", "sh", 1)
	f.drop(100)
	f.proc(100, 300, "bash", "/usr/bin/bash", "bash", 1000)
	f.setStat(100, 100, tty)
	wantApproverRefused(t, peers.CheckApprover(peers.LinuxProc{Root: f.procRoot}, 101, nil), "cannot all be read")
}

func TestCheckApproverProcessReplaced(t *testing.T) {
	for _, pid := range []int{101, 100, 90} {
		f := approverWorld(t)
		p := &flipProc{LinuxProc: peers.LinuxProc{Root: f.procRoot}, pid: pid, after: 2}
		if pid == 101 {
			p.after = 3 // the first two reads are before the walk and in it
		}
		if rf := peers.CheckApprover(p, 101, nil); rf == nil || !strings.Contains(rf.Detail, "changed while it was checked") {
			t.Errorf("pid %d replaced: %+v", pid, rf)
		}
	}
}

func TestWalkAncestorsComplete(t *testing.T) {
	f := newFx(t)
	f.proc(10, 9, "sh", "/usr/bin/sh", "sh", 1)
	f.proc(9, 1, "systemd", "/usr/lib/systemd/systemd", "systemd --user", 1)
	f.proc(20, 21, "sh", "/usr/bin/sh", "sh", 1) // 21 cannot be read
	f.proc(30, 0, "init", "/sbin/init", "init", 1)
	p := peers.LinuxProc{Root: f.procRoot}
	for _, c := range []struct {
		start    int
		n        int
		complete bool
	}{{10, 2, true}, {20, 1, false}, {30, 1, true}, {1, 0, true}, {0, 0, false}, {404, 0, false}} {
		got, complete := peers.WalkAncestors(p, c.start)
		if len(got) != c.n || complete != c.complete {
			t.Errorf("walk from %d: %d processes, complete %v", c.start, len(got), complete)
		}
	}
}

// The send-side check refuses a walk it cannot finish, a process replaced while it was read
// and a requester whose pidfd says it has gone.
func TestTrustedSenderWalkMustBeComplete(t *testing.T) {
	w := newTrustWorld(t)
	w.tree(999, w.interp, []string{filepath.Join(w.venv, "bin/python"), w.script}) // 999 cannot be read
	_, rf := w.check()
	wantNotTrusted(t, rf, "cannot all be read")
}

func TestTrustedSenderProcessReplaced(t *testing.T) {
	for _, pid := range []int{702, 700} {
		w := newTrustWorld(t)
		w.tree(1, w.interp, []string{filepath.Join(w.venv, "bin/python"), w.script})
		e := w.env()
		e.StartPID = 702
		e.Proc = &flipProc{LinuxProc: peers.LinuxProc{Root: w.procRoot}, pid: pid, after: 2}
		_, rf := e.TrustedSender()
		wantNotTrusted(t, rf, "changed while it was checked")
	}
	w := newTrustWorld(t)
	w.tree(1, w.interp, []string{filepath.Join(w.venv, "bin/python"), w.script})
	e := w.env()
	e.StartPID = 702
	e.PeerAlive = func() bool { return false }
	_, rf := e.TrustedSender()
	wantNotTrusted(t, rf, "ended while it was checked")
	e.PeerAlive = func() bool { return true }
	if _, rf := e.TrustedSender(); rf != nil {
		t.Errorf("live pidfd refused: %+v", rf)
	}
}

// rehash makes RECORD's line for rel (relative to site-packages) match the file now.
func rehash(t *testing.T, venv, rel string) {
	t.Helper()
	site := filepath.Join(venv, "lib/python3.12/site-packages")
	b, err := os.ReadFile(filepath.Join(site, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	rp := filepath.Join(venv, doorbelltest.Dist, "RECORD")
	rec, _ := os.ReadFile(rp)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(rec)), "\n") {
		if strings.HasPrefix(l, rel+",") {
			l = fmt.Sprintf("%s,sha256=%s,%d", rel, base64.RawURLEncoding.EncodeToString(sum[:]), len(b))
		}
		out = append(out, l)
	}
	if err := os.WriteFile(rp, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A venv made to look like Doorbell's (pyvenv.cfg, a METADATA naming agent-doorbell and any
// bin/doorbell) is not an install: RECORD, entry_points.txt and the launcher must hold.
func TestDoorbellInstallChecks(t *testing.T) {
	f := newFx(t)
	mk := func(name string) string {
		v := filepath.Join(f.home, name)
		f.doorbellVenv(v, "")
		return resolved(t, v)
	}
	ok := mk("good")
	if _, err := peers.ResolveDoorbellInstall(ok); err != nil {
		t.Fatalf("a real install refused: %v", err)
	}

	cases := []struct {
		name, why string
		spoil     func(v string)
	}{
		{"no RECORD", "no RECORD", func(v string) { os.Remove(filepath.Join(v, doorbelltest.Dist, "RECORD")) }},
		{"no entry point", "console script", func(v string) {
			f.write(filepath.Join(v, doorbelltest.Dist, "entry_points.txt"), "[console_scripts]\ndoorbell = evil:main\n")
		}},
		{"cli edited", "does not match its RECORD", func(v string) {
			f.write(filepath.Join(v, "lib/python3.12/site-packages/doorbell/cli.py"), "import os\n")
		}},
		{"script edited", "does not match its RECORD", func(v string) {
			b, _ := os.ReadFile(filepath.Join(v, "bin/doorbell"))
			f.write(filepath.Join(v, "bin/doorbell"), string(b)+"import os\n")
		}},
		{"script not a launcher, RECORD rewritten to match", "not the standard launcher", func(v string) {
			f.write(filepath.Join(v, "bin/doorbell"), "#!"+filepath.Join(v, "bin/python")+"\nimport os\nos.system('wy peers send tag:x --stdin')\n")
			rehash(t, v, "../../../bin/doorbell")
		}},
		{"script missing from RECORD", "does not list", func(v string) {
			rp := filepath.Join(v, doorbelltest.Dist, "RECORD")
			b, _ := os.ReadFile(rp)
			var keep []string
			for _, l := range strings.Split(string(b), "\n") {
				if !strings.HasPrefix(l, "../../../bin/doorbell,") {
					keep = append(keep, l)
				}
			}
			f.write(rp, strings.Join(keep, "\n"))
		}},
		{"a shell script", "shell script", func(v string) {
			f.write(filepath.Join(v, "bin/doorbell"), "#!/bin/sh\nexec wy peers send tag:x --stdin\n")
			rehash(t, v, "../../../bin/doorbell")
		}},
	}
	for i, c := range cases {
		v := mk(fmt.Sprintf("bad%d", i))
		c.spoil(v)
		if _, err := peers.ResolveDoorbellInstall(v); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: %v (want %q)", c.name, err, c.why)
		}
	}
}

// uv writes another launcher than pip; it passes too.
func TestDoorbellInstallUVLauncher(t *testing.T) {
	f := newFx(t)
	v := filepath.Join(f.home, "uv")
	f.doorbellVenv(v, "")
	v = resolved(t, v)
	f.write(filepath.Join(v, "bin/doorbell"), "#!"+filepath.Join(v, "bin/python")+"\n# -*- coding: utf-8 -*-\nimport sys\nfrom doorbell.cli import main\n"+
		"if __name__ == \"__main__\":\n    if sys.argv[0].endswith(\"-script.pyw\"):\n        sys.argv[0] = sys.argv[0][:-11]\n"+
		"    elif sys.argv[0].endswith(\".exe\"):\n        sys.argv[0] = sys.argv[0][:-4]\n    sys.exit(main())\n")
	rehash(t, v, "../../../bin/doorbell")
	if _, err := peers.ResolveDoorbellInstall(v); err != nil {
		t.Fatal(err)
	}
}

// The send-side check runs the install checks again: an edit after the approval stops it.
func TestTrustedSenderInstallEditedAfterApproval(t *testing.T) {
	w := newTrustWorld(t)
	w.tree(1, w.interp, []string{filepath.Join(w.venv, "bin/python"), w.script})
	w.write(filepath.Join(w.venv, "lib/python3.12/site-packages/doorbell/cli.py"), "import os\n")
	_, rf := w.check()
	wantNotTrusted(t, rf, "does not match its RECORD")
}

// freshRemote is a hub whose fresh directory answer revokes the approval (what
// peerlink.Remote's reconcile does with the hub's trust state); the cached one does not.
type freshRemote struct {
	w                  *trustWorld
	fresh, stale, sent int
}

func (r *freshRemote) Sessions() ([]peers.Session, error) {
	r.stale++
	return nil, nil
}

func (r *freshRemote) SessionsFresh() ([]peers.Session, error) {
	r.fresh++
	if r.w != nil {
		if _, err := r.w.env().RevokeDoorbell("hub"); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (r *freshRemote) Send(peers.Outgoing) (*peers.Pending, *peers.Refusal) {
	r.sent++
	return nil, peers.Refuse(peers.CodeFailed, "not in this test")
}
func (r *freshRemote) Log(peers.LocalLog)              {}
func (r *freshRemote) Names() map[string]peers.HubName { return nil }
func (r *freshRemote) Tag(string, string, string) (peers.TagAnswer, error) {
	return peers.TagAnswer{}, fmt.Errorf("not in this test")
}

// A revocation reaches the very next send on this machine: the send asks the hub past the
// directory cache and reads the record again after the answer.
func TestSendServiceAppliesRevocationAtOnce(t *testing.T) {
	w := newTrustWorld(t)
	r := &freshRemote{w: w}
	e := w.env()
	e.Remote = r
	_, rf := e.SendService(w.rec, "tag:ci", "hello", false, false, peers.SendOpts{})
	if rf == nil || rf.Code != peers.CodeNotASession || !strings.Contains(rf.Detail, "revoked or replaced") {
		t.Errorf("send after a revocation: %+v", rf)
	}
	if r.fresh != 1 || r.stale != 0 || r.sent != 0 {
		t.Errorf("fresh %d, cached %d, sent %d", r.fresh, r.stale, r.sent)
	}
	// An approval replaced meanwhile (approved again) is not the one the check passed either.
	w2 := newTrustWorld(t)
	old := w2.rec
	old.ApprovedAt = old.ApprovedAt.Add(-1)
	e2 := w2.env()
	e2.Remote = &freshRemote{}
	_, rf = e2.SendService(old, "tag:ci", "hello", false, false, peers.SendOpts{})
	if rf == nil || rf.Code != peers.CodeNotASession {
		t.Errorf("send with a replaced approval: %+v", rf)
	}
}

// A record a test build made is valid and marked.
func TestTestBuildRecordHonoured(t *testing.T) {
	w := newTrustWorld(t)
	rec := w.rec
	rec.TestBuild = true
	if err := w.env().SaveTrust(&rec); err != nil {
		t.Fatal(err)
	}
	got, err := w.env().LoadTrust()
	if err != nil || got == nil || !got.TestBuild {
		t.Fatalf("load: %+v %v", got, err)
	}
	b, _ := os.ReadFile(w.env().TrustPath())
	if !strings.Contains(string(b), `"test_build": true`) {
		t.Errorf("record: %s", b)
	}
	w.tree(1, w.interp, []string{filepath.Join(w.venv, "bin/python"), w.script})
	if _, rf := w.check(); rf != nil {
		t.Errorf("a test build's record refused: %+v", rf)
	}
	if w.rec.TestBuild != peers.TrustGuardOff {
		t.Errorf("ApproveDoorbell's TestBuild %v in a build whose guard off is %v", w.rec.TestBuild, peers.TrustGuardOff)
	}
}
