//go:build fakeinbox && peerstest

package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/agent"
	"github.com/mraj602/watchyard/internal/peers/fakeinbox"
)

// startAgent runs `wy agent run` in-process in the peersWorld: a temp runtime dir, the fake
// /proc, the fake inboxes. The "caller" of every connection is the fake process 501 (a
// bash under session A's claude), so no real process tree or real socket is involved.
func startAgent(t *testing.T, w *peersWorld) (stop func(), logPath string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	oldCtx, oldCred := agentContext, agentPeerCred
	agentContext = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	agentPeerCred = func(*net.UnixConn) (int, int, error) { return w.startPID, os.Getuid(), nil }
	t.Cleanup(func() { agentContext, agentPeerCred = oldCtx, oldCred })

	done := make(chan int, 1)
	var o, e bytes.Buffer
	go func() { done <- dispatch([]string{"agent", "run"}, &o, &e) }()
	sock := filepath.Join(w.runtime, "wy", "agent.sock")
	for i := 0; i < 300; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case code := <-done:
				if code != 0 {
					t.Errorf("wy agent run exited %d: %s", code, e.String())
				}
			case <-time.After(3 * time.Second):
				t.Error("wy agent run did not stop")
			}
		})
	}
	return stop, filepath.Join(w.state, "logs", "agent.log")
}

func TestAgentRunServesPeers(t *testing.T) {
	w := newPeersWorld(t)
	stop, logPath := startAgent(t, w)
	defer stop()

	code, out, errs := runPeersWy("peers", "send", "worker", "through the agent")
	if code != 0 || !strings.HasPrefix(out, "delivered to worker@personal.laptop (msg ") || errs != "" {
		t.Fatalf("send: %d %q %q", code, out, errs)
	}
	if !w.b.Wait(1, 2*time.Second) {
		t.Fatal("nothing delivered")
	}
	if g := w.b.All()[0]; g.Outcome != fakeinbox.Deliver || !strings.HasSuffix(g.Wrapper.Body, "\n\nthrough the agent") {
		t.Errorf("%s %q", g.Outcome, g.Wrapper.Body)
	}
	// The agent did it: its log has the line (ids and sizes, never the text).
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "send msg=") || !strings.Contains(string(b), "size=17") || strings.Contains(string(b), "through the agent") {
		t.Errorf("agent log: %q", b)
	}

	// Refusals come back as the same lines and exit codes.
	if code, out, _ = runPeersWy("peers", "send", "nobody", "hi"); code != 7 {
		t.Errorf("unknown target: %d %q", code, out)
	}
	if code, out, _ = runPeersWy("peers", "send", "worker", "ghp_"+strings.Repeat("a", 36)); code != 6 {
		t.Errorf("secret: %d %q", code, out)
	}

	// list goes through it as well.
	if code, out, _ = runPeersWy("peers", "list", "--json"); code != 0 || !strings.Contains(out, "worker@personal.laptop") {
		t.Errorf("list: %d %q", code, out)
	}

	// A request that names a session the caller is not inside is refused.
	sock := filepath.Join(w.runtime, "wy", "agent.sock")
	rep, err := agent.Call(sock, agent.Request{Op: agent.OpSend, ClaudePID: 600, To: "worker", Text: "x"}, 2*time.Second)
	if err != nil || rep.Error == nil || rep.Error.Code != "not_a_session" {
		t.Errorf("foreign claude_pid: %+v %v", rep, err)
	}
	if n := len(w.b.All()); n != 1 {
		t.Errorf("%d frames in the inbox, want 1", n)
	}

	// wy agent status sees it (the service is not installed here: no real unit exists).
	t.Setenv("HOME", w.home)
	oldSvc := agentServiceEnv
	t.Cleanup(func() { agentServiceEnv = oldSvc })
	agentServiceEnv = fakeServiceEnv(t, &recorder{})
	if code, out, _ = runPeersWy("agent", "status"); code != 0 || !strings.Contains(out, "agent: running, pid") || !strings.Contains(out, "link none") {
		t.Errorf("status: %d %q", code, out)
	}

	// Once the agent stops, send falls back to the direct path and still delivers.
	stop()
	code, out, _ = runPeersWy("peers", "send", "worker", "direct again")
	if code != 0 || !strings.HasPrefix(out, "delivered to worker@personal.laptop") {
		t.Errorf("fallback: %d %q", code, out)
	}
	if !w.b.Wait(2, 2*time.Second) {
		t.Error("fallback did not deliver")
	}
	b, _ = os.ReadFile(logPath)
	if strings.Count(string(b), "send msg=") != 1 {
		t.Errorf("the direct send was logged by the agent: %q", b)
	}
}

// Tags are held at the hub (peers.md 3.4): an agent without a hub link answers the list from
// its own registries, so a tag send must refuse hub_unreachable (exit 7) rather than report
// that no session holds the tag, and nothing reaches an inbox.
func TestAgentTagSendWithoutHub(t *testing.T) {
	w := newPeersWorld(t)
	stop, _ := startAgent(t, w)
	defer stop()

	code, out, _ := runPeersWy("peers", "send", "tag:relay", "hi")
	if code != 7 || !strings.Contains(out, "tags are held at the hub") {
		t.Fatalf("tag send without a hub: %d %q", code, out)
	}
	if code, out, _ = runPeersWy("peers", "send", "tag:relay", "--one", "--same-host", "hi"); code != 7 {
		t.Errorf("--one --same-host without a hub: %d %q", code, out)
	}
	if n := len(w.a.All()) + len(w.b.All()); n != 0 {
		t.Errorf("%d frames delivered", n)
	}
}
