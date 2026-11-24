package api_test

import (
	"net/url"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
)

// swMove is store.SwitchMove as the client reads it (the time stays a string).
type swMove struct {
	store.SwitchMove
	At string `json:"at"`
}

type swPage struct {
	Moves   []swMove            `json:"moves"`
	Next    *string             `json:"next"`
	Summary store.SwitchSummary `json:"summary"`
}

func moveRecord(t *testing.T, machine string, at time.Time, mod func(p *record.SwitchMovePayload)) []byte {
	t.Helper()
	p5, cap5 := 97.0, 95.0
	p := record.SwitchMovePayload{At: at.Format(time.RFC3339), SessionID: "s1", SessionName: "api", Kind: "background",
		From: "claude-3", To: "claude-2", Auto: true, Reason: "5h 97% >= 95", Window: "5h", P5: &p5, Cap5: &cap5}
	if mod != nil {
		mod(&p)
	}
	r, err := record.NewSwitchMove(record.Meta{Machine: machine, Client: "wy/0.0.0-test", Now: at}, p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// setup.md 8.6 Switch log: the hub keeps each move a machine reports, newest first, with the
// move number in the session's path, the summary of the filter, and filters and paging.
func TestSwitchLog(t *testing.T) {
	h := startHub(t, "")
	mt, rd := h.token("machine"), h.token("phone")
	h.ingest(mt, simpleHook(t, h.machine, "s1", "SessionStart", t0))
	m1 := moveRecord(t, h.machine, t0.Add(1*time.Hour), nil)
	h.ingest(mt,
		m1,
		moveRecord(t, h.machine, t0.Add(2*time.Hour), func(p *record.SwitchMovePayload) { p.From, p.To, p.CrossOrg = "claude-2", "claude-6", true }),
		moveRecord(t, h.machine, t0.Add(3*time.Hour), func(p *record.SwitchMovePayload) { p.From, p.To, p.Error = "claude-6", "claude-3", "claude not found" }),
		moveRecord(t, h.machine, t0.Add(4*time.Hour), func(p *record.SwitchMovePayload) {
			p.SessionID, p.SessionName, p.Kind, p.Interactive, p.Auto, p.Reason, p.Window, p.P5, p.Cap5 = "s2", "web", "tmux", true, false, "by hand", "", nil, nil
			p.From, p.To = "claude-3", "claude-5"
		}))
	// A resent batch makes no second row.
	h.ingest(mt, m1)

	var all swPage
	h.get("/v1/switches", rd, &all)
	if len(all.Moves) != 4 || all.Next != nil {
		t.Fatalf("moves %d next %v", len(all.Moves), all.Next)
	}
	if all.Moves[0].SessionID != "s2" || all.Moves[0].Interactive != true || all.Moves[0].Auto || all.Moves[0].P5 != nil || all.Moves[0].Result != "resumed" {
		t.Fatalf("newest %+v", all.Moves[0])
	}
	failed := all.Moves[1]
	if failed.Result != "failed" || failed.Error == nil || *failed.Error != "claude not found" || failed.MoveNo != nil {
		t.Fatalf("failed %+v", failed)
	}
	second := all.Moves[2]
	if second.MoveNo == nil || *second.MoveNo != 2 || !second.CrossOrg || second.MachineName == "" || !second.SessionKnown {
		t.Fatalf("second %+v", second)
	}
	if first := all.Moves[3]; first.MoveNo == nil || *first.MoveNo != 1 || first.P5 == nil || *first.P5 != 97 || first.Window != "5h" {
		t.Fatalf("first %+v", first)
	}
	if all.Moves[0].SessionKnown {
		t.Fatalf("s2 was never seen by the hub")
	}
	s := all.Summary
	if s.Moves != 3 || s.Failed != 1 || s.Auto != 2 || s.Manual != 1 || s.Sessions != 2 || s.Sessions2 != 1 {
		t.Fatalf("summary %+v", s)
	}
	var c3 *store.SwitchAccountCount
	for i := range s.ByAccount {
		if s.ByAccount[i].Account == "claude-3" {
			c3 = &s.ByAccount[i]
		}
	}
	if c3 == nil || c3.Out != 2 || c3.In != 0 {
		t.Fatalf("by account %+v", s.ByAccount)
	}

	query := func(kv ...string) swPage {
		q := url.Values{}
		for i := 0; i+1 < len(kv); i += 2 {
			q.Set(kv[i], kv[i+1])
		}
		var p swPage
		h.get("/v1/switches?"+q.Encode(), rd, &p)
		return p
	}
	if p := query("session", "s1"); len(p.Moves) != 3 {
		t.Fatalf("session filter %d", len(p.Moves))
	}
	if p := query("account", "CLAUDE-6"); len(p.Moves) != 2 { // case-insensitive, from or to
		t.Fatalf("account filter %d", len(p.Moves))
	}
	if p := query("mode", "manual"); len(p.Moves) != 1 || p.Summary.Moves != 1 {
		t.Fatalf("manual filter %+v", p.Summary)
	}
	if p := query("result", "failed"); len(p.Moves) != 1 || p.Summary.Failed != 1 || p.Summary.Moves != 0 {
		t.Fatalf("failed filter %+v", p.Summary)
	}
	if p := query("machine", "m_nobody"); len(p.Moves) != 0 || p.Summary.Moves != 0 || p.Summary.ByAccount == nil {
		t.Fatalf("machine filter %+v", p)
	}
	if p := query("since", t0.Add(90*time.Minute).Format(time.RFC3339), "until", t0.Add(3*time.Hour).Format(time.RFC3339)); len(p.Moves) != 1 || p.Summary.Moves != 1 {
		t.Fatalf("range %+v", p.Summary)
	}
	// Paging: two at a time, the summary still covers everything.
	p1 := query("limit", "3")
	if len(p1.Moves) != 3 || p1.Next == nil || p1.Summary.Moves != 3 {
		t.Fatalf("page 1 %d %v", len(p1.Moves), p1.Next)
	}
	p2 := query("limit", "3", "cursor", *p1.Next)
	if len(p2.Moves) != 1 || p2.Next != nil || p2.Moves[0].ID != all.Moves[3].ID {
		t.Fatalf("page 2 %+v", p2.Moves)
	}
	for _, bad := range []string{"mode=sometimes", "result=x", "since=yesterday", "limit=0", "limit=500", "cursor=%25"} {
		if code, _ := h.do("GET", "/v1/switches?"+bad, rd, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code, _ := h.do("GET", "/v1/switches", "", nil); code != 401 {
		t.Errorf("no token: %d", code)
	}
}

// A machine that does not send the record has an empty log; a machine cannot log moves of a
// session another machine runs (record.md 5.1).
func TestSwitchLogOwnSessionsOnly(t *testing.T) {
	h := startHub(t, "")
	mt, rd := h.token("machine"), h.token("phone")
	var empty swPage
	h.get("/v1/switches", rd, &empty)
	if len(empty.Moves) != 0 || empty.Summary.Moves != 0 {
		t.Fatalf("empty %+v", empty)
	}
	h.ingest(mt, simpleHook(t, h.machine, "s1", "SessionStart", t0))
	other := newMachineID()
	h.exec(`INSERT INTO machine (id, name, first_seen) VALUES (?, 'other', 0)`, other)
	h.ingest(mt, moveRecord(t, h.machine, t0.Add(time.Hour), nil))
	h.exec(`UPDATE session SET machine_id = ? WHERE id = 's1'`, other)
	h.ingest(mt, moveRecord(t, h.machine, t0.Add(2*time.Hour), nil))
	var got swPage
	h.get("/v1/switches", rd, &got)
	if len(got.Moves) != 1 {
		t.Fatalf("moves %d", len(got.Moves))
	}
	if n := h.count(`SELECT count(*) FROM record WHERE kind = 'switch_move' AND typed = 0 AND type_error LIKE '%another machine%'`); n != 1 {
		t.Fatalf("untyped records %d", n)
	}
}
