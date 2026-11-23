package devserver

import (
	"net/http"
	"slices"
	"strings"
	"time"
)

// PE3 mock (peers.md 5, 6.3, 10, 11): the account matrix, the kill switch, thread resume and
// the audit list. The
// rules are the hub's: an edit that widens (adds a pair, same_account on, resume, more turns)
// needs an owner caller (no turn limit, max_turns 0, is the most turns); narrowing is accepted from owner, push and write callers. The
// documents are not signed here (the signature is a fixed placeholder of the right shape).

const (
	mockFP  = "mmmmmmmmmmmmmmmmmmmmmmmm"
	mockSig = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

type peersState struct {
	version     int
	paused      bool
	sameAccount bool
	maxTurns    int
	allow       [][]string
	signer      string
	madeBy      string
	widens      bool
	at          time.Time
	audit       []M // newest first
	nextAudit   int
	threads     map[string]bool // thread id -> open
	trusted     []M             // the trusted senders (Doorbell), one per machine (peers.md 4.1a)
}

// mockThread is the thread the mock's loop guard stopped (peers.md 6.3).
const mockThread = "t_3a9e5c7d1b2f4a60"

func newPeersState(d *Data, now time.Time) *peersState {
	p := &peersState{version: 1, sameAccount: true, maxTurns: 0, allow: [][]string{}, signer: "hub", madeBy: "hub", at: now.Add(-48 * time.Hour), nextAudit: 1,
		threads: map[string]bool{mockThread: true}}
	p.trusted = []M{
		{"sender": "doorbell", "machine": "m_laptop", "machine_name": "laptop", "account": "3f1c6a2e-0000-4000-8000-000000000001", "account_label": "personal",
			"program": "/home/me/.local/share/doorbell/venv/bin/doorbell", "interpreter": "/usr/bin/python3.12", "venv": "/home/me/.local/share/doorbell/venv",
			"approved_at": ts(now.Add(-6 * 24 * time.Hour)), "revoked_at": nil, "revoked_by": nil, "last_send_at": ts(now.Add(-20 * time.Minute)), "sent_today": 4,
			"recent_tags": []M{{"tag": "deploy", "at": ts(now.Add(-20 * time.Minute))}, {"tag": "review", "at": ts(now.Add(-2 * time.Hour))}, {"tag": "nightly", "at": ts(now.Add(-30 * time.Hour))}}},
		{"sender": "doorbell", "machine": "m_vps", "machine_name": "old-vps", "account": "3f1c6a2e-0000-4000-8000-000000000001", "account_label": "personal",
			"program": "/opt/doorbell/bin/doorbell", "interpreter": "/usr/bin/python3.11", "venv": "/opt/doorbell",
			"approved_at": ts(now.Add(-20 * 24 * time.Hour)), "revoked_at": ts(now.Add(-3 * 24 * time.Hour)), "revoked_by": "cli", "last_send_at": nil, "sent_today": 0,
			"recent_tags": []M{}},
	}
	p.log(now.Add(-48*time.Hour), "hub", "matrix", M{"version": 1, "signer": "hub", "widens": false, "paused": false, "same_account": true, "max_turns": 0, "pairs": 0})
	p.logThread(now.Add(-30*time.Minute), "hub", "loop_guard", mockThread, M{})
	return p
}

func (p *peersState) log(at time.Time, actor, event string, detail M) {
	p.audit = append([]M{{"id": p.nextAudit, "at": ts(at), "actor": actor, "event": event, "thread": nil, "msg": nil, "detail": detail}}, p.audit...)
	p.nextAudit++
}

func (p *peersState) logThread(at time.Time, actor, event, thread string, detail M) {
	p.log(at, actor, event, detail)
	p.audit[0]["thread"] = thread
}

func (s *Server) peersRoutes() {
	m := s.mux.HandleFunc
	m("GET /v1/peers", s.guard(s.listPeers, "owner", "read"))
	m("GET /v1/peers/trusted", s.guard(s.listTrusted, "owner", "read"))
	m("POST /v1/peers/trusted/revoke", s.guard(s.revokeTrusted, "owner", "push"))
	m("GET /v1/peers/matrix", s.guard(s.getMatrix, "owner", "read", "push", "write"))
	m("PUT /v1/peers/matrix", s.guard(s.putMatrix, "owner", "push", "write"))
	m("POST /v1/peers/pause", s.guard(s.pausePeers, "owner", "push", "write"))
	m("GET /v1/peers/audit", s.guard(s.peerAudit, "owner", "read"))
	m("GET /v1/peers/messages", s.guard(s.peerMessages, "owner", "read"))
	m("POST /v1/peers/threads/{id}/resume", s.guard(s.resumeThread, "owner"))
}

// resumeThread is "Resume thread": owner only, 404 for a thread the mock does not know, 409
// for a closed one; the mock tells its two pretend machines.
func (s *Server) resumeThread(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.peers()
	id := r.PathValue("id")
	open, ok := p.threads[id]
	switch {
	case !ok:
		writeErr(w, 404, "not_found", "no such thread")
		return
	case !open:
		writeErr(w, 409, "conflict", "the thread is closed; a new message opens a new one")
		return
	}
	wasPaused := false
	for _, e := range p.audit {
		if e["thread"] == id {
			wasPaused = e["event"] == "loop_guard" || e["event"] == "thread_resume_refused"
			break
		}
	}
	p.logThread(s.opts.Now(), c.kind, "thread_resume", id, M{"was_paused": wasPaused, "machines": 2})
	// One pretend machine keeps hub_may_widen off: it refuses and tells the hub, which marks
	// the thread paused again (peers.md 6.3).
	p.logThread(s.opts.Now(), "dev_gpu", "thread_resume_refused", id, M{"machine": "gpu-box"})
	writeJSON(w, 200, M{"thread": id, "was_paused": wasPaused, "machines": 2})
}

// peers returns the state, made on first use. Call with s.mu held.
func (s *Server) peers() *peersState {
	if s.peerState == nil {
		s.peerState = newPeersState(s.data, s.opts.Now())
	}
	return s.peerState
}

func (s *Server) matrixInfo(p *peersState) M {
	accounts := M{}
	for _, seat := range s.data.Seats {
		if h, _ := seat["hidden"].(bool); h {
			continue
		}
		if a, ok := seat["account"].(string); ok {
			l, _ := seat["label"].(string)
			accounts[a] = l
		}
	}
	allow := make([][]string, len(p.allow))
	copy(allow, p.allow)
	return M{
		"doc":     M{"type": "matrix", "body": "e30", "sigs": []M{{"by": p.signer, "fp": mockFP, "sig": mockSig}}},
		"matrix":  M{"v": 1, "version": p.version, "paused": p.paused, "same_account": p.sameAccount, "max_turns": p.maxTurns, "accounts": accounts, "allow": allow},
		"signer":  p.signer,
		"widens":  p.widens,
		"made_by": p.madeBy,

		"created_at": ts(p.at),
	}
}

func (s *Server) getMatrix(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, 200, s.matrixInfo(s.peers()))
}

