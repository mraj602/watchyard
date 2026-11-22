package devserver

import "time"

// M is a JSON object. Fixtures are plain maps so they carry exactly the fields the API
// spec names and nothing a Go struct would add or drop.
type M = map[string]any

// Data is everything the mock hub serves.
type Data struct {
	Now      time.Time
	Seats    []M
	Sessions []M
	Machines []M
	// StateMachines (fixtures_machines.go): one machine per pull state, appended to Machines by
	// POST /_dev/pull-states.
	StateMachines []M
	// SelfMachine is the machine id the signed-in device is bound to (DELETE answers 409 for it); empty by default.
	SelfMachine string

	// Identity, devices and push (fixtures_devices.go).
	Alerts   []M // full Alert objects, any order
	Devices  []M
	Targets  []M // PushTarget objects; "device" names the owning device
	Rules    []M
	Settings M
	Channels []M
}

// ts formats a time the way the API does: RFC 3339 UTC with milliseconds.
func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// Fixtures builds a believable board around now: four accounts (one near its limit), three
// machines (one quiet), sessions in every state.
func Fixtures(now time.Time) *Data {
	ago := func(d time.Duration) string { return ts(now.Add(-d)) }
	in := func(d time.Duration) string { return ts(now.Add(d)) }
	min, hour := time.Minute, time.Hour

	win := func(key, label string, used, peak float64, resets string, src string, pace M) M {
		w := M{"key": key, "label": label, "used_pct": used, "peak_pct": peak, "resets_at": resets,
			"started_at": nil, "unused_pct": nil, "source": src, "updated_at": ago(40 * time.Second)}
		if pace != nil {
			w["pace"] = pace
		}
		return w
	}
	place := func(mid, name, slot string) M {
		return M{"machine": mid, "machine_name": name, "slot": slot, "tab_id": nil}
	}

	d := &Data{Now: now}
	d.Seats = []M{
		{
			"id": "seat_personal", "account": "3f1c6a2e-0000-4000-8000-000000000001", "org": "9b20d4f1-0000-4000-8000-000000000002",
			"email": "you@example.com", "org_name": nil, "plan": "Max 20x", "label": "personal", "colour": "#0E7C76", "mode": "protected",
			"order": 0, "hidden": false,
			"windows": []M{
				win("five_hour", "5-hour", 62, 62, in(100*min), "both", M{"projected_pct": 85.0, "full_at": nil, "band": "on_track"}),
				win("seven_day", "7-day", 48, 48, in(3*24*hour+9*hour), "both", M{"projected_pct": 71.0, "full_at": nil, "band": "comfortable"}),
				win("seven_day_opus", "7-day · Opus", 22, 22, in(3*24*hour+9*hour), "web", nil),
				win("iguana_necktie", "Cloud session credits", 30, 30, in(3*24*hour+9*hour), "web", nil),
			},
			"extra_usage": M{"is_enabled": false}, "spend": nil, "web_raw_at": ago(3 * min),
			"places":        []M{place("m_laptop", "laptop", "~/.claude"), place("m_gpu", "gpu-box", "~/.claude-2")},
			"live_sessions": 4,
		},
		{
			"id": "seat_work", "account": "3f1c6a2e-0000-4000-8000-000000000011", "org": "9b20d4f1-0000-4000-8000-000000000012",
			"email": "you@work.example", "org_name": "Acme", "plan": "Team", "label": "work", "colour": "#6A4FD6", "mode": nil,
			"order": 1, "hidden": false,
			"windows": []M{
				win("five_hour", "5-hour", 41, 55, in(70*min), "statusline", M{"projected_pct": 52.0, "full_at": nil, "band": "comfortable"}),
				win("seven_day", "7-day", 30, 30, in(5*24*hour), "statusline", M{"projected_pct": 44.0, "full_at": nil, "band": "comfortable"}),
			},
			"extra_usage": nil, "spend": nil, "web_raw_at": nil,
			"places":        []M{place("m_laptop", "laptop", "~/.claude-2"), place("m_gpu", "gpu-box", "~/.claude")},
			"live_sessions": 3,
		},
		{
			"id": "seat_client", "account": "3f1c6a2e-0000-4000-8000-000000000021", "org": "9b20d4f1-0000-4000-8000-000000000022",
			"email": "you@client.example", "org_name": nil, "plan": "Max 5x", "label": "client", "colour": "#9A7400", "mode": "aggressive",
			"order": 2, "hidden": false,
			"windows": []M{
				win("five_hour", "5-hour", 100, 100, in(9*min), "both", M{"projected_pct": 100.0, "full_at": ago(2 * min), "band": "runaway"}),
				win("seven_day", "7-day", 91, 91, in(2*24*hour+4*hour), "both", M{"projected_pct": 118.0, "full_at": in(30 * hour), "band": "runaway"}),
				win("seven_day_sonnet", "7-day · Sonnet", 12, 12, in(2*24*hour+4*hour), "web", nil),
			},
			"extra_usage": M{"is_enabled": true, "monthly_limit": 5000, "used_credits": 1240, "currency": "USD"},
			"spend":       M{"month_to_date_usd": 12.4},
			"web_raw_at":  ago(2 * min),
			"places":      []M{place("m_laptop", "laptop", "~/.claude-3")}, "live_sessions": 3,
		},
		{
			// No colour from the API: the board must fall back to blue (4th seat).
			"id": "seat_research", "account": "3f1c6a2e-0000-4000-8000-000000000031", "org": "9b20d4f1-0000-4000-8000-000000000032",
			"email": "you@lab.example", "org_name": nil, "plan": "Pro", "label": "research", "colour": nil, "mode": nil,
			"order": 3, "hidden": false,
			"windows": []M{
				win("five_hour", "5-hour", 8, 8, in(3*hour), "web", nil),
				win("seven_day", "7-day", 12, 12, in(4*24*hour), "web", nil),
			},
			"extra_usage": nil, "spend": nil, "web_raw_at": ago(40 * min),
			"places": []M{}, "live_sessions": 0,
			"signed_out": []M{{"machine": "m_gpu", "machine_name": "gpu-box", "slot": "~/.claude-4", "since": ago(2 * hour)}},
		},
	}

	sess := func(id, state, detail string, needs bool, since time.Duration, seat, seatLabel, colour, machine, machineName, project, title, last string, ctx float64) M {
		s := M{
			"id": id, "state": state, "detail": nilIfEmpty(detail), "needs_you": needs, "state_since": ago(since),
			"question": nil, "last_message": nilIfEmpty(last),
			"seat": nilIfEmpty(seat), "seat_label": nilIfEmpty(seatLabel), "seat_colour": nilIfEmpty(colour),
			"machine": machine, "machine_name": machineName, "slot": "~/.claude", "project": project, "cwd": "/home/me/" + project,
			"name": nil, "title": nilIfEmpty(title), "kind": "interactive", "model": "claude-opus-4-7", "context_pct": ctx,
			"started_at": ago(since + 90*min), "last_event_at": ago(since), "ended_at": nil,
			"remote_url": nil, "resume": "claude --resume " + id, "focus_url": nil, "stale": false, "machine_stale": false,
			"alert": nil,
		}
		return s
	}
	asked := sess("c41e0000-0000-4000-8000-000000000003", "asked", "question", true, 4*min, "seat_work", "work", "#6A4FD6", "m_gpu", "gpu-box", "api", "migrate-orders", "", 71)
	asked["question"] = "Should I also update the migration, or keep the old column until the backfill finishes?"
	asked["last_message"] = asked["question"]
	asked["remote_url"] = "https://claude.ai/code/session_demo"
	asked["resume"] = "claude-2 --resume c41e0000-0000-4000-8000-000000000003"
	perm := sess("c41e0000-0000-4000-8000-000000000004", "needs_permission", "permission_prompt", true, 1*min, "seat_client", "client", "#9A7400", "m_laptop", "laptop", "storefront", "deploy", "", 38)
	perm["question"] = "Bash(npm run deploy -- --prod)"
	limited := sess("c41e0000-0000-4000-8000-000000000005", "failed", "rate_limit", true, 9*min, "seat_client", "client", "#9A7400", "m_laptop", "laptop", "data-pipeline", "backfill", "5-hour limit reached. Resets soon.", 93)
	limited["resume"] = "claude-3 --resume c41e0000-0000-4000-8000-000000000005"
	work1 := sess("c41e0000-0000-4000-8000-000000000006", "working", "", false, 3*min, "seat_work", "work", "#6A4FD6", "m_gpu", "gpu-box", "api", "flaky-tests", "Running the integration suite again with the retry patch applied.", 54)
	work2 := sess("c41e0000-0000-4000-8000-000000000007", "working", "", false, 12*min, "seat_work", "work", "#6A4FD6", "m_laptop", "laptop", "infra", "terraform-plan", "Reading modules/network/main.tf", 22)
	done1 := sess("c41e0000-0000-4000-8000-000000000008", "done", "", false, 25*min, "seat_work", "work", "#6A4FD6", "m_laptop", "laptop", "docs", "changelog", "Updated CHANGELOG.md with the three fixes.", 9)
	p1 := sess("c41e0000-0000-4000-8000-000000000009", "working", "", false, 40*time.Second, "seat_personal", "personal", "#0E7C76", "m_laptop", "laptop", "watchyard", "hub-ingest", "Writing server/ingest.go", 88)
	p2 := sess("c41e0000-0000-4000-8000-00000000000a", "working", "", false, 7*min, "seat_personal", "personal", "#0E7C76", "m_gpu", "gpu-box", "blog", "draft-post", "Fetching three sources for the intro", 31)
	p3 := sess("c41e0000-0000-4000-8000-00000000000b", "done", "started", false, 2*hour, "seat_personal", "personal", "#0E7C76", "m_laptop", "laptop", "finance", "october-export", "Exported 214 rows to october.csv for the Café Lumière account.", 12)
	// A name besides the title, and an accent: the sessions search (pwa.md 3.0b) finds both.
	p3["name"] = "rapport-financier"
	gone := sess("c41e0000-0000-4000-8000-00000000000c", "gone", "", false, 5*hour, "seat_client", "client", "#9A7400", "m_laptop", "laptop", "storefront", "seo-audit", "Ended without a goodbye (terminal closed).", 47)
	gone["ended_at"] = ago(5 * hour)
	stale := sess("c41e0000-0000-4000-8000-00000000000d", "working", "", false, 14*hour, "seat_personal", "personal", "#0E7C76", "m_vps", "old-vps", "cron-jobs", "", "", 15)
	stale["stale"], stale["machine_stale"] = true, true
	unattr := sess("c41e0000-0000-4000-8000-00000000000e", "done", "", false, 3*hour, "", "", "", "m_gpu", "gpu-box", "scratch", "", "", 4)
	unattr["context_pct"] = nil
	d.Sessions = []M{asked, perm, limited, work1, p1, work2, p2, done1, p3, unattr, stale, gone}

	d.Machines = []M{
		{"id": "m_laptop", "name": "laptop", "os": "Fedora 44", "arch": "amd64", "wy_version": "0.2.0", "last_seen": ago(8 * time.Second), "last_record": ago(8 * time.Second),
			"backlog": 0, "silent": false, "live_sessions": 9,
			"slots": []M{
				{"folder": "~/.claude", "launcher": "claude", "seat": "seat_personal", "logged_in": true, "claude_version": "2.1.296"},
				{"folder": "~/.claude-2", "launcher": "claude-2", "seat": "seat_work", "logged_in": true, "claude_version": "2.1.296"},
				{"folder": "~/.claude-3", "launcher": "claude-3", "seat": "seat_client", "logged_in": true, "claude_version": "2.1.296"},
				{"folder": "~/.claude-4", "launcher": nil, "seat": nil, "logged_in": false, "claude_version": nil},
			}},
		{"id": "m_gpu", "name": "gpu-box", "os": "Ubuntu 24.04", "arch": "amd64", "wy_version": "0.2.0", "last_seen": ago(31 * time.Second), "last_record": ago(31 * time.Second),
			"backlog": 3, "silent": false, "live_sessions": 5,
			"slots": []M{
				{"folder": "~/.claude", "launcher": "claude", "seat": "seat_work", "logged_in": true, "claude_version": "2.1.290"},
				{"folder": "~/.claude-2", "launcher": "claude-2", "seat": "seat_personal", "logged_in": true, "claude_version": "2.1.290"},
			}},
		{"id": "m_vps", "name": "old-vps", "os": "Debian 12", "arch": "arm64", "wy_version": "0.1.9", "last_seen": ago(3 * hour), "last_record": ago(3 * hour),
			"backlog": 212, "silent": true, "live_sessions": 1,
			"slots": []M{{"folder": "~/.claude", "launcher": "claude", "seat": "seat_personal", "logged_in": true, "claude_version": "2.1.280"}}},
	}
	d.deviceFixtures(now)
	d.machineFixtures(now)
	return d
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Overview derives the /v1/overview body from the current data.
func (d *Data) Overview(streamID int) M {
	needs := []M{}
	counts := M{"working": 0, "needs_you": 0, "done": 0, "live": 0}
	n := func(k string) { counts[k] = counts[k].(int) + 1 }
	for _, s := range d.Sessions {
		st := s["state"].(string)
		if st == "ended" || st == "gone" {
			continue
		}
		n("live")
		switch {
		case s["needs_you"] == true:
			n("needs_you")
			needs = append(needs, s)
		case st == "working":
			n("working")
		case st == "done":
			n("done")
		}
	}
	return M{"time": ts(d.Now), "seats": d.Seats, "needs_you": sortBySince(needs), "machines": d.LiveMachines(), "counts": counts, "stream_id": streamID, "tz": "Asia/Kolkata", "tz_source": "system"}
}

func sortBySince(in []M) []M {
	out := append([]M(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j]["state_since"].(string) < out[j-1]["state_since"].(string); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Timeline makes a believable hook-event history for a session.
func (d *Data) Timeline(s M) []M {
	since, _ := time.Parse(time.RFC3339Nano, s["state_since"].(string))
	start, _ := time.Parse(time.RFC3339Nano, s["started_at"].(string))
	step := func(i int, ev, state string) M {
		t := start.Add(time.Duration(i) * (since.Sub(start)) / 6)
		return M{"ts": ts(t), "event": ev, "subtype": nil, "tool_name": nil, "state_after": nilIfEmpty(state), "last_message": nil}
	}
	tl := []M{step(0, "SessionStart", "done"), step(1, "UserPromptSubmit", "working")}
	pre := step(2, "PreToolUse", "")
	pre["tool_name"] = "Bash"
	tl = append(tl, pre, step(3, "Stop", "done"), step(4, "UserPromptSubmit", "working"))
	last := M{"ts": s["state_since"], "event": "Stop", "subtype": nil, "tool_name": nil, "state_after": s["state"], "last_message": s["last_message"]}
	switch s["state"] {
	case "needs_permission":
		last["event"] = "PermissionRequest"
	case "failed":
		last["event"] = "StopFailure"
		last["subtype"] = s["detail"]
	case "working":
		last["event"], last["last_message"] = "UserPromptSubmit", nil
	case "gone":
		last["event"], last["state_after"] = "PostToolUse", "working"
	}
	return append(tl, last)
}

func (d *Data) Session(id string) (M, bool) {
	for _, s := range d.Sessions {
		if s["id"] == id {
			return s, true
		}
	}
	return nil, false
}

func (d *Data) SessionDetail(id string) (M, bool) {
	s, ok := d.Session(id)
	if !ok {
		return nil, false
	}
	out := M{}
	for k, v := range s {
		out[k] = v
	}
	out["timeline"] = d.Timeline(s)
	out["tokens"] = M{"input": 182340, "output": 41210, "cache_read": 5120033, "cache_write": 310442, "replies": 37, "api_usd": 4.82}
	return out, true
}
