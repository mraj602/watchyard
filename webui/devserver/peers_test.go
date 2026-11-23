package devserver

import "testing"

const (
	acctA = "3f1c6a2e-0000-4000-8000-000000000001"
	acctB = "3f1c6a2e-0000-4000-8000-000000000011"
)

// PE3: the matrix, pause and audit endpoints follow api.yaml and the hub's who-may-do-what.
func TestPeersMatrixPauseAudit(t *testing.T) {
	schemas := loadSchemas(t)
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)
	phone := newClient(t, ts)
	phone.cookie(PhoneToken)
	board := newClient(t, ts)
	board.cookie(DefaultToken)

	m := owner.want(200, "GET", "/v1/peers/matrix", nil)
	check(t, schemas, "MatrixInfo", "matrix", m)
	check(t, schemas, "Matrix", "matrix.matrix", m["matrix"])
	if v := m["matrix"].(map[string]any)["version"]; v != float64(1) {
		t.Fatalf("version %v, want 1", v)
	}
	// A read-only board sign-in reads but cannot change anything.
	board.want(200, "GET", "/v1/peers/matrix", nil)
	board.want(403, "PUT", "/v1/peers/matrix", M{"edit": M{"deny": [][]string{{acctA, acctB}}}})

	// Widening needs the owner session; a phone gets 403 and the version stays.
	phone.want(403, "PUT", "/v1/peers/matrix", M{"edit": M{"allow": [][]string{{acctA, acctB}}}})
	m = owner.want(200, "PUT", "/v1/peers/matrix", M{"edit": M{"base_version": 1, "allow": [][]string{{acctA, acctB}, {acctB, acctA}}}})
	check(t, schemas, "MatrixInfo", "matrix", m)
	if v := m["matrix"].(map[string]any)["version"]; v != float64(2) {
		t.Fatalf("version %v, want 2", v)
	}
	if m["widens"] != true {
		t.Fatalf("an added pair must widen: %v", m)
	}
	// A stale base version is a 409.
	owner.want(409, "PUT", "/v1/peers/matrix", M{"edit": M{"base_version": 1, "deny": [][]string{{acctA, acctB}}}})

	// Narrowing is open to a phone.
	m = phone.want(200, "PUT", "/v1/peers/matrix", M{"edit": M{"base_version": 2, "deny": [][]string{{acctB, acctA}}}})
	if m["widens"] != false || len(m["matrix"].(map[string]any)["allow"].([]any)) != 1 {
		t.Fatalf("deny one direction leaves one pair: %v", m)
	}

	// The kill switch: any device pauses, only an owner resumes.
	m = phone.want(200, "POST", "/v1/peers/pause", M{"paused": true})
	if m["matrix"].(map[string]any)["paused"] != true {
		t.Fatalf("not paused: %v", m)
	}
	phone.want(403, "POST", "/v1/peers/pause", M{"paused": false})
	owner.want(200, "POST", "/v1/peers/pause", M{"paused": false})

	// The turn limit: 0 is no limit (the default) and the highest value (keys.md 5.2).
	if v := m["matrix"].(map[string]any)["max_turns"]; v != float64(0) {
		t.Fatalf("default max_turns %v, want 0 (no limit)", v)
	}
	m = phone.want(200, "PUT", "/v1/peers/matrix", M{"edit": M{"max_turns": 40}})
	if m["widens"] != false || m["matrix"].(map[string]any)["max_turns"] != float64(40) {
		t.Fatalf("no limit → 40 narrows: %v", m)
	}
	phone.want(403, "PUT", "/v1/peers/matrix", M{"edit": M{"max_turns": 0}})
	phone.want(403, "PUT", "/v1/peers/matrix", M{"edit": M{"max_turns": 99}})
	owner.want(400, "PUT", "/v1/peers/matrix", M{"edit": M{"max_turns": 1}})
	m = owner.want(200, "PUT", "/v1/peers/matrix", M{"edit": M{"max_turns": 0}})
	if m["widens"] != true || m["matrix"].(map[string]any)["max_turns"] != float64(0) {
		t.Fatalf("40 → no limit widens: %v", m)
	}

	a := owner.want(200, "GET", "/v1/peers/audit", nil)
	var events []string
	each(t, a["entries"], func(e any) {
		check(t, schemas, "PeerAuditEntry", "audit", e)
		events = append(events, e.(map[string]any)["event"].(string))
	})
	want := []string{"matrix", "matrix", "resume", "pause", "matrix", "matrix", "loop_guard", "matrix"} // newest first
	if len(events) != len(want) {
		t.Fatalf("audit events %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("audit events %v, want %v", events, want)
		}
	}
	board.want(200, "GET", "/v1/peers/audit", nil)
}

