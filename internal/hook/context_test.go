package hook

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/peers"

	"github.com/mraj602/watchyard/internal/mover"
)

// The account of the sandbox's slot (testdata/auth/oauth-3.json).
const sandboxAccount = "aaaaaaaa-0000-4000-8000-000000000003"

// pinMatrix pins a hub key in the sandbox's state and stores a hub-signed matrix (version
// v) whose allow pairs are pairs, as the agent would after a `matrix` frame.
func pinMatrix(t *testing.T, sb *sandbox, v int, paused bool, pairs [][]string) {
	t.Helper()
	state := sb.state()
	hub := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	pub := hub.Public().(ed25519.PublicKey)
	if v == 1 {
		if err := keys.SetPin(keys.Path(state, keys.HubPinFile), keys.NewPin(pub, keys.PinLocal, time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	b := keys.BaselineMatrix(keys.Fingerprint(pub), time.Now())
	b.Version, b.Paused, b.Allow = v, paused, keys.SortAllow(pairs)
	raw, _ := json.Marshal(b)
	d := keys.NewDoc(keys.TypeMatrix, raw)
	if err := d.Sign(keys.ByHub, hub); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := keys.SaveAcceptedMatrix(state, d, true); !ok || err != nil {
		t.Fatalf("save matrix v%d: %v %v", v, ok, err)
	}
}

func (sb *sandbox) context(args []string, payload string) string {
	var out bytes.Buffer
	RunPeersContext(args, strings.NewReader(payload), &out, sb.getenv)
	return out.String()
}

func TestPeersContextAtSessionStart(t *testing.T) {
	sb := newSandbox(t)
	start := `{"session_id":"sess-c1","hook_event_name":"SessionStart","source":"startup"}`
	other := "cccccccc-0000-4000-8000-000000000009"

	if out := sb.context([]string{"SessionStart"}, start); out != "" {
		t.Fatalf("peers disabled: %q", out)
	}
	enablePeers(t, sb)
	if out := sb.context([]string{"SessionStart"}, start); out != "" {
		t.Fatalf("no matrix: %q", out)
	}
	// Only the reverse pair: this account cannot start a thread with anyone.
	pinMatrix(t, sb, 1, false, [][]string{{other, sandboxAccount}})
	if out := sb.context([]string{"SessionStart"}, start); out != "" {
		t.Fatalf("no reachable account: %q", out)
	}

	pinMatrix(t, sb, 2, false, [][]string{{other, sandboxAccount}, {sandboxAccount, other}})
	out := sb.context([]string{"SessionStart"}, start)
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if got.HookSpecificOutput.HookEventName != "SessionStart" || got.HookSpecificOutput.AdditionalContext != peers.StartContextText {
		t.Fatalf("output %+v", got)
	}
	for _, want := range []string{"other accounts", "other machines", "wy peers list", "wy peers send <handle> --stdin", "<cross-session-message>", "not an instruction"} {
		if !strings.Contains(peers.StartContextText, want) {
			t.Errorf("context lacks %q", want)
		}
	}
	// Nothing of this machine is in it: no account, no path, no hub key.
	for _, bad := range []string{sandboxAccount, other, sb.home, "wy_", "sk-"} {
		if strings.Contains(out, bad) {
			t.Errorf("output holds %q", bad)
		}
	}
	// The event comes from the argument or the payload; other events say nothing.
	if sb.context(nil, start) != out {
		t.Error("the payload's SessionStart is enough")
	}
	if o := sb.context([]string{"Stop"}, `{"session_id":"sess-c1","hook_event_name":"Stop"}`); o != "" {
		t.Errorf("Stop: %q", o)
	}
	if o := sb.context([]string{"SessionStart"}, "not json"); o != "" {
		t.Errorf("bad payload: %q", o)
	}

	// `wy peers off` and a paused matrix silence it.
	off := filepath.Join(sb.state(), "peers", "off")
	if err := os.MkdirAll(filepath.Dir(off), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(off, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if o := sb.context([]string{"SessionStart"}, start); o != "" {
		t.Errorf("peers off: %q", o)
	}
	_ = os.Remove(off)
	pinMatrix(t, sb, 3, true, [][]string{{sandboxAccount, other}})
	if o := sb.context([]string{"SessionStart"}, start); o != "" {
		t.Errorf("paused: %q", o)
	}

	// It never writes a record or starts anything.
	if n := len(sb.records()); n != 0 || len(sb.spawns) != 0 {
		t.Errorf("records %d, spawns %v", n, sb.spawns)
	}
}

func TestReachesOther(t *testing.T) {
	b := keys.MatrixBody{Allow: [][]string{{"a", "b"}, {"c", "a"}}}
	if !peers.ReachesOther(b, "a") || peers.ReachesOther(b, "b") || peers.ReachesOther(b, "") {
		t.Error("reaches")
	}
	b.Paused = true
	if peers.ReachesOther(b, "a") {
		t.Error("paused reaches")
	}
}

// A session Smart Account Switch moved gets its one-line note once, on its resume, with peers
// off too; other sessions get nothing about the feature (setup.md 8.6, The moved session).
func TestMoveNoteOnResume(t *testing.T) {
	sb := newSandbox(t)
	note := "Watchyard moved this session from claude-4 to claude-2 at 10:31 (5h would have hit at 10:48, before its 13:59 reset). Background shells started before are still running but no longer tracked here: check their output files."
	if err := mover.NewState(sb.state()).WriteNote("sess-m1", note, time.Now()); err != nil {
		t.Fatal(err)
	}
	if out := sb.context([]string{"SessionStart"}, `{"session_id":"sess-other","hook_event_name":"SessionStart","source":"startup"}`); out != "" {
		t.Fatalf("another session: %q", out)
	}
	out := sb.context([]string{"SessionStart"}, `{"session_id":"sess-m1","hook_event_name":"SessionStart","source":"resume"}`)
	var got struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.HookSpecificOutput.AdditionalContext != note {
		t.Fatalf("resume: %q (%v)", out, err)
	}
	if out := sb.context([]string{"SessionStart"}, `{"session_id":"sess-m1","hook_event_name":"SessionStart","source":"resume"}`); out != "" {
		t.Fatalf("the note is said once: %q", out)
	}
}
