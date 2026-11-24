package devserver

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GET /v1/switches mock (setup.md 8.6 Switch log, api.yaml listSwitches): a made-up log in the
// hub's shape, so the board's Switches page and a session's path have something to draw. The
// filters, the summary and the paging are the hub's; the cursor is an offset here.

type mockMove struct {
	id, machine, machineName, session, name, kind, from, to, reason, window, errText string
	at                                                                               time.Time
	interactive, auto, crossOrg, known                                               bool
	p5, p7, cap5, cap7, burn5                                                        *float64
}

func fp(v float64) *float64 { return &v }

func mockMoves(now time.Time) []mockMove {
	const s4, s5 = "c41e0000-0000-4000-8000-000000000004", "c41e0000-0000-4000-8000-000000000005"
	h := func(n float64) time.Time { return now.Add(-time.Duration(n * float64(time.Hour))) }
	return []mockMove{
		{id: "mv01", at: h(30), machine: "m_laptop", machineName: "laptop", session: "c41e0000-0000-4000-8000-0000000000aa", name: "old-job", kind: "headless", from: "client", to: "personal",
			auto: true, reason: "weekly 92% >= 90", window: "7d", p7: fp(92), cap7: fp(90), burn5: fp(4.1)},
		{id: "mv02", at: h(6), machine: "m_laptop", machineName: "laptop", session: s4, name: "deploy", kind: "background", from: "client", to: "personal", known: true,
			auto: true, reason: "5h 97% >= 95", window: "5h", p5: fp(97), cap5: fp(95), p7: fp(60), cap7: fp(90), burn5: fp(21.5)},
		{id: "mv03", at: h(5), machine: "m_laptop", machineName: "laptop", session: s4, name: "deploy", kind: "background", from: "personal", to: "work", known: true, crossOrg: true,
			auto: true, reason: "5h 96% >= 95", window: "5h", p5: fp(96), cap5: fp(95), burn5: fp(18)},
		{id: "mv04", at: h(3), machine: "m_gpu", machineName: "gpu-box", session: s5, name: "backfill", kind: "tmux", interactive: true, from: "work", to: "client", known: true,
			auto: true, reason: "stopped at the 5h limit", window: "5h", p5: fp(100), cap5: fp(95), burn5: fp(30)},
		{id: "mv05", at: h(2), machine: "m_gpu", machineName: "gpu-box", session: s5, name: "backfill", kind: "tmux", interactive: true, from: "client", to: "personal", known: true,
			auto: false, reason: "by hand"},
		{id: "mv06", at: h(1), machine: "m_laptop", machineName: "laptop", session: s4, name: "deploy", kind: "background", from: "work", to: "client", known: true,
			auto: true, reason: "5h 98% >= 95", window: "5h", p5: fp(98), cap5: fp(95), burn5: fp(25), errText: "claude --resume failed: exit status 1"},
	}
}

func (s *Server) switchesRoutes() {
	s.mux.HandleFunc("GET /v1/switches", s.guard(func(w http.ResponseWriter, r *http.Request, _ *caller) {
		q := r.URL.Query()
		now := s.opts.Now().UTC()
		var since, until time.Time
		for name, dst := range map[string]*time.Time{"since": &since, "until": &until} {
			if v := q.Get(name); v != "" {
				t, err := time.Parse(time.RFC3339, v)
				if err != nil {
					writeErr(w, 400, "bad_request", name+" must be an RFC 3339 time")
					return
				}
				*dst = t
			}
		}
		limit := 50
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 200 {
				writeErr(w, 400, "bad_request", "limit must be 1-200")
				return
			}
			limit = n
		}
		if m := q.Get("mode"); m != "" && m != "auto" && m != "manual" {
			writeErr(w, 400, "bad_request", "mode must be auto or manual")
			return
		}
		if m := q.Get("result"); m != "" && m != "resumed" && m != "failed" {
			writeErr(w, 400, "bad_request", "result must be resumed or failed")
			return
		}
		all := mockMoves(now)
		sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
		var hit []mockMove
		for _, m := range all {
			switch {
			case q.Get("machine") != "" && q.Get("machine") != m.machine,
				q.Get("session") != "" && q.Get("session") != m.session,
				q.Get("account") != "" && !strings.EqualFold(q.Get("account"), m.from) && !strings.EqualFold(q.Get("account"), m.to),
				q.Get("mode") == "auto" && !m.auto, q.Get("mode") == "manual" && m.auto,
				q.Get("result") == "failed" && m.errText == "", q.Get("result") == "resumed" && m.errText != "",
				!since.IsZero() && m.at.Before(since), !until.IsZero() && !m.at.Before(until):
				continue
			}
			hit = append(hit, m)
		}
		off := 0
		if c := q.Get("cursor"); c != "" {
			n, err := strconv.Atoi(strings.TrimPrefix(c, "o"))
			if err != nil || n < 0 {
				writeErr(w, 400, "bad_request", "bad cursor")
				return
			}
			off = n
		}
		// Move numbers count the moves that worked, oldest first, per session.
		no := map[string]int{}
		num := map[string]int{}
		for i := len(all) - 1; i >= 0; i-- {
			if all[i].errText == "" {
				no[all[i].session]++
				num[all[i].id] = no[all[i].session]
			}
		}
		sum := M{"moves": 0, "failed": 0, "auto": 0, "manual": 0, "sessions": 0, "sessions_twice": 0}
		per := map[string]int{}
		by := map[string][2]int{} // out, in
		cnt := func(k string) { sum[k] = sum[k].(int) + 1 }
		for _, m := range hit {
			if m.errText != "" {
				cnt("failed")
				continue
			}
			cnt("moves")
			if m.auto {
				cnt("auto")
			} else {
				cnt("manual")
			}
			per[m.session]++
			f, t := by[m.from], by[m.to]
			f[0]++
			t[1]++
			by[m.from], by[m.to] = f, t
		}
		sum["sessions"] = len(per)
		for _, n := range per {
			if n >= 2 {
				cnt("sessions_twice")
			}
		}
		accts := []M{}
		for a, c := range by {
			accts = append(accts, M{"account": a, "out": c[0], "in": c[1]})
		}
		sort.Slice(accts, func(i, j int) bool {
			ti, tj := accts[i]["out"].(int)+accts[i]["in"].(int), accts[j]["out"].(int)+accts[j]["in"].(int)
			if ti != tj {
				return ti > tj
			}
			return accts[i]["account"].(string) < accts[j]["account"].(string)
		})
		sum["by_account"] = accts
		page := []M{}
		var next any
		for i := off; i < len(hit); i++ {
			if len(page) == limit {
				next = "o" + strconv.Itoa(i)
				break
			}
			m := hit[i]
			var errv, mno any
			result := "resumed"
			if m.errText != "" {
				errv, result = m.errText, "failed"
			} else {
				mno = num[m.id]
			}
			page = append(page, M{"id": m.id, "at": ts(m.at), "machine_id": m.machine, "machine_name": m.machineName, "session_id": m.session, "session_name": m.name,
				"session_known": m.known, "kind": m.kind, "interactive": m.interactive, "from": m.from, "to": m.to, "auto": m.auto, "reason": m.reason, "window": m.window,
				"p5": m.p5, "p7": m.p7, "cap5": m.cap5, "cap7": m.cap7, "burn5": m.burn5, "cross_org": m.crossOrg, "move_no": mno, "result": result, "error": errv})
		}
		writeJSON(w, 200, M{"moves": page, "next": next, "summary": sum})
	}, "read", "push", "owner"))
}