// "Resume thread" (peers.md 6.3): owner only, 404 for an unknown thread, an audit row.
func TestPeersThreadResume(t *testing.T) {
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)
	phone := newClient(t, ts)
	phone.cookie(PhoneToken)
	board := newClient(t, ts)
	board.cookie(DefaultToken)
	path := "/v1/peers/threads/" + mockThread + "/resume"

	phone.want(403, "POST", path, nil)
	board.want(403, "POST", path, nil)
	owner.want(404, "POST", "/v1/peers/threads/t_ffffffffffffffff/resume", nil)
	m := owner.want(200, "POST", path, nil)
	if m["thread"] != mockThread || m["was_paused"] != true || m["machines"] != float64(2) {
		t.Fatalf("resume: %v", m)
	}
	a := owner.want(200, "GET", "/v1/peers/audit", nil)
	entries := a["entries"].([]any)
	first, second := entries[0].(map[string]any), entries[1].(map[string]any)
	if first["event"] != "thread_resume_refused" || first["thread"] != mockThread || second["event"] != "thread_resume" ||
		second["thread"] != mockThread {
		t.Fatalf("newest audit entries %v, %v", first, second)
	}
	// A machine kept its stop, so the thread is paused again.
	if m := owner.want(200, "POST", path, nil); m["was_paused"] != true {
		t.Fatalf("second resume: %v", m)
	}
}

// The directory carries the sessions' tags, and the list shows the same tags (pwa.md 3.0b, 3.1).
func TestPeersDirectoryTags(t *testing.T) {
	schemas := loadSchemas(t)
	_, ts := newServer(t)
	board := newClient(t, ts)
	board.cookie(DefaultToken)

	list := board.want(200, "GET", "/v1/peers", nil)
	held := map[string][]any{}
	each(t, list["sessions"], func(p any) {
		check(t, schemas, "PeerSession", "peer", p)
		m := p.(map[string]any)
		held[m["session"].(string)] = m["tags"].([]any)
	})
	if len(held) == 0 {
		t.Fatal("no live sessions in the directory")
	}
	// Every tag of the sessions list is in the directory, and a session with none has none.
	sessions := board.want(200, "GET", "/v1/sessions", nil)
	tagged := 0
	each(t, sessions["sessions"], func(x any) {
		s := x.(map[string]any)
		check(t, schemas, "Session", "session", s)
		tags, has := s["tags"].([]any)
		if has {
			tagged++
			if len(tags) == 0 {
				t.Fatalf("tags must be omitted when none: %v", s["id"])
			}
		}
		if dir, ok := held[s["id"].(string)]; ok && len(dir) != len(tags) {
			t.Fatalf("session %v: tags %v in the list, %v in the directory", s["id"], tags, dir)
		}
	})
	if tagged == 0 {
		t.Fatal("no session carries tags")
	}
}

// Trusted senders (peers.md 4.1a): readable by a read-only sign-in; revoking narrows, needs an
// owner or a push device, is idempotent, 404 for a machine with no entry, and is audited.
func TestPeersTrustedSenders(t *testing.T) {
	schemas := loadSchemas(t)
	_, ts := newServer(t)
	owner := newClient(t, ts)
	owner.cookie(OwnerToken)
	phone := newClient(t, ts)
	phone.cookie(PhoneToken)
	board := newClient(t, ts)
	board.cookie(DefaultToken)

	list := board.want(200, "GET", "/v1/peers/trusted", nil)
	revoked := 0
	each(t, list["senders"], func(x any) {
		check(t, schemas, "TrustedSender", "sender", x)
		if x.(map[string]any)["revoked_at"] != nil {
			revoked++
		}
	})
	if n := len(list["senders"].([]any)); n < 2 || revoked != 1 {
		t.Fatalf("want a live and a revoked sender, got %d senders, %d revoked", n, revoked)
	}

	body := M{"machine": "m_laptop", "sender": "doorbell"}
	board.want(403, "POST", "/v1/peers/trusted/revoke", body)
	owner.want(400, "POST", "/v1/peers/trusted/revoke", M{"machine": "m_laptop", "sender": "other"})
	owner.want(404, "POST", "/v1/peers/trusted/revoke", M{"machine": "m_nowhere", "sender": "doorbell"})
	got := phone.want(200, "POST", "/v1/peers/trusted/revoke", body)
	check(t, schemas, "TrustedSender", "revoked", got)
	if got["revoked_at"] == nil || got["revoked_by"] == nil {
		t.Fatalf("not revoked: %v", got)
	}
	// Idempotent: the first time stays.
	again := owner.want(200, "POST", "/v1/peers/trusted/revoke", body)
	if again["revoked_at"] != got["revoked_at"] {
		t.Fatalf("second revoke changed the time: %v vs %v", again["revoked_at"], got["revoked_at"])
	}
	a := owner.want(200, "GET", "/v1/peers/audit", nil)
	if e := a["entries"].([]any)[0].(map[string]any); e["event"] != "trust_revoke" {
		t.Fatalf("newest audit entry %v", e)
	}
	n := 0
	each(t, a["entries"], func(e any) {
		if e.(map[string]any)["event"] == "trust_revoke" {
			n++
		}
	})
	if n != 1 {
		t.Fatalf("%d trust_revoke rows, want 1", n)
	}
}
