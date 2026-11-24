package status

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/state"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/record"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

type fixtureEvent struct {
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
}

// loadRun reads testdata/hooks/runs/<name>.jsonl.
func loadRun(t *testing.T, name string) []fixtureEvent {
	t.Helper()
	f, err := os.Open("../../testdata/hooks/runs/" + name + ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []fixtureEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e fixtureEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func sessionOf(t *testing.T, evs []fixtureEvent) string {
	t.Helper()
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil || p.SessionID == "" {
		t.Fatal("no session id", err)
	}
	return p.SessionID
}

const machine = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K"

// feed appends the first n events of a run (all when n < 0) as hook_event records, one
// second apart from start, and returns the session id and the time of the last one.
func feed(t *testing.T, ob *outbox.Outbox, run string, n int, start time.Time, slot string, seat *record.Seat) (string, time.Time) {
	t.Helper()
	evs := loadRun(t, run)
	if n < 0 || n > len(evs) {
		n = len(evs)
	}
	last := start
	for i, e := range evs[:n] {
		last = start.Add(time.Duration(i) * time.Second)
		r, err := record.NewHookEvent(record.Meta{Machine: machine, Slot: slot, Seat: seat, Now: last, Client: "wy/test"},
			e.Payload, record.HookOptions{LastMessages: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ob.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	return sessionOf(t, evs), last
}

func indexOfEvent(t *testing.T, run, name string) int {
	t.Helper()
	for i, e := range loadRun(t, run) {
		if e.Event == name {
			return i
		}
	}
	t.Fatalf("%s has no %s", run, name)
	return -1
}

func findRow(rows []Row, sid string) *Row {
	for i := range rows {
		if rows[i].SessionID == sid {
			return &rows[i]
		}
	}
	return nil
}

func TestStatusTableFromHookRuns(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	seat1 := &record.Seat{Account: "aaaaaaaa-0000-4000-8000-000000000001", Org: "bbbbbbbb-0000-4000-8000-000000000001", Email: "user1@example.com", Src: "claude_json"}
	seat2 := &record.Seat{Account: "aaaaaaaa-0000-4000-8000-000000000002", Org: "bbbbbbbb-0000-4000-8000-000000000002", Email: "user2@example.com", Src: "claude_json"}

	// A: stopped at the permission prompt, B: asked a question, C: failed, D: working,
	// E: finished a turn, F: ended.
	a, _ := feed(t, ob, "02-permission-needed", indexOfEvent(t, "02-permission-needed", "PermissionRequest")+1, t0, "~/.claude", seat1)
	b, _ := feed(t, ob, "03-question", indexOfEvent(t, "03-question", "SessionEnd"), t0.Add(1*time.Minute), "~/.claude-2", seat2)
	c, _ := feed(t, ob, "06-stop-failure", indexOfEvent(t, "06-stop-failure", "SessionEnd"), t0.Add(2*time.Minute), "~/.claude-2", seat2)
	d, _ := feed(t, ob, "01-write-and-bash", indexOfEvent(t, "01-write-and-bash", "UserPromptSubmit")+1, t0.Add(3*time.Minute), "~/.claude", seat1)
	e, _ := feed(t, ob, "04-subagent", indexOfEvent(t, "04-subagent", "SessionEnd"), t0.Add(4*time.Minute), "~/.claude", seat1)
	f, _ := feed(t, ob, "08-plain-with-env", -1, t0.Add(5*time.Minute), "~/.claude-3", nil)

	// Context % from a limit_sample of D (the newest wins).
	for i, pct := range []float64{20, 37} {
		ls, err := record.NewLimitSample(record.Meta{Machine: machine, Slot: "~/.claude", Seat: seat1, Now: t0.Add(time.Duration(10+i) * time.Minute)},
			[]byte(`{"session_id":"`+d+`","context_window":{"used_percentage":`+itoa(int(pct))+`}}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ob.Append(ls); err != nil {
			t.Fatal(err)
		}
	}

	now := t0.Add(30 * time.Minute)
	opts := Options{OutboxDir: ob.Dir, Home: "/home/user", Labels: map[string]string{"~/.claude": "work"}, Now: now}
	rows, err := Collect(opts)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range rows {
		order = append(order, r.SessionID)
	}
	// Needs-you first (longest waiting first), then working, then done; ended is hidden.
	want := []string{a, b, c, d, e}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order %v\nwant  %v", order, want)
	}
	for sid, st := range map[string]state.State{a: state.NeedsPermission, b: state.Asked, c: state.Failed, d: state.Working, e: state.Done} {
		if r := findRow(rows, sid); r == nil || r.State != st {
			t.Errorf("session %s: %+v, want state %s", sid[:8], r, st)
		}
	}
	if r := findRow(rows, c); r.Detail != "model_not_found" || r.Label() != "failed:model_not_found" || !r.NeedsYou {
		t.Errorf("failed row %+v", r)
	}
	if r := findRow(rows, b); r.Question == "" || !r.NeedsYou {
		t.Errorf("asked row has no question: %+v", r)
	}
	ra := findRow(rows, a)
	if ra.Account != "work" || ra.Slot != "~/.claude" {
		t.Errorf("label: %+v", ra)
	}
	if rb := findRow(rows, b); rb.Account != "user2@example.com" {
		t.Errorf("no label falls back to the e-mail: %q", rb.Account)
	}
	if rd := findRow(rows, d); rd.ContextPct == nil || *rd.ContextPct != 37 {
		t.Errorf("context pct: %+v", rd.ContextPct)
	}
	if ra.ContextPct != nil {
		t.Error("session without a sample must have no context pct")
	}
	if ra.Project == "" || ra.Project == "-" || ra.Project != filepath.Base(ra.Cwd) {
		t.Errorf("project/cwd %q %q", ra.Project, ra.Cwd)
	}

	// --all adds the ended one, last.
	opts.All = true
	all, err := Collect(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 6 || all[5].SessionID != f || all[5].State != state.Ended {
		t.Fatalf("--all: %d rows, last %+v", len(all), all[len(all)-1])
	}
	if all[5].Account != "-" {
		t.Errorf("no seat and no label: %q", all[5].Account)
	}

	// Rendering: header, one line per row, the summary.
	var buf bytes.Buffer
	if err := Render(&buf, rows, now); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, w := range []string{"ACCOUNT", "STATE", "!needs_permission", "!asked", "!failed:model_not_found", "working", "37%", "5 sessions, 3 need you"} {
		if !strings.Contains(out, w) {
			t.Errorf("table lacks %q:\n%s", w, out)
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestGoneFromCompleteLists(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	seat := &record.Seat{Account: "a", Org: "o"}
	sid, last := feed(t, ob, "01-write-and-bash", indexOfEvent(t, "01-write-and-bash", "UserPromptSubmit")+1, t0, "~/.claude", seat)
	other, _ := feed(t, ob, "04-subagent", indexOfEvent(t, "04-subagent", "Stop")+1, t0, "~/.claude-2", seat)

	list := func(at time.Time, slot string, complete bool, sessions ...string) {
		var items []json.RawMessage
		for _, s := range sessions {
			items = append(items, json.RawMessage(`{"pid":1,"cwd":"/x","kind":"interactive","sessionId":"`+s+`","status":"idle"}`))
		}
		r, err := record.NewSessionList(record.Meta{Machine: machine, Slot: slot, Seat: seat, Now: at},
			record.SessionListPayload{Complete: complete, Source: "claude_agents", Sessions: items})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ob.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	// An incomplete list and lists of another slot never count; two complete misses do.
	list(last.Add(2*time.Minute), "~/.claude", false)
	list(last.Add(3*time.Minute), "~/.claude-2", true, other)
	list(last.Add(5*time.Minute), "~/.claude", true)
	rows, err := Collect(Options{OutboxDir: ob.Dir, Now: last.Add(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if r := findRow(rows, sid); r == nil || r.State != state.Working {
		t.Fatalf("one complete miss must not make it gone: %+v", r)
	}
	list(last.Add(10*time.Minute), "~/.claude", true)
	rows, _ = Collect(Options{OutboxDir: ob.Dir, Now: last.Add(11 * time.Minute)})
	if findRow(rows, sid) != nil {
		t.Fatal("gone session must be hidden without --all")
	}
	rows, _ = Collect(Options{OutboxDir: ob.Dir, Now: last.Add(11 * time.Minute), All: true})
	if r := findRow(rows, sid); r == nil || r.State != state.Gone {
		t.Fatalf("want gone with --all, got %+v", r)
	}
	if r := findRow(rows, other); r == nil || r.State != state.Done {
		t.Fatalf("a listed session stays: %+v", r)
	}
}

func TestListOnlySessionsAndStatus(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	items := []json.RawMessage{
		json.RawMessage(`{"pid":7,"cwd":"/home/user/proj/api","kind":"interactive","sessionId":"s-busy","status":"busy"}`),
		json.RawMessage(`{"pid":8,"cwd":"/home/user/proj/web","kind":"interactive","sessionId":"s-idle","status":"idle"}`),
	}
	r, err := record.NewSessionList(record.Meta{Machine: machine, Slot: "~/.claude-4", Now: t0},
		record.SessionListPayload{Complete: true, Source: "claude_agents", Sessions: items})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ob.Append(r); err != nil {
		t.Fatal(err)
	}
	rows, err := Collect(Options{OutboxDir: ob.Dir, Home: "/home/user", Now: t0.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].State != state.Working || rows[0].Project != "api" || rows[0].Cwd != "~/proj/api" || rows[1].State != state.Done {
		t.Fatalf("%+v", rows)
	}
}

func TestLoadWindowPartialLinesAndKinds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	ob := outbox.New(dir)
	now := t0
	old, _ := feed(t, ob, "07-compact", 1, now.Add(-8*24*time.Hour), "~/.claude", nil)
	fresh, _ := feed(t, ob, "05-tool-failure", 2, now.Add(-time.Hour), "~/.claude", nil)
	tr, _ := record.NewTokenRows(record.Meta{Machine: machine, Now: now}, record.TokenRowsPayload{})
	if _, err := ob.Append(tr); err != nil {
		t.Fatal(err)
	}
	// A write in progress: bytes after the last newline are not read.
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	fh, _ := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0o600)
	fh.WriteString(`{"v":1,"id":"01JA2XQ8R7Y5V0Z6M3K1T9B4CD","kind":"hook_event","ts":"2026-10-12T08:00:00.000Z","machine":"m","payload":{"session_id":"part`)
	fh.Close()

	recs, err := Load(dir, now.Add(-Window))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Kind == record.KindTokenRows {
			t.Fatal("token_rows must be skipped")
		}
	}
	rows, _ := Collect(Options{OutboxDir: dir, Now: now, All: true})
	if findRow(rows, old) != nil {
		t.Error("a record older than 7 days is shown")
	}
	if findRow(rows, fresh) == nil {
		t.Error("fresh session missing")
	}
	if findRow(rows, "part") != nil {
		t.Error("partial line was read")
	}
	// A missing folder is an empty outbox.
	if rows, err := Collect(Options{OutboxDir: filepath.Join(dir, "none"), Now: now}); err != nil || len(rows) != 0 {
		t.Fatalf("%v %v", rows, err)
	}
	var buf bytes.Buffer
	_ = Render(&buf, nil, now)
	if !strings.Contains(buf.String(), "No sessions") {
		t.Error(buf.String())
	}
}

func TestAge(t *testing.T) {
	for d, want := range map[time.Duration]string{-time.Second: "0s", 5 * time.Second: "5s", 90 * time.Second: "1m", 3 * time.Hour: "3h", 49 * time.Hour: "2d"} {
		if got := Age(d); got != want {
			t.Errorf("Age(%v) = %q, want %q", d, got, want)
		}
	}
}

// A session resumed under another config folder (identity.md 5.3) belongs to the new slot and
// its seat from its first live record there; the old slot's complete lists no longer count it
// as missing.
func TestSessionMovedToAnotherSlot(t *testing.T) {
	ob := outbox.New(filepath.Join(t.TempDir(), "outbox"))
	old := &record.Seat{Account: "a4", Org: "o", Email: "four@example.test"}
	moved := &record.Seat{Account: "a2", Org: "o", Email: "two@example.test"}
	run := "01-write-and-bash"
	sid, last := feed(t, ob, run, indexOfEvent(t, run, "UserPromptSubmit")+1, t0, "~/.claude-4", old)
	// Resumed under ~/.claude-2: its next prompt arrives from there.
	evs := loadRun(t, run)
	at := last.Add(3 * time.Minute)
	r, err := record.NewHookEvent(record.Meta{Machine: machine, Slot: "~/.claude-2", Seat: moved, Now: at, Client: "wy/test"},
		evs[indexOfEvent(t, run, "UserPromptSubmit")].Payload, record.HookOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ob.Append(r); err != nil {
		t.Fatal(err)
	}
	for _, m := range []time.Duration{5, 10} {
		lr, err := record.NewSessionList(record.Meta{Machine: machine, Slot: "~/.claude-4", Seat: old, Now: at.Add(m * time.Minute)},
			record.SessionListPayload{Complete: true, Source: "claude_agents"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ob.Append(lr); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := Collect(Options{OutboxDir: ob.Dir, Now: at.Add(11 * time.Minute), All: true})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(rows, sid)
	if row == nil || row.Slot != "~/.claude-2" || row.Email != "two@example.test" || row.State == state.Gone {
		t.Fatalf("moved session: %+v", row)
	}
}
