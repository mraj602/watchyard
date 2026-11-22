package devserver

import (
	"sort"
	"time"
)

// Fixed ids the app and tests can rely on.
const (
	devLaptop  = "dev_laptop"
	devGPU     = "dev_gpu"
	devAndroid = "dev_android"
	devIPad    = "dev_ipad"
	devBoard   = "dev_board" // the plain board login
	devPhone   = "dev_phone" // the phone paired through the mock claim
	devOwner   = "dev_owner" // the owner session

	targetPhone = "pt_phone"
	targetIPad  = "pt_ipad"
)

var briefKeys = []string{"id", "trigger", "status", "reason", "due_at", "fired_at", "fire_count", "snoozed_until", "seen_at"}

// Brief is the AlertBrief view of a full alert.
func Brief(a M) M {
	b := M{}
	for _, k := range briefKeys {
		b[k] = a[k]
	}
	return b
}

func defaultPrefs() M {
	return M{"mute": []string{}, "quiet_hours": nil, "private": nil, "tap": "auto", "tz": nil}
}

func defaultSettings() M {
	return M{
		"quiet_hours":   M{"enabled": false, "start": "23:00", "end": "08:00", "tz": "Asia/Kolkata", "critical": true},
		"private":       false,
		"skip_remote":   false,
		"snooze_min":    15,
		"max_age_s":     900,
		"batch_s":       2,
		"flood":         M{"max": 6, "per_s": 600},
		"vapid_subject": "https://hub.example.ts.net",
		"webpush_hosts": []string{"fcm.googleapis.com", "updates.push.services.mozilla.com", "web.push.apple.com", "wns2-par02p.notify.windows.com"},
		"refresh_min":   15,
	}
}

func newDevice(now time.Time, id, kind, name, hint string, scopes []string, created time.Duration, createdBy string) M {
	return M{
		"id": id, "kind": kind, "name": name, "hint": hint, "scopes": scopes, "owner_session": false, "machine": nil,
		"created_at": ts(now.Add(-created)), "created_by": createdBy,
		"last_used_at": ts(now.Add(-time.Minute)), "last_used_ip": "192.0.2.7",
		"expires_at": nil, "revoked_at": nil, "prefs": defaultPrefs(),
	}
}

