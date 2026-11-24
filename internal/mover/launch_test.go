package mover

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestKeptArgs(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"--model opus --resume abc --permission-mode acceptEdits", "--model opus --permission-mode acceptEdits"},
		{"-r abc --dangerously-skip-permissions first prompt", "--dangerously-skip-permissions"},
		{"--continue --add-dir /a /b --settings s.json", "--add-dir /a /b --settings s.json"},
		{"--session-id 1234 --fork-session --mcp-config a.json b.json --plugin-dir p hello", "--mcp-config a.json b.json --plugin-dir p"},
		{"-p --output-format json --resume=xyz do it", "-p --output-format json"},
		{"--bg --model=sonnet -- the prompt", "--model=sonnet"},
		{"--resume --verbose", "--verbose"},
	} {
		if got := strings.Join(KeptArgs(strings.Fields(c.in)), " "); got != c.want {
			t.Errorf("KeptArgs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDetectKinds(t *testing.T) {
	cl := func(s string) []string { return append([]string{"claude"}, strings.Fields(s)...) }
	for _, c := range []struct {
		d    DetectInput
		want Kind
	}{
		{DetectInput{Cmdline: cl("--model opus"), Pane: "%3"}, KindTmux},
		{DetectInput{Cmdline: cl("--model opus"), Pane: "%3", Wrapper: 77}, KindWrapped},
		{DetectInput{Cmdline: cl("--model opus")}, KindTerminal},
		{DetectInput{Cmdline: cl("-p hi"), Pane: "%3"}, KindHeadless},
		{DetectInput{Cmdline: cl(""), RegKind: "bg"}, KindBackground},
		{DetectInput{Cmdline: cl("--print --sdk-url https://x --session-id cse_1")}, KindCloud},
	} {
		if got := Detect(c.d).Kind; got != c.want {
			t.Errorf("%+v: %s, want %s", c.d, got, c.want)
		}
	}
	for _, k := range []Kind{KindTerminal, KindCloud} {
		if k.Movable() {
			t.Errorf("%s movable", k)
		}
	}
}

func TestResumeArgsByKind(t *testing.T) {
	l := Launch{SessionID: "s1", Argv: strings.Fields("--model opus --resume old first")}
	note := "the note"
	for k, want := range map[Kind][]string{
		KindTmux:       {"--model", "opus", "--resume", "s1"},
		KindWrapped:    {"--model", "opus", "--resume", "s1"},
		KindBackground: {"--model", "opus", "--resume", "s1", "--bg", note},
		KindHeadless:   {"-p", "--model", "opus", "--resume", "s1", note},
	} {
		l.Kind = k
		if got := ResumeArgs(l, note); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
}

func TestCommandTemplate(t *testing.T) {
	tg := Target{Dir: "/home/u/.claude-2", Label: "claude-2"}
	if got := strings.Join(Command("", tg, []string{"--resume", "s"}), " "); got != "env CLAUDE_CONFIG_DIR=/home/u/.claude-2 claude --resume s" {
		t.Errorf("default: %q", got)
	}
	if got := strings.Join(Command("~/bin/claude-launch {n}", tg, []string{"--resume", "s"}), " "); got != "~/bin/claude-launch 2 --resume s" {
		t.Errorf("template: %q", got)
	}
	if SlotNumber("/home/u/.claude") != "1" || SlotNumber("/x/.claude-12") != "12" {
		t.Error("slot numbers")
	}
	if got := Shell([]string{"a b", "it's", "ok"}); got != `'a b' 'it'\''s' ok` {
		t.Errorf("Shell: %s", got)
	}
}

func TestNoteText(t *testing.T) {
	at := time.Date(2026, 11, 24, 10, 31, 0, 0, time.Local)
	mv := Move{Window: Win5h, HitAt: at.Add(17 * time.Minute), ResetAt: time.Date(2026, 11, 24, 13, 59, 0, 0, time.Local)}
	want := "Watchyard moved this session from claude-4 to claude-2 at 10:31 (5h would have hit at 10:48, before its 13:59 reset). " +
		"Background shells started before are still running but no longer tracked here: check their output files."
	if got := Note("claude-4", "claude-2", at, mv); got != want {
		t.Errorf("note:\n%s\nwant\n%s", got, want)
	}
	if got := Note("claude-4", "claude-2", at, Move{}); !strings.HasPrefix(got, "Watchyard moved this session from claude-4 to claude-2 at 10:31. Background") {
		t.Errorf("manual note: %s", got)
	}
}

// fakeHost records what a move does; it never touches a process.
type fakeHost struct {
	calls []string
	dead  bool
	fail  string
}

func (h *fakeHost) rec(s string) error {
	h.calls = append(h.calls, s)
	if h.fail != "" && strings.HasPrefix(s, h.fail) {
		return errors.New("failed")
	}
	return nil
}
func (h *fakeHost) Alive(Launch) bool         { return !h.dead }
func (h *fakeHost) Kill(pid int) error        { return h.rec("kill " + strconv.Itoa(pid)) }
func (h *fakeHost) Tmux(args ...string) error { return h.rec("tmux " + strings.Join(args, " ")) }
func (h *fakeHost) Start(dir string, argv []string, log string) error {
	return h.rec("start " + dir + ": " + strings.Join(argv, " ") + " > " + log)
}
func (h *fakeHost) Order(pid int, o RunOrder) error {
	return h.rec("order " + strconv.Itoa(pid) + ": " + strings.Join(o.Argv, " "))
}
func (h *fakeHost) Note(sid, text string) error { return h.rec("note " + sid) }

func TestExecuteByKind(t *testing.T) {
	tg := Target{Key: "acct:b/o", Label: "claude-2", Dir: "/h/.claude-2"}
	base := Launch{PID: 42, SessionID: "s1", CWD: "/w", Argv: []string{"--model", "opus"}, Pane: "%5", Wrapper: 40}
	cmd := "env CLAUDE_CONFIG_DIR=/h/.claude-2 claude --model opus --resume s1"
	for _, c := range []struct {
		kind Kind
		want []string
	}{
		{KindTmux, []string{"note s1", "tmux set-option -p -t %5 remain-on-exit on", "kill 42",
			"tmux respawn-pane -k -t %5 -c /w bash -ic '" + cmd + "'", "tmux set-option -p -u -t %5 remain-on-exit"}},
		{KindWrapped, []string{"note s1", "order 40: " + cmd, "kill 42"}},
		{KindBackground, []string{"kill 42", "start /w: " + cmd + " --bg N > "}},
		{KindHeadless, []string{"kill 42", "start /w: env CLAUDE_CONFIG_DIR=/h/.claude-2 claude -p --model opus --resume s1 N > /log"}},
	} {
		h := &fakeHost{}
		l := base
		l.Kind = c.kind
		if _, err := Execute(h, l, tg, "", "N", "/log"); err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if !reflect.DeepEqual(h.calls, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.kind, h.calls, c.want)
		}
	}
	// Never stopped: a plain terminal, a claude.ai session, a pid that is another process now,
	// and a wrapper order that could not be written.
	for _, k := range []Kind{KindTerminal, KindCloud} {
		h := &fakeHost{}
		l := base
		l.Kind = k
		if _, err := Execute(h, l, tg, "", "N", ""); !errors.Is(err, ErrNotMovable) || len(h.calls) != 0 {
			t.Errorf("%s: %v %q", k, err, h.calls)
		}
	}
	h := &fakeHost{dead: true}
	l := base
	l.Kind = KindTmux
	if _, err := Execute(h, l, tg, "", "N", ""); err == nil || len(h.calls) != 0 {
		t.Errorf("gone pid: %v %q", err, h.calls)
	}
	h = &fakeHost{fail: "order"}
	l.Kind = KindWrapped
	if _, err := Execute(h, l, tg, "", "N", ""); err == nil || strings.Contains(strings.Join(h.calls, "|"), "kill") {
		t.Errorf("order failed but killed: %q", h.calls)
	}
}

func TestStateFiles(t *testing.T) {
	st := NewState(t.TempDir())
	now := time.Now()
	if err := st.WriteNote("s1", "hello", now); err != nil {
		t.Fatal(err)
	}
	if st.TakeNote("s1", now) != "hello" || st.TakeNote("s1", now) != "" {
		t.Error("a note is taken once")
	}
	_ = st.WriteNote("s2", "old", now.Add(-2*NoteTTL))
	if st.TakeNote("s2", now) != "" {
		t.Error("an old note is dropped")
	}
	if st.WriteNote("../x", "x", now) == nil {
		t.Error("bad session id written")
	}
	if err := st.SetPin("s1", true); err != nil || !st.Pins()["s1"] {
		t.Error("pin")
	}
	if err := st.SetPin("s1", false); err != nil || st.Pins()["s1"] {
		t.Error("unpin")
	}
	if err := st.WriteOrder(7, RunOrder{SessionID: "s1", Argv: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	if o, ok := st.TakeOrder(7); !ok || o.SessionID != "s1" {
		t.Error("order")
	}
	if _, ok := st.TakeOrder(7); ok {
		t.Error("an order is taken once")
	}
	_ = st.Append(LogEntry{At: now, SessionID: "s1", From: "a", To: "b"})
	_ = st.Append(LogEntry{At: now, SessionID: "s2", From: "a", To: "b", Error: "x"})
	if es := st.Log(now.Add(-time.Minute)); len(es) != 1 || es[0].SessionID != "s1" {
		t.Errorf("log: %+v", es)
	}
}

func TestCleanEnv(t *testing.T) {
	got := CleanEnv([]string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/x", "CLAUDE_CODE_MESSAGING_TOKEN=t", "CLAUDECODE=1", "HOME=/h"})
	if strings.Join(got, " ") != "PATH=/bin HOME=/h" {
		t.Errorf("CleanEnv: %q", got)
	}
}
