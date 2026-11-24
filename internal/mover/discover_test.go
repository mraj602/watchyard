package mover

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/peers"
)

// fakeProc is a process table: pid -> (ppid, start time).
type fakeProc map[int][2]int

func (f fakeProc) Info(pid int) (peers.ProcInfo, error) {
	p, ok := f[pid]
	if !ok {
		return peers.ProcInfo{}, errors.New("no such process")
	}
	return peers.ProcInfo{PID: pid, PPID: p[0]}, nil
}
func (f fakeProc) ConfigDir(int) (string, bool, error) { return "", false, nil }
func (f fakeProc) StartTime(pid int) (string, error) {
	p, ok := f[pid]
	if !ok {
		return "", errors.New("no such process")
	}
	return strconv.Itoa(p[1]), nil
}

func TestSessionsDiscovery(t *testing.T) {
	dir := t.TempDir()
	slot := identity.Slot{Dir: filepath.Join(dir, ".claude-2"), Name: "~/.claude-2"}
	write := func(pid int, body string) {
		p := filepath.Join(slot.Dir, "sessions", strconv.Itoa(pid)+".json")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(100, `{"pid":100,"sessionId":"tmux-s","cwd":"/w","procStart":"5","kind":"interactive","status":"idle","statusUpdatedAt":1795212957805}`)
	write(200, `{"pid":200,"sessionId":"wrapped-s","cwd":"/w","procStart":"6","kind":"interactive","status":"busy","bridgeSessionId":"b1"}`)
	write(300, `{"pid":300,"sessionId":"bg-s","cwd":"/w","procStart":"7","kind":"bg","status":"idle"}`)
	write(400, `{"pid":400,"sessionId":"gone-s","cwd":"/w","procStart":"8","kind":"interactive"}`)
	write(500, `{"pid":500,"sessionId":"reused-s","cwd":"/w","procStart":"9","kind":"interactive"}`)
	// The .key file next to them is never opened (the FS refuses it).
	if err := os.WriteFile(filepath.Join(slot.Dir, "sessions", "100.abc.key"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := NewState(dir)
	_ = st.WriteRun(RunInfo{Wrapper: 190, Child: 195})
	proc := fakeProc{100: {90, 5}, 90: {1, 1}, 200: {195, 6}, 195: {190, 2}, 190: {1, 1}, 300: {1, 7}, 500: {1, 99}}
	y := Sys{FS: identity.OSFS{}, Proc: proc, State: st,
		Cmdline: func(pid int) []string { return []string{"claude", "--model", "opus"} },
		Panes:   func() map[int]string { return map[int]string{90: "%4"} }}
	got := map[string]Launch{}
	for _, l := range y.Sessions([]identity.Slot{slot}) {
		got[l.SessionID] = l
	}
	if len(got) != 3 {
		t.Fatalf("sessions: %+v", got)
	}
	if l := got["tmux-s"]; l.Kind != KindTmux || l.Pane != "%4" || l.PaneRoot || l.StatusSince.IsZero() || l.Status != "idle" {
		t.Errorf("tmux: %+v", l)
	}
	if l := got["wrapped-s"]; l.Kind != KindWrapped || l.Wrapper != 190 || !l.RemoteControl {
		t.Errorf("wrapped: %+v", l)
	}
	if l := got["bg-s"]; l.Kind != KindBackground {
		t.Errorf("bg: %+v", l)
	}
}
