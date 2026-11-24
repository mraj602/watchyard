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
	if KindCloud.Movable() || !KindTerminal.Movable() {
		t.Error("movable: terminal yes, cloud no")
	}
}

func TestResumeArgsByKind(t *testing.T) {
	l := Launch{SessionID: "s1", Argv: strings.Fields("--model opus --resume old first")}
	note := "the note"
	for k, want := range map[Kind][]string{
		KindTmux:       {"--model", "opus", "--resume", "s1"},
		KindWrapped:    {"--model", "opus", "--resume", "s1"},
		KindBackground: {"--model", "opus", "--resume", "s1", "--bg", note + PromptTail},
		KindHeadless:   {"-p", "--model", "opus", "--resume", "s1", note + PromptTail},
	} {
		l.Kind = k
		if got := ResumeArgs(l, note); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	// Mid-turn: every kind resumes with the note as its prompt, without the idle tail.
	l.MidTurn = true
	for k, want := range map[Kind][]string{
		KindTmux:       {"--model", "opus", "--resume", "s1", note},
		KindWrapped:    {"--model", "opus", "--resume", "s1", note},
		KindBackground: {"--model", "opus", "--resume", "s1", "--bg", note},
		KindHeadless:   {"-p", "--model", "opus", "--resume", "s1", note},
	} {
		l.Kind = k
		if got := ResumeArgs(l, note); !reflect.DeepEqual(got, want) {
			t.Errorf("mid-turn %s: %q, want %q", k, got, want)
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

// fakeHost records what a move does; it never touches a process.
type fakeHost struct {
	calls  []string
	dead   bool
	killed bool
	fail   string
	// copyUntil: a restarted copy (pid 77) runs until this many StopJob calls.
	copyUntil, stops int
	// stopIgnored: claude stop does not end the process (an older claude).
	stopIgnored bool
}

func (h *fakeHost) rec(s string) error {
	h.calls = append(h.calls, s)
	if h.fail != "" && strings.HasPrefix(s, h.fail) {
		return errors.New("failed")
	}
	return nil
}
func (h *fakeHost) Alive(Launch) bool { return !h.dead && !h.killed }
func (h *fakeHost) Kill(pid int) error {
	h.killed = true
	return h.rec("kill " + strconv.Itoa(pid))
}
func (h *fakeHost) Carry(l Launch, to string) error {
	return h.rec("carry " + l.SessionID + " " + l.Slot + " -> " + to)
}
func (h *fakeHost) StopJob(dir, sid string) error {
	h.stops++
	h.killed = h.killed || !h.stopIgnored
	return h.rec("stop " + sid + " in " + dir)
}
func (h *fakeHost) Copies(dir, sid string, except int) []int {
	if h.copyUntil > h.stops {
		return []int{77}
	}
	return nil
}
func (h *fakeHost) TTY(pid int) string { return "/dev/pts/7" }
func (h *fakeHost) WriteTTY(tty, text string) error {
	return h.rec("tty " + tty + ": " + text)
}
func (h *fakeHost) Tmux(args ...string) error { return h.rec("tmux " + strings.Join(args, " ")) }
func (h *fakeHost) Start(dir string, argv []string, log string) error {
	return h.rec("start " + dir + ": " + strings.Join(argv, " ") + " > " + log)
}
func (h *fakeHost) Order(pid int, o RunOrder) error {
	return h.rec("order " + strconv.Itoa(pid) + ": " + strings.Join(o.Argv, " "))
}
func (h *fakeHost) Note(sid, text string) error { return h.rec("note " + sid) }

func TestExecuteByKind(t *testing.T) {
	defer func(d time.Duration) { Settle = d }(Settle)
	Settle = 0
	defer func(d time.Duration) { AfterWatch = d }(AfterWatch)
	AfterWatch = 0
	tg := Target{Key: "acct:b/o", Label: "claude-2", Dir: "/h/.claude-2"}
	base := Launch{PID: 42, SessionID: "s1", Slot: "/h/.claude-4", CWD: "/w", Argv: []string{"--model", "opus"}, Pane: "%5", Wrapper: 40}
	cmd := "env CLAUDE_CONFIG_DIR=/h/.claude-2 claude --model opus --resume s1"
	for _, c := range []struct {
		kind Kind
		want []string
	}{
		{KindTmux, []string{"note s1", "tmux set-option -p -t %5 remain-on-exit on", "kill 42", "carry s1 /h/.claude-4 -> /h/.claude-2",
			"tmux respawn-pane -k -t %5 -c /w bash -ic '" + cmd + "'", "tmux set-option -p -u -t %5 remain-on-exit"}},
		{KindWrapped, []string{"note s1", "order 40: " + cmd, "kill 42"}},
		{KindBackground, []string{"stop s1 in /h/.claude-4", "carry s1 /h/.claude-4 -> /h/.claude-2", "start /w: " + cmd + " --bg N" + PromptTail + " > "}},
		{KindHeadless, []string{"kill 42", "carry s1 /h/.claude-4 -> /h/.claude-2", "start /w: env CLAUDE_CONFIG_DIR=/h/.claude-2 claude -p --model opus --resume s1 N" + PromptTail + " > /log"}},
		// A plain terminal goes on as a background session; with no message nothing is written.
		{KindTerminal, []string{"kill 42", "carry s1 /h/.claude-4 -> /h/.claude-2", "start /w: " + cmd + " --bg N" + PromptTail + " > "}},
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
	// Never stopped: a claude.ai session, a pid that is another process now, and a wrapper
	// order that could not be written.
	for _, k := range []Kind{KindCloud} {
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
	got := CleanEnv([]string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/x", "CLAUDE_CODE_MESSAGING_TOKEN=t", "CLAUDECODE=1", "HOME=/h",
		"CLAUDE_CODE_SESSION_ID=s", "CLAUDE_CODE_CHILD_SESSION=1", "CLAUDE_PID=9", "CLAUDE_CODE_MAX_OUTPUT_TOKENS=8000"})
	// The owner's own settings stay; the calling session's variables go.
	if strings.Join(got, " ") != "PATH=/bin HOME=/h CLAUDE_CODE_MAX_OUTPUT_TOKENS=8000" {
		t.Errorf("CleanEnv: %q", got)
	}
}

// The coordinator's live finding (setup.md 8.6): the old account's supervisor restarts a
// background job whose process died. The move stops the job there and resumes elsewhere only
// once no copy runs; a copy that will not stop blocks the move.
func TestBackgroundMoveStopsOldCopy(t *testing.T) {
	defer func(d time.Duration) { Settle = d }(Settle)
	Settle = 300 * time.Millisecond
	defer func(d time.Duration) { AfterWatch = d }(AfterWatch)
	AfterWatch = 0
	tg := Target{Key: "acct:b/o", Label: "claude-2", Dir: "/h/.claude-2"}
	l := Launch{PID: 42, SessionID: "s1", Slot: "/h/.claude-4", CWD: "/w", Kind: KindBackground}
	h := &fakeHost{copyUntil: 2}
	if _, err := Execute(h, l, tg, "", "N", ""); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(h.calls, "|")
	if !strings.HasPrefix(got, "stop s1 in /h/.claude-4|stop s1 in /h/.claude-4|carry s1") || !strings.Contains(got, "|start /w:") {
		t.Fatalf("steps %s", got)
	}
	// claude stop that does not end it: killed after StopWait.
	defer func(d time.Duration) { StopWait = d }(StopWait)
	StopWait = 50 * time.Millisecond
	h = &fakeHost{stopIgnored: true}
	if _, err := Execute(h, l, tg, "", "N", ""); err != nil || !strings.HasPrefix(strings.Join(h.calls, "|"), "stop s1 in /h/.claude-4|kill 42|stop s1 in /h/.claude-4|carry") {
		t.Fatalf("stop ignored: %v %q", err, h.calls)
	}
	h = &fakeHost{copyUntil: 99}
	if _, err := Execute(h, l, tg, "", "N", ""); err == nil || !strings.Contains(err.Error(), "keeps a copy running") {
		t.Fatalf("stubborn copy: %v", err)
	}
	for _, c := range h.calls {
		if strings.HasPrefix(c, "start") {
			t.Fatalf("resumed while a copy ran: %v", h.calls)
		}
	}
}

func TestHasPrompt(t *testing.T) {
	for argv, want := range map[string]bool{
		"--model opus":                        false,
		"--model opus do-it":                  true,
		"--add-dir a b --model opus":          false,
		"--resume abc":                        false,
		"--resume abc note":                   true,
		"-p --output-format json task":        true,
		"--permission-mode bypassPermissions": false,
		"-- task":                             true,
	} {
		if got := HasPrompt(strings.Fields(argv)); got != want {
			t.Errorf("%q: %v, want %v", argv, got, want)
		}
	}
}

// e2e v2: the old account's supervisor restarted a job killed mid-turn after the move's watch
// ended. Each later pass stops such a copy for ReapWindow after the latest background move.
func TestReapStopsLateCopies(t *testing.T) {
	st := State{Dir: t.TempDir()}
	now := time.Now()
	_ = st.Append(LogEntry{At: now.Add(-5 * time.Minute), SessionID: "s1", From: "a", To: "b", FromDir: "/h/.claude-4", ToDir: "/h/.claude-2", Kind: KindBackground})
	_ = st.Append(LogEntry{At: now.Add(-5 * time.Minute), SessionID: "s2", From: "a", To: "b", FromDir: "/h/.claude-4", ToDir: "/h/.claude-2", Kind: KindTmux})
	_ = st.Append(LogEntry{At: now.Add(-time.Hour), SessionID: "s3", From: "a", To: "b", FromDir: "/h/.claude-4", ToDir: "/h/.claude-2", Kind: KindBackground})
	h := &fakeHost{copyUntil: 1}
	got := Reap(h, st, now)
	if len(got) != 1 || got[0].SessionID != "s1" {
		t.Fatalf("reaped %+v", got)
	}
	if c := strings.Join(h.calls, "|"); !strings.HasPrefix(c, "stop s1 in /h/.claude-4") {
		t.Errorf("calls %s", c)
	}
	// No copy: nothing to do.
	h = &fakeHost{}
	if got := Reap(h, st, now); len(got) != 0 || len(h.calls) != 0 {
		t.Errorf("no copy: %+v %v", got, h.calls)
	}
}

// A copy that the old account's supervisor restarts after the resume is stopped too.
func TestWatchOldStopsCopyAfterResume(t *testing.T) {
	defer func(d time.Duration) { AfterWatch = d }(AfterWatch)
	AfterWatch = 1500 * time.Millisecond
	h := &fakeHost{copyUntil: 1}
	watchOld(h, Launch{PID: 42, SessionID: "s1", Slot: "/h/.claude-4", Kind: KindBackground})
	if c := strings.Join(h.calls, "|"); c != "stop s1 in /h/.claude-4" {
		t.Errorf("calls %s", c)
	}
}

// Owner correction to v2: an interactive session is moved, and the human sees a message where
// it ran: above the resumed session in its tmux pane, printed by the wy run wrapper, or
// written to its plain terminal, which the session leaves for a background session.
func TestExecuteHumanMessage(t *testing.T) {
	defer func(d time.Duration) { Settle = d }(Settle)
	Settle = 0
	tg := Target{Key: "acct:b/o", Label: "claude-2", Dir: "/h/.claude-2"}
	l := Launch{PID: 42, SessionID: "s1", Slot: "/h/.claude-4", CWD: "/w", Pane: "%5", Wrapper: 40, MidTurn: true,
		Message: "[Watchyard] This session moved"}
	cmd := "env CLAUDE_CONFIG_DIR=/h/.claude-2 claude --resume s1 N"

	l.Kind = KindTmux
	h := &fakeHost{}
	if _, err := Execute(h, l, tg, "", "N", ""); err != nil {
		t.Fatal(err)
	}
	want := "tmux respawn-pane -k -t %5 -c /w bash -ic " + Shell([]string{"printf '\\n%s\\n\\n' " + Shell([]string{l.Message}) + "; sleep 4; " + cmd})
	if !strings.Contains(strings.Join(h.calls, "|"), want) || strings.Contains(strings.Join(h.calls, "|"), "note s1") {
		t.Errorf("tmux: %q\nwant %s (mid-turn: the note is the prompt, no SessionStart note)", h.calls, want)
	}

	l.Kind = KindTerminal
	h = &fakeHost{}
	if _, err := Execute(h, l, tg, "", "N", ""); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(h.calls, "|")
	if !strings.HasSuffix(got, "|start /w: "+cmd[:len(cmd)-2]+" --bg N > |tty /dev/pts/7: "+l.Message) {
		t.Errorf("terminal: %s", got)
	}

	l.Kind = KindWrapped
	o := &orderHost{}
	if _, err := Execute(o, l, tg, "", "N", ""); err != nil || o.order.Message != l.Message {
		t.Errorf("wrapped: %v %+v", err, o.order)
	}
}

type orderHost struct {
	fakeHost
	order RunOrder
}

func (h *orderHost) Order(pid int, o RunOrder) error { h.order = o; return nil }

// Mid-turn, a background session is stopped, never killed first: its supervisor restarts a
// killed job (e2e v2). It is killed only when it outlives MidTurnStopWait.
func TestBackgroundMidTurnStopsFirst(t *testing.T) {
	defer func(d time.Duration) { Settle = d }(Settle)
	Settle = 0
	defer func(d time.Duration) { AfterWatch = d }(AfterWatch)
	AfterWatch = 0
	tg := Target{Key: "acct:b/o", Label: "claude-2", Dir: "/h/.claude-2"}
	l := Launch{PID: 42, SessionID: "s1", Slot: "/h/.claude-4", CWD: "/w", Kind: KindBackground, MidTurn: true}
	h := &fakeHost{}
	if _, err := Execute(h, l, tg, "", "N", ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.calls, "|"); !strings.HasPrefix(got, "stop s1 in /h/.claude-4|carry") {
		t.Errorf("steps %s", got)
	}
	defer func(d time.Duration) { MidTurnStopWait = d }(MidTurnStopWait)
	MidTurnStopWait = 50 * time.Millisecond
	h = &fakeHost{stopIgnored: true}
	if _, err := Execute(h, l, tg, "", "N", ""); err != nil || !strings.HasPrefix(strings.Join(h.calls, "|"), "stop s1 in /h/.claude-4|kill 42|stop s1 in /h/.claude-4|carry") {
		t.Fatalf("stop ignored: %v %q", err, h.calls)
	}
}

// claude stop takes the job's short id: with the session id it matches nothing (e2e v2).
func TestJobID(t *testing.T) {
	agents := []byte(`[{"id":"b4c4db81","sessionId":"b4c4db81-fae9-401e-9961-db0875b0c674","kind":"background"},{"pid":3,"sessionId":"x"}]`)
	for _, c := range []struct{ sid, want string }{
		{"b4c4db81-fae9-401e-9961-db0875b0c674", "b4c4db81"},
		{"14cbe57e-1688-4224-9d88-63598181b252", "14cbe57e"},
		{"short", "short"},
	} {
		if got := JobID(c.sid, agents); got != c.want {
			t.Errorf("%s: %q, want %q", c.sid, got, c.want)
		}
	}
	if got := JobID("abcdefgh-1", []byte("not json")); got != "abcdefgh" {
		t.Errorf("bad json: %q", got)
	}
}
