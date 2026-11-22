package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/store"
)

const trustM = "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4A"

// trustStore is a hub store with one machine (and its agent device).
func trustStore(t *testing.T) (*store.Store, context.Context, string) {
	t.Helper()
	st, ctx := openTargets(t)
	c, err := auth.Create(ctx, st, auth.CreateRequest{Kind: "machine", Name: "laptop", Machine: trustM})
	if err != nil {
		t.Fatal(err)
	}
	return st, ctx, c.Device.ID
}

func trustRow(device, account string, approved int64) store.PeerTrustRow {
	return store.PeerTrustRow{Machine: trustM, Sender: "doorbell", Device: device, Account: account, Program: "/v/bin/doorbell",
		Interpreter: "/py/bin/python3", Venv: "/v", ApprovedAt: approved}
}

func putTrust(t *testing.T, st *store.Store, ctx context.Context, r store.PeerTrustRow, now int64) bool {
	t.Helper()
	var changed bool
	if err := st.Write(ctx, func(tx *store.Tx) error {
		var err error
		changed, err = tx.PutPeerTrust(r, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return changed
}

func revokeTrust(t *testing.T, st *store.Store, ctx context.Context, by string, now int64) (bool, error) {
	t.Helper()
	var changed bool
	err := st.Write(ctx, func(tx *store.Tx) error {
		var err error
		changed, err = tx.RevokePeerTrust(trustM, "doorbell", by, now)
		return err
	})
	return changed, err
}

func trustOf(t *testing.T, st *store.Store, ctx context.Context) store.PeerTrustRow {
	t.Helper()
	rows, err := st.MachinePeerTrust(ctx, trustM)
	if err != nil || len(rows) != 1 {
		t.Fatalf("trust rows: %+v %v", rows, err)
	}
	return rows[0]
}

// A machine's report: only a strictly newer approval replaces a revoked row and clears the
// revocation; the same approval after a revoke and an older one change nothing.
func TestPutPeerTrust(t *testing.T) {
	st, ctx, dev := trustStore(t)
	if rows, err := st.MachinePeerTrust(ctx, trustM); err != nil || len(rows) != 0 {
		t.Fatalf("before: %+v %v", rows, err)
	}

	// First report: inserted, with the machine's name.
	if !putTrust(t, st, ctx, trustRow(dev, "acct-a", 1000), 5000) {
		t.Fatal("first report not stored")
	}
	r := trustOf(t, st, ctx)
	if !r.Active() || r.MachineName != "laptop" || r.Account != "acct-a" || r.ApprovedAt != 1000 || r.UpdatedAt != 5000 ||
		r.Venv != "/v" || r.Program != "/v/bin/doorbell" || r.Interpreter != "/py/bin/python3" || r.Device != dev {
		t.Fatalf("row: %+v", r)
	}
	var one store.PeerTrustRow
	var ok bool
	if err := st.Write(ctx, func(tx *store.Tx) (err error) { one, ok, err = tx.PeerTrust(trustM, "doorbell"); return }); err != nil || !ok || one.Account != "acct-a" {
		t.Fatalf("PeerTrust: %+v %v %v", one, ok, err)
	}
	if err := st.Write(ctx, func(tx *store.Tx) (err error) { _, ok, err = tx.PeerTrust(trustM, "nobody"); return }); err != nil || ok {
		t.Fatalf("PeerTrust of another sender: %v %v", ok, err)
	}

	// The same report again: nothing changes (updated_at stays).
	if putTrust(t, st, ctx, trustRow(dev, "acct-a", 1000), 6000) {
		t.Error("an identical report counted as a change")
	}
	if trustOf(t, st, ctx).UpdatedAt != 5000 {
		t.Error("an identical report rewrote the row")
	}
	// An older approval is ignored.
	if putTrust(t, st, ctx, trustRow(dev, "acct-z", 900), 6000) || trustOf(t, st, ctx).Account != "acct-a" {
		t.Error("an older approval replaced the row")
	}
	// A newer approval while active replaces it (another account, say).
	if !putTrust(t, st, ctx, trustRow(dev, "acct-b", 2000), 7000) {
		t.Fatal("a newer approval was ignored")
	}
	if r := trustOf(t, st, ctx); r.Account != "acct-b" || r.ApprovedAt != 2000 || r.UpdatedAt != 7000 {
		t.Fatalf("row: %+v", r)
	}

	// Revoked: the same approval is ignored, an older one too; the revocation stands.
	if changed, err := revokeTrust(t, st, ctx, "dev_owner", 8000); err != nil || !changed {
		t.Fatalf("revoke: %v %v", changed, err)
	}
	if putTrust(t, st, ctx, trustRow(dev, "acct-b", 2000), 9000) {
		t.Error("the same approval after a revoke was taken")
	}
	if putTrust(t, st, ctx, trustRow(dev, "acct-b", 1500), 9000) {
		t.Error("an older approval after a revoke was taken")
	}
	if r := trustOf(t, st, ctx); r.Active() || r.RevokedAt != 8000 || r.RevokedBy != "dev_owner" {
		t.Fatalf("revocation undone: %+v", r)
	}
	// A strictly newer approval replaces the row and clears the revocation.
	if !putTrust(t, st, ctx, trustRow(dev, "acct-c", 3000), 10000) {
		t.Fatal("a newer approval after a revoke was ignored")
	}
	if r := trustOf(t, st, ctx); !r.Active() || r.RevokedAt != 0 || r.RevokedBy != "" || r.Account != "acct-c" || r.ApprovedAt != 3000 {
		t.Fatalf("row: %+v", r)
	}
}

// RevokePeerTrust: the first call marks the row, the second changes nothing, an unknown
// machine is ErrNotFound; PeerTrusts lists every row by machine name.
func TestRevokePeerTrust(t *testing.T) {
	st, ctx, dev := trustStore(t)
	if _, err := revokeTrust(t, st, ctx, "cli", 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no row: %v, want ErrNotFound", err)
	}
	putTrust(t, st, ctx, trustRow(dev, "acct-a", 1000), 2000)

	if changed, err := revokeTrust(t, st, ctx, "dev_owner", 3000); err != nil || !changed {
		t.Fatalf("revoke: %v %v", changed, err)
	}
	r := trustOf(t, st, ctx)
	if r.Active() || r.RevokedAt != 3000 || r.RevokedBy != "dev_owner" || r.UpdatedAt != 3000 {
		t.Fatalf("row: %+v", r)
	}
	// Idempotent: the first revocation's time and author stay.
	if changed, err := revokeTrust(t, st, ctx, "cli", 4000); err != nil || changed {
		t.Fatalf("revoke again: %v %v", changed, err)
	}
	if r := trustOf(t, st, ctx); r.RevokedAt != 3000 || r.RevokedBy != "dev_owner" || r.UpdatedAt != 3000 {
		t.Fatalf("row after a second revoke: %+v", r)
	}
	if changed, err := revokeTrust(t, st, ctx, "cli", 4000); err != nil || changed {
		t.Fatalf("third revoke: %v %v", changed, err)
	}
	// Another machine and another sender are ErrNotFound.
	err := st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.RevokePeerTrust("m_other", "doorbell", "cli", 5000)
		return err
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("other machine: %v", err)
	}

	all, err := st.PeerTrusts(ctx)
	if err != nil || len(all) != 1 || all[0].Machine != trustM || all[0].MachineName != "laptop" {
		t.Fatalf("PeerTrusts: %+v %v", all, err)
	}
	if rows, err := st.MachinePeerTrust(ctx, "m_other"); err != nil || len(rows) != 0 {
		t.Fatalf("other machine's rows: %+v %v", rows, err)
	}
}

// RevokePeerTrustOwner marks the row revoked by the caller, writes one trust_revoke audit row
// (none for a repeat) and returns ErrNotFound without a row.
func TestRevokePeerTrustOwner(t *testing.T) {
	st, ctx, dev := trustStore(t)
	if _, err := st.RevokePeerTrustOwner(ctx, trustM, "doorbell", "dev_owner"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no row: %v", err)
	}
	putTrust(t, st, ctx, trustRow(dev, "acct-a", 1000), 2000)
	r, err := st.RevokePeerTrustOwner(ctx, trustM, "doorbell", "dev_owner")
	if err != nil || r.Active() || r.RevokedBy != "dev_owner" {
		t.Fatalf("revoke: %+v %v", r, err)
	}
	r2, err := st.RevokePeerTrustOwner(ctx, trustM, "doorbell", "dev_other")
	if err != nil || r2.RevokedAt != r.RevokedAt || r2.RevokedBy != "dev_owner" {
		t.Fatalf("revoke again: %+v %v", r2, err)
	}
	audit, err := st.PeerAudit(ctx, 0, 10)
	if err != nil || len(audit) != 1 || audit[0].Event != "trust_revoke" || audit[0].Actor != "dev_owner" {
		t.Fatalf("audit: %+v %v", audit, err)
	}
}

// PeerTrustUse counts a machine's trusted sends: the newest, those since a time, and the tags
// used since another time (newest first); a session's messages and another machine's do not
// count.
func TestPeerTrustUse(t *testing.T) {
	st, ctx, dev := trustStore(t)
	other, err := auth.Create(ctx, st, auth.CreateRequest{Kind: "machine", Name: "gpu", Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4B"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	const hour = int64(time.Hour / time.Millisecond)
	n := 0
	msg := func(device, kind, tag string, at int64) {
		t.Helper()
		n++
		id := "msg-" + string(rune('a'+n))
		if err := st.Write(ctx, func(tx *store.Tx) error {
			return tx.InsertPeerMessage(store.PeerMessageRow{ID: id, Thread: "t_" + id, FromSession: "s", FromDevice: device, FromAcct: "acct-a",
				ToSession: "sess-b", ToDevice: other.Device.ID, ToAcct: "acct-b", Kind: "start", Size: 1, Status: "delivered",
				CreatedAt: at, ExpiresAt: now + hour, FromKind: kind, ViaTag: tag})
		}); err != nil {
			t.Fatal(err)
		}
	}
	const svc = "service:doorbell"
	msg(dev, svc, "ci", now-10*hour)     // old: before both windows
	msg(dev, svc, "deploy", now-3*hour)  // in the tag window, not "today"
	msg(dev, svc, "ci", now-2*hour)      // newest ci
	msg(dev, svc, "alerts", now-hour)    // today, newest
	msg(dev, "", "", now-30*60*1000)     // a session's message: not counted
	msg(other.Device.ID, svc, "ci", now) // another machine's

	u, err := st.PeerTrustUse(ctx, trustM, svc, now-90*60*1000, now-5*hour)
	if err != nil {
		t.Fatal(err)
	}
	if u.LastAt != now-hour {
		t.Errorf("LastAt %d, want %d", u.LastAt, now-hour)
	}
	if u.Since != 1 {
		t.Errorf("Since %d, want 1", u.Since)
	}
	want := []store.TagUse{{Tag: "alerts", At: now - hour}, {Tag: "ci", At: now - 2*hour}, {Tag: "deploy", At: now - 3*hour}}
	if len(u.Tags) != len(want) {
		t.Fatalf("Tags %+v, want %+v", u.Tags, want)
	}
	for i := range want {
		if u.Tags[i] != want[i] {
			t.Errorf("Tags[%d] %+v, want %+v", i, u.Tags[i], want[i])
		}
	}
	// No sends: zero values.
	if u, err := st.PeerTrustUse(ctx, "m_unused", svc, 0, 0); err != nil || u.LastAt != 0 || u.Since != 0 || len(u.Tags) != 0 {
		t.Errorf("unused machine: %+v %v", u, err)
	}
}

// AccountLabels maps an account to its first labelled seat's label; accounts with no
// labelled seat are absent.
func TestAccountLabels(t *testing.T) {
	st, ctx := openTargets(t)
	if m, err := st.AccountLabels(ctx); err != nil || len(m) != 0 {
		t.Fatalf("empty: %v %v", m, err)
	}
	at := int64(0)
	seat := func(account, org, email, label string) {
		t.Helper()
		at += 1000 // seat ids sort by their creation time
		if err := st.Write(ctx, func(tx *store.Tx) error {
			id, err := tx.UpsertSeat(store.SeatKey{Account: account, Org: org, Email: email}, at)
			if err != nil {
				return err
			}
			if label == "" {
				return nil
			}
			return tx.SeatLook(id, label, "", nil)
		}); err != nil {
			t.Fatal(err)
		}
	}
	seat("acct-a", "org-1", "a1@example.test", "work")
	seat("acct-a", "org-2", "a2@example.test", "second") // the same account's later seat (another org)
	seat("acct-b", "org-1", "b@example.test", "")        // no label
	seat("acct-c", "org-1", "c@example.test", "home")
	m, err := st.AccountLabels(ctx)
	if err != nil || len(m) != 2 || m["acct-a"] != "work" || m["acct-c"] != "home" {
		t.Fatalf("labels: %v %v", m, err)
	}
	if _, has := m["acct-b"]; has {
		t.Error("an unlabelled account is listed")
	}
}
