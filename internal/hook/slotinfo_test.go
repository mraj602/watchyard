package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/record"
)

func writeConfig(t *testing.T, sb *sandbox, body string) {
	t.Helper()
	cfg := filepath.Join(sb.home, "wy", "config")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func slotInfos(t *testing.T, sb *sandbox) []record.SlotInfoPayload {
	t.Helper()
	var out []record.SlotInfoPayload
	for _, r := range sb.ofKind(record.KindSlotInfo) {
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
		var p record.SlotInfoPayload
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// record.md 4.7: slot_info on SessionStart when the login differs from the last one sent,
// so a label set at setup follows the folder's login to the hub, and nothing more while it
// stays the same.
func TestSessionStartSendsSlotInfoOnLoginChange(t *testing.T) {
	sb := newSandbox(t)
	writeConfig(t, sb, "[slots.labels]\n\"~/.claude-3\" = \"work\"\n[slots.colours]\n\"~/.claude-3\" = \"#7c3aed\"\n")
	start := []byte(`{"session_id":"s1"}`)
	sb.run("UserPromptSubmit", []byte(`{"session_id":"s1","prompt":"x"}`))
	if n := len(slotInfos(t, sb)); n != 0 {
		t.Fatalf("UserPromptSubmit queued %d slot_info", n)
	}
	sb.run("SessionStart", start)
	got := slotInfos(t, sb)
	if len(got) != 1 {
		t.Fatalf("first SessionStart: %d slot_info, want 1", len(got))
	}
	p := got[0]
	if p.Folder != "~/.claude-3" || !p.LoggedIn || string(p.Extra["label"]) != `"work"` || string(p.Extra["colour"]) != `"#7c3aed"` {
		t.Fatalf("slot_info: %+v extra %s", p, p.Extra)
	}
	var oa map[string]any
	if err := json.Unmarshal(p.OAuthAccount, &oa); err != nil || oa["organizationName"] != "Org 3" || oa["accountUuid"] != "aaaaaaaa-0000-4000-8000-000000000003" {
		t.Fatalf("oauth_account kept whole: %s (%v)", p.OAuthAccount, err)
	}
	r := sb.ofKind(record.KindSlotInfo)[0]
	if r.Seat == nil || r.Seat.Account != "aaaaaaaa-0000-4000-8000-000000000003" || r.Slot != "~/.claude-3" {
		t.Fatalf("envelope: %+v", r)
	}

	sb.run("SessionStart", start)
	if n := len(slotInfos(t, sb)); n != 1 {
		t.Fatalf("same login: %d slot_info, want still 1", n)
	}

	// Logged in again as someone else: sent again, with the label.
	slot := filepath.Join(sb.home, ".claude-3", ".claude.json")
	other := []byte(`{"oauthAccount":{"accountUuid":"aaaaaaaa-0000-4000-8000-000000000009","emailAddress":"new@example.com","organizationUuid":"bbbbbbbb-0000-4000-8000-000000000009"}}`)
	if err := os.WriteFile(slot, other, 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(slot, later, later)
	sb.run("SessionStart", start)
	got = slotInfos(t, sb)
	if len(got) != 2 || !got[1].LoggedIn || string(got[1].Extra["label"]) != `"work"` {
		t.Fatalf("after re-login: %+v", got)
	}
	if rs := sb.ofKind(record.KindSlotInfo); rs[1].Seat == nil || rs[1].Seat.Account != "aaaaaaaa-0000-4000-8000-000000000009" {
		t.Fatalf("after re-login seat: %+v", rs[1].Seat)
	}

	// A new label is a change too.
	writeConfig(t, sb, "[slots.labels]\n\"~/.claude-3\" = \"client\"\n")
	sb.run("SessionStart", start)
	if got = slotInfos(t, sb); len(got) != 3 || string(got[2].Extra["label"]) != `"client"` {
		t.Fatalf("after relabel: %+v", got)
	}
}

// Once a day the same login is sent again (record.md 4.7).
func TestSessionStartResendsSlotInfoDaily(t *testing.T) {
	sb := newSandbox(t)
	sb.run("SessionStart", []byte(`{"session_id":"s1"}`))
	path := filepath.Join(sb.state(), "identity", sentName)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]sentSlot
	if err := json.Unmarshal(b, &sent); err != nil {
		t.Fatal(err)
	}
	s := sent["~/.claude-3"]
	if s.Account != "aaaaaaaa-0000-4000-8000-000000000003" || !s.LoggedIn {
		t.Fatalf("sent.json: %s", b)
	}
	s.At = time.Now().Add(-25 * time.Hour)
	sent["~/.claude-3"] = s
	b, _ = json.Marshal(sent)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	sb.run("SessionStart", []byte(`{"session_id":"s2"}`))
	if n := len(slotInfos(t, sb)); n != 2 {
		t.Fatalf("a day later: %d slot_info, want 2", n)
	}
}

// A folder that is logged out reports logged_in false, which closes its login period on the
// hub (identity.md 5.2).
func TestSessionStartReportsLoggedOut(t *testing.T) {
	sb := newSandbox(t)
	if err := os.WriteFile(filepath.Join(sb.home, ".claude-3", ".claude.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sb.run("SessionStart", []byte(`{"session_id":"s1"}`))
	got := slotInfos(t, sb)
	if len(got) != 1 || got[0].LoggedIn || len(got[0].OAuthAccount) != 0 {
		t.Fatalf("logged out: %+v", got)
	}
}

// A `/login` inside a session that goes on: the next hook event of any kind sends the new
// login (record.md 4.7), so the folder's label reaches its new seat without waiting for the
// next SessionStart. Label changes and the daily resend still wait for SessionStart.
func TestAnyEventSendsSlotInfoOnLoginChange(t *testing.T) {
	sb := newSandbox(t)
	writeConfig(t, sb, "[slots.labels]\n\"~/.claude-3\" = \"work\"\n")
	sb.run("SessionStart", []byte(`{"session_id":"s1"}`))
	if n := len(slotInfos(t, sb)); n != 1 {
		t.Fatalf("SessionStart: %d slot_info, want 1", n)
	}

	slot := filepath.Join(sb.home, ".claude-3", ".claude.json")
	other := []byte(`{"oauthAccount":{"accountUuid":"aaaaaaaa-0000-4000-8000-000000000003","emailAddress":"new@example.com","organizationUuid":"bbbbbbbb-0000-4000-8000-000000000009"}}`)
	if err := os.WriteFile(slot, other, 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(slot, later, later)
	sb.run("Notification", []byte(`{"session_id":"s1","message":"logged in"}`))
	got := slotInfos(t, sb)
	if len(got) != 2 || string(got[1].Extra["label"]) != `"work"` {
		t.Fatalf("after /login: %+v", got)
	}
	if rs := sb.ofKind(record.KindSlotInfo); rs[1].Seat == nil || rs[1].Seat.Org != "bbbbbbbb-0000-4000-8000-000000000009" {
		t.Fatalf("after /login seat: %+v", rs[1].Seat)
	}
	sb.run("UserPromptSubmit", []byte(`{"session_id":"s1","prompt":"x"}`))
	if n := len(slotInfos(t, sb)); n != 2 {
		t.Fatalf("same login again: %d slot_info, want still 2", n)
	}

	writeConfig(t, sb, "[slots.labels]\n\"~/.claude-3\" = \"client\"\n")
	sb.run("UserPromptSubmit", []byte(`{"session_id":"s1","prompt":"y"}`))
	if n := len(slotInfos(t, sb)); n != 2 {
		t.Fatalf("relabel outside SessionStart: %d slot_info, want still 2", n)
	}
}