type matrixEdit struct {
	BaseVersion *int       `json:"base_version"`
	Allow       [][]string `json:"allow"`
	Deny        [][]string `json:"deny"`
	Paused      *bool      `json:"paused"`
	SameAccount *bool      `json:"same_account"`
	MaxTurns    *int       `json:"max_turns"`
}

func (s *Server) putMatrix(w http.ResponseWriter, r *http.Request, c *caller) {
	var in struct {
		Doc  M           `json:"doc"`
		Edit *matrixEdit `json:"edit"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Edit == nil {
		writeErr(w, 400, "bad_request", "the mock takes an edit only")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyEdit(w, c, *in.Edit)
}

func (s *Server) pausePeers(w http.ResponseWriter, r *http.Request, c *caller) {
	var in struct {
		Paused *bool `json:"paused"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Paused == nil {
		writeErr(w, 400, "bad_request", "paused is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.peers()
	if p.paused == *in.Paused {
		writeJSON(w, 200, s.matrixInfo(p))
		return
	}
	s.applyEdit(w, c, matrixEdit{Paused: in.Paused})
}

// applyEdit builds the next version from an edit; s.mu is held.
func (s *Server) applyEdit(w http.ResponseWriter, c *caller, e matrixEdit) {
	p := s.peers()
	if e.BaseVersion != nil && *e.BaseVersion != p.version {
		writeErr(w, 409, "conflict", "the matrix is at another version")
		return
	}
	next := *p
	next.allow = slices.Clone(p.allow)
	widens := false
	for _, a := range e.Allow {
		if len(a) != 2 || a[0] == "" || a[1] == "" || a[0] == a[1] || a[0] == "*" || a[1] == "*" {
			writeErr(w, 400, "bad_request", "allow entries are [from, to] account uuids")
			return
		}
		if !slices.ContainsFunc(next.allow, func(x []string) bool { return x[0] == a[0] && x[1] == a[1] }) {
			next.allow = append(next.allow, a)
			widens = true
		}
	}
	for _, d := range e.Deny {
		if len(d) != 2 || d[0] == "" || d[1] == "" || (d[0] == "*" && d[1] == "*") {
			writeErr(w, 400, "bad_request", "deny entries are [from, to] account uuids, either side may be *")
			return
		}
		next.allow = slices.DeleteFunc(next.allow, func(x []string) bool {
			return (d[0] == "*" || d[0] == x[0]) && (d[1] == "*" || d[1] == x[1])
		})
	}
	slices.SortFunc(next.allow, func(a, b []string) int { return strings.Compare(a[0]+"\x00"+a[1], b[0]+"\x00"+b[1]) })
	if e.Paused != nil {
		next.paused = *e.Paused
		widens = widens || (p.paused && !*e.Paused)
	}
	if e.SameAccount != nil {
		next.sameAccount = *e.SameAccount
		widens = widens || (!p.sameAccount && *e.SameAccount)
	}
	if e.MaxTurns != nil {
		if n := *e.MaxTurns; n != 0 && (n < 2 || n > 500) {
			writeErr(w, 400, "bad_request", "max_turns is 0 (no limit) or 2 to 500")
			return
		}
		next.maxTurns = *e.MaxTurns
		// 0 is no limit, the highest value: setting it widens, leaving it narrows.
		widens = widens || (p.maxTurns != 0 && p.maxTurns != *e.MaxTurns && (*e.MaxTurns == 0 || *e.MaxTurns > p.maxTurns))
	}
	if widens && !c.has("owner") {
		writeErr(w, 403, "forbidden", "widening the matrix needs the owner key or an owner session")
		return
	}
	now := s.opts.Now()
	next.version = p.version + 1
	next.widens = widens
	next.madeBy = c.kind
	next.at = now
	event := "matrix"
	if len(next.allow) == len(p.allow) && next.sameAccount == p.sameAccount && next.maxTurns == p.maxTurns && next.paused != p.paused {
		event = "resume"
		if next.paused {
			event = "pause"
		}
	}
	next.audit, next.nextAudit = p.audit, p.nextAudit
	next.log(now, c.kind, event, M{"version": next.version, "signer": "hub", "widens": widens, "paused": next.paused,
		"same_account": next.sameAccount, "max_turns": next.maxTurns, "pairs": len(next.allow)})
	*p = next
	writeJSON(w, 200, s.matrixInfo(p))
}

// peerMessages lists message metadata (never text): a fixed few between the mock's accounts.
func (s *Server) peerMessages(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	acct := func(n string) M { return M{"account": "3f1c6a2e-0000-4000-8000-0000000000" + n} }
	writeJSON(w, 200, M{"messages": []M{
		{"id": "pm_3", "thread": "th_1", "from": acct("11"), "to": acct("01"), "kind": "reply", "size": 412, "local": false, "status": "delivered", "detail": nil, "created_at": ts(now.Add(-20 * time.Minute)), "final_at": ts(now.Add(-19 * time.Minute))},
		{"id": "pm_2", "thread": "th_1", "from": acct("01"), "to": acct("11"), "kind": "start", "size": 2300, "local": false, "status": "delivered", "detail": nil, "created_at": ts(now.Add(-25 * time.Minute)), "final_at": ts(now.Add(-24 * time.Minute))},
		{"id": "pm_1", "thread": "th_2", "from": acct("21"), "to": acct("31"), "kind": "start", "size": 90, "local": false, "status": "refused", "detail": "no pair", "created_at": ts(now.Add(-3 * time.Hour)), "final_at": nil},
	}})
}

func (s *Server) peerAudit(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.peers().audit
	if len(entries) > 100 {
		entries = entries[:100]
	}
	writeJSON(w, 200, M{"entries": entries})
}

// listPeers is the directory (peers.md 3): the live sessions of an account, with their tags.
func (s *Server) listPeers(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	accounts := map[string]string{}
	for _, seat := range s.data.Seats {
		id, _ := seat["id"].(string)
		a, _ := seat["account"].(string)
		accounts[id] = a
	}
	out := []M{}
	for _, x := range s.data.Sessions {
		st, _ := x["state"].(string)
		seat, _ := x["seat"].(string)
		if st == "ended" || st == "gone" || accounts[seat] == "" {
			continue
		}
		project, _ := x["project"].(string)
		name := project
		if n, ok := x["name"].(string); ok && n != "" {
			name = n
		}
		label, _ := x["seat_label"].(string)
		machine, _ := x["machine_name"].(string)
		tags, _ := x["tags"].([]string)
		if tags == nil {
			tags = []string{}
		}
		out = append(out, M{"session": x["id"], "handle": name + "@" + label + "." + machine, "name": name, "account": accounts[seat], "account_label": label,
			"machine": x["machine"], "machine_name": machine, "project": nilIfEmpty(project), "state": st, "agent": true, "tags": tags})
	}
	writeJSON(w, 200, M{"sessions": out})
}

// listTrusted lists the trusted senders, revoked ones included (peers.md 4.1a, 10).
func (s *Server) listTrusted(w http.ResponseWriter, r *http.Request, c *caller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, 200, M{"senders": s.peers().trusted})
}

// revokeTrusted narrows: it sets revoked_at once (a second call keeps the first time), writes a
// trust_revoke audit row and answers the entry. 404 for a machine with no entry.
func (s *Server) revokeTrusted(w http.ResponseWriter, r *http.Request, c *caller) {
	var in struct {
		Machine string `json:"machine"`
		Sender  string `json:"sender"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Machine == "" || in.Sender != "doorbell" {
		writeErr(w, 400, "bad_request", "machine and sender (doorbell) are required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.peers()
	for _, t := range p.trusted {
		if t["machine"] != in.Machine {
			continue
		}
		if t["revoked_at"] == nil {
			now := s.opts.Now()
			t["revoked_at"], t["revoked_by"] = ts(now), "dev_"+c.kind
			p.log(now, c.kind, "trust_revoke", M{"machine": in.Machine, "sender": in.Sender})
		}
		writeJSON(w, 200, t)
		return
	}
	writeErr(w, 404, "not_found", "no trusted sender on that machine")
}
