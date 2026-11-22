package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BackupName is the default backup file name for time t: hub-<UTC time>.db (paths.md 3).
func BackupName(t time.Time) string {
	return "hub-" + t.UTC().Format("20060102T150405Z") + ".db"
}

// Backup writes a consistent copy of the live database to `to` with VACUUM INTO, which
// runs as a read transaction: ingest keeps working while it runs. The copy is a complete,
// compacted database (no -wal needed). `to` must not exist.
func (s *Store) Backup(ctx context.Context, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	// VACUUM INTO needs a connection that may write the target file (the read pool is
	// query_only), and must not take the writer connection, so ingest is not blocked.
	db, err := sql.Open("sqlite", dsn(s.path, 30*time.Second, false))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := vacuumInto(ctx, db, to); err != nil {
		return fmt.Errorf("store: backup to %s: %w", to, err)
	}
	return os.Chmod(to, 0o600)
}

// PruneResult counts what Prune removed (or would remove).
type PruneResult struct {
	Records, HookEvents, LimitSamples, WebUsage, TokenRows, LimitHits, AccountLeases int64
}

// Prune removes raw and fine-grained rows older than before (record.received_at…, sample
// ts), as `wy hub prune`. It never removes seats, accounts, orgs,
// machines, slots, slot logins, sessions, session_state, session_transition, usage_window,
// history_day or devices: everything the board needs to show a summary stays.
//
// Removed: record rows with ts < before (hook_event rows go with them by ON DELETE
// CASCADE), limit_sample, web_usage, token_row, limit_hit and account_lease rows with ts < before.
// With dryRun the counts are computed and nothing is deleted.
func (s *Store) Prune(ctx context.Context, before time.Time, dryRun bool) (PruneResult, error) {
	var res PruneResult
	ms := before.UnixMilli()
	err := s.Write(ctx, func(tx *Tx) error {
		count := func(dst *int64, q string) error {
			return tx.queryRow(q, ms).Scan(dst)
		}
		steps := []struct {
			dst          *int64
			count, purge string
		}{
			{&res.HookEvents, `SELECT count(*) FROM hook_event WHERE record_id IN (SELECT id FROM record WHERE ts < ?)`, ``},
			{&res.Records, `SELECT count(*) FROM record WHERE ts < ?`, `DELETE FROM record WHERE ts < ?`},
			{&res.LimitSamples, `SELECT count(*) FROM limit_sample WHERE ts < ?`, `DELETE FROM limit_sample WHERE ts < ?`},
			{&res.WebUsage, `SELECT count(*) FROM web_usage WHERE ts < ?`, `DELETE FROM web_usage WHERE ts < ?`},
			{&res.TokenRows, `SELECT count(*) FROM token_row WHERE ts < ?`, `DELETE FROM token_row WHERE ts < ?`},
			{&res.LimitHits, `SELECT count(*) FROM limit_hit WHERE ts < ?`, `DELETE FROM limit_hit WHERE ts < ?`},
			{&res.AccountLeases, `SELECT count(*) FROM account_lease WHERE ts < ?`, `DELETE FROM account_lease WHERE ts < ?`},
		}
		for _, st := range steps {
			if err := count(st.dst, st.count); err != nil {
				return err
			}
		}
		if dryRun {
			return nil
		}
		for _, st := range steps {
			if st.purge == "" {
				continue
			}
			if _, err := tx.exec(st.purge, ms); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("store: prune: %w", err)
	}
	if !dryRun {
		// Give the space back to the file system; outside the transaction.
		s.mu.Lock()
		_, _ = s.w.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
		s.mu.Unlock()
	}
	return res, nil
}