func (d *Data) deviceFixtures(now time.Time) {
	ago := func(x time.Duration) string { return ts(now.Add(-x)) }
	in := func(x time.Duration) string { return ts(now.Add(x)) }
	min, hour := time.Minute, time.Hour

	d.Settings = defaultSettings()
	d.Channels = []M{{"kind": "webpush", "enabled": true}, {"kind": "ntfy", "enabled": true}}

	// ---- rules
	rule := func(trigger string, enabled bool, delay int, bypass bool, scope M) M {
		return M{"id": "rule_" + trigger, "trigger": trigger, "base": true, "enabled": enabled, "delay_s": delay,
			"bypass_quiet": bypass, "scope": scope, "updated_at": ago(48 * hour)}
	}
	wins := M{"windows": []string{"five_hour", "seven_day"}}
	d.Rules = []M{
		rule("needs_permission", true, 30, false, M{}),
		rule("asked", true, 60, false, M{}),
		rule("failed", true, 0, true, M{}),
		rule("limit_80", true, 0, false, wins),
		rule("limit_95", true, 0, false, wins),
		rule("reset", false, 0, false, wins),
		rule("machine_silent", true, 3600, false, M{}),
		{"id": "rule_o1", "trigger": "asked", "base": false, "enabled": true, "delay_s": 300, "bypass_quiet": false,
			"scope": M{"seats": []string{"seat_work"}}, "updated_at": ago(2 * hour)},
	}

	// ---- devices and targets
	lap := newDevice(now, devLaptop, "machine", "laptop", "wy_m_…aB3d", []string{"write"}, 20*24*hour, "pair:pc_01JA0001")
	lap["machine"] = "m_laptop"
	gpu := newDevice(now, devGPU, "machine", "gpu-box", "wy_m_…q9Zx", []string{"write"}, 12*24*hour, "pair:pc_01JA0002")
	gpu["machine"] = "m_gpu"
	android := newDevice(now, devAndroid, "phone", "Phone X · Chrome", "wy_p_…k3Qz", []string{"read", "push"}, 6*24*hour, "pair:pc_01JA0003")
	ipad := newDevice(now, devIPad, "phone", "iPad · Safari", "wy_p_…m7Lw", []string{"read", "push"}, 3*24*hour, "pair:pc_01JA0004")
	ipad["last_used_at"] = ago(30 * hour)
	d.Devices = []M{lap, gpu, android, ipad}

	d.Targets = []M{
		{"id": targetPhone, "kind": "webpush", "device": devAndroid, "name": "Phone X · Chrome", "endpoint_host": "fcm.googleapis.com",
			"endpoint": "https://fcm.googleapis.com/fcm/send/dev-phone-endpoint", "server": nil, "topic": nil, "token_set": false,
			"enabled": true, "status": "ok", "last_ok_at": ago(2 * min), "last_error": nil, "last_error_at": nil, "created_at": ago(6 * 24 * hour)},
		{"id": targetIPad, "kind": "webpush", "device": devIPad, "name": "iPad · Safari", "endpoint_host": "web.push.apple.com",
			"endpoint": "https://web.push.apple.com/dev-ipad-endpoint", "server": nil, "topic": nil, "token_set": false,
			"enabled": true, "status": "gone", "last_ok_at": ago(40 * hour), "last_error": "410 Gone", "last_error_at": ago(30 * hour), "created_at": ago(3 * 24 * hour)},
	}

	// ---- alerts, one in every status
	delivery := func(target, name string, fire int, status string, at string) M {
		return M{"target": target, "kind": "webpush", "target_name": name, "fire": fire, "batch": nil, "status": status,
			"reason": nil, "tries": 1, "http_status": 201, "updated_at": at}
	}
	alert := func(id, trigger, status string, created string) M {
		return M{"id": id, "trigger": trigger, "status": status, "reason": nil, "due_at": nil, "fired_at": nil, "fire_count": 0,
			"snoozed_until": nil, "seen_at": nil, "rule": "rule_" + trigger, "session": nil, "seat": nil, "machine": nil,
			"window_key": nil, "title": "", "body": nil, "url": "/#/", "created_at": created, "since_at": nil, "ended_at": nil,
			"deliveries": []M{}}
	}
	const (
		sAsked  = "c41e0000-0000-4000-8000-000000000003"
		sPerm   = "c41e0000-0000-4000-8000-000000000004"
		sFailed = "c41e0000-0000-4000-8000-000000000005"
		sWork   = "c41e0000-0000-4000-8000-000000000006"
		sDocs   = "c41e0000-0000-4000-8000-000000000008"
	)

	pending := alert("al_pending", "asked", "pending", ago(20*time.Second))
	pending["due_at"] = in(40 * time.Second)
	pending["session"], pending["seat"], pending["machine"] = sAsked, "seat_work", "m_gpu"
	pending["title"] = "work · api · asked a question"
	pending["body"] = "Should I also update the migration, or keep the old column until the backfill finishes?"
	pending["url"] = "/#/sessions/" + sAsked + "?alert=al_pending"
	pending["since_at"] = ago(4 * min)

	fired := alert("al_fired", "needs_permission", "fired", ago(90*time.Second))
	fired["fired_at"], fired["fire_count"] = ago(30*time.Second), 1
	fired["session"], fired["seat"], fired["machine"] = sPerm, "seat_client", "m_laptop"
	fired["title"] = "client · storefront · needs permission"
	fired["body"] = "Bash(npm run deploy -- --prod)"
	fired["url"] = "/#/sessions/" + sPerm + "?alert=al_fired"
	fired["since_at"] = ago(1 * min)
	fired["deliveries"] = []M{delivery(targetPhone, "Phone X · Chrome", 1, "sent", ago(30*time.Second)), delivery(targetIPad, "iPad · Safari", 1, "gone", ago(30*time.Second))}
	fired["deliveries"].([]M)[1]["http_status"] = 410

	snoozed := alert("al_snoozed", "limit_80", "snoozed", ago(55*min))
	snoozed["fired_at"], snoozed["fire_count"] = ago(40*min), 1
	snoozed["snoozed_until"], snoozed["due_at"] = in(11*min), in(11*min)
	snoozed["seat"], snoozed["window_key"] = "seat_client", "five_hour"
	snoozed["title"] = "client · 5-hour window at 80%"
	snoozed["body"] = "work has 59% left"
	snoozed["url"] = "/#/accounts?seat=seat_client"
	snoozed["deliveries"] = []M{delivery(targetPhone, "Phone X · Chrome", 1, "sent", ago(40*min))}

	seen := alert("al_seen", "failed", "seen", ago(9*min))
	seen["fired_at"], seen["fire_count"], seen["seen_at"] = ago(8*min), 1, ago(6*min)
	seen["session"], seen["seat"], seen["machine"] = sFailed, "seat_client", "m_laptop"
	seen["title"] = "client · data-pipeline · rate limited"
	seen["body"] = "5-hour limit reached. Resets soon. work has 59% left"
	seen["url"] = "/#/sessions/" + sFailed + "?alert=al_seen"
	seen["since_at"] = ago(9 * min)
	seen["deliveries"] = []M{delivery(targetPhone, "Phone X · Chrome", 1, "sent", ago(8*min))}

	cancelled := alert("al_cancelled", "needs_permission", "cancelled", ago(35*min))
	cancelled["reason"], cancelled["ended_at"] = "answered", ago(34*min)
	cancelled["session"], cancelled["seat"], cancelled["machine"] = sWork, "seat_work", "m_gpu"
	cancelled["title"] = "work · api · needs permission"
	cancelled["url"] = "/#/sessions/" + sWork + "?alert=al_cancelled"
	cancelled["since_at"] = ago(35 * min)

	closed := alert("al_closed", "machine_silent", "closed", ago(5*hour))
	closed["reason"], closed["fired_at"], closed["fire_count"], closed["ended_at"] = "recovered", ago(4*hour), 1, ago(3*hour+30*min)
	closed["machine"] = "m_vps"
	closed["title"] = "old-vps silent 1 h"
	closed["body"] = "1 session was working when it went quiet."
	closed["url"] = "/#/machines?machine=m_vps"
	closed["deliveries"] = []M{delivery(targetPhone, "Phone X · Chrome", 1, "sent", ago(4*hour))}

	skipped := alert("al_skipped", "asked", "skipped", ago(3*hour))
	skipped["reason"] = "remote_control"
	skipped["session"], skipped["seat"], skipped["machine"] = sDocs, "seat_work", "m_laptop"
	skipped["title"] = "work · docs · asked a question"
	skipped["url"] = "/#/sessions/" + sDocs + "?alert=al_skipped"

	d.Alerts = []M{pending, fired, snoozed, seen, cancelled, closed, skipped}
	d.SyncAlerts()
}

var openStatus = map[string]bool{"pending": true, "fired": true, "snoozed": true, "seen": true}

// SyncAlerts sets each session's `alert` brief to its newest open alert, or null. A skipped
// alert counts while its session needs you (the mock has no episodes: that is its current
// one, push.md 10.1), so Seen can quiet the row.
func (d *Data) SyncAlerts() {
	for _, s := range d.Sessions {
		s["alert"] = nil
	}
	sorted := append([]M(nil), d.Alerts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i]["created_at"].(string) < sorted[j]["created_at"].(string) })
	for _, a := range sorted {
		id, _ := a["session"].(string)
		if id == "" {
			continue
		}
		s, ok := d.Session(id)
		if !ok || !(openStatus[a["status"].(string)] || a["status"] == "skipped" && s["needs_you"] == true) {
			continue
		}
		s["alert"] = Brief(a)
	}
}

// Alert returns the alert with the given id.
func (d *Data) Alert(id string) (M, bool) {
	for _, a := range d.Alerts {
		if a["id"] == id {
			return a, true
		}
	}
	return nil, false
}
