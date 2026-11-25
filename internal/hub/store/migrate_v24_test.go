package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"testing"
)

// 0024_switch_move.sql is applied on live hubs as these exact bytes (it came from the board's
// Switches log branch): it must never change, or the two branches' hubs would differ.
const switchMove0024SHA = "c23c42a4fc422255dbc06749238de73baf1a7d821ba31cd4d6e4a1bcefdd363f"

// A hub at v24 (switch_move applied by the board-moves-log build) opens with this build and
// applies only 0025: its v24 row and its switch_move rows stay, and the seat gains pick_off
// and drain_until and the drain rules.
func TestMigrateFromV24SwitchMove(t *testing.T) {
	ms, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 25 || ms[23].Name != "0024_switch_move.sql" || ms[24].Name != "0025_seat_off_drain.sql" {
		t.Fatalf("migrations 24, 25: %s, %s", ms[23].Name, ms[24].Name)
	}
	if sum := sha256.Sum256([]byte(ms[23].SQL)); hex.EncodeToString(sum[:]) != switchMove0024SHA {
		t.Fatalf("0024_switch_move.sql changed (sha256 %x): live hubs applied the old bytes", sum)
	}

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, m := range ms[:24] {
		if _, err := db.Exec(m.SQL); err != nil {
			t.Fatalf("%s: %v", m.Name, err)
		}
		if _, err := db.Exec(`INSERT OR REPLACE INTO schema_version (version, applied_at, wy_version) VALUES (?, 1000, 'v24-hub')`, m.Version); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO machine (id, name, first_seen) VALUES ('m_1', 'laptop', 1)`,
		`INSERT INTO switch_move (record_id, machine_id, at, session_id, kind, interactive, from_label, to_label, auto, cross_org)
		 VALUES ('r_1', 'm_1', 5, 's_1', 'lane', 0, 'claude-2', 'claude-4', 1, 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.SchemaVersion(ctx); v != 25 {
		t.Fatalf("version %d", v)
	}
	var applied, n int
	var by string
	if err := s.R().QueryRow(`SELECT applied_at, wy_version FROM schema_version WHERE version = 24`).Scan(&applied, &by); err != nil || applied != 1000 || by != "v24-hub" {
		t.Fatalf("v24 row re-applied: %v %d %s", err, applied, by)
	}
	if err := s.R().QueryRow(`SELECT count(*) FROM schema_version WHERE version = 25 AND wy_version != 'v24-hub'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("v25 row: %v %d", err, n)
	}
	if err := s.R().QueryRow(`SELECT count(*) FROM switch_move`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("switch_move rows: %v %d", err, n)
	}
	if err := s.R().QueryRow(`SELECT count(*) FROM seat WHERE pick_off = 0 AND drain_until IS NULL`).Scan(&n); err != nil {
		t.Fatalf("seat columns: %v", err)
	}
	if err := s.R().QueryRow(`SELECT count(*) FROM rule WHERE id IN ('rule_drain_started', 'rule_drain_ended', 'rule_drain_pulled')`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("drain rules: %v %d", err, n)
	}
}
