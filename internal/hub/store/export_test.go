package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func openTest(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(dir, "hub.db"), Options{NoBackupBeforeMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedDevice(t *testing.T, s *Store) {
	t.Helper()
	err := s.Write(context.Background(), func(tx *Tx) error {
		if _, err := tx.exec(`INSERT INTO machine (id, name, first_seen) VALUES ('m_1', 'laptop', 1)`); err != nil {
			return err
		}
		_, err := tx.exec(`INSERT INTO device (id, kind, name, machine_id, token_hash, token_hint, scopes, created_at, created_by)
			VALUES ('dev_1', 'machine', 'laptop', 'm_1', ?, 'wy_m_…abcd', '["write"]', 1, 'cli')`, bytes.Repeat([]byte{7}, 32))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// ingestOne is what ingest does in its write transaction: re-check the freeze, then store.
func ingestOne(ctx context.Context, s *Store, id string) error {
	return s.Write(ctx, func(tx *Tx) error {
		if err := tx.CheckNotFrozen(); err != nil {
			return err
		}
		_, err := tx.exec(`INSERT INTO record (id, device_id, machine_id, kind, v, ts, received_at, body)
			VALUES (?, 'dev_1', 'm_1', 'hook_event', 1, 1, 1, '{}')`, id)
		return err
	})
}

func state(t *testing.T) string {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "pull"), 0o700)
	os.WriteFile(filepath.Join(d, "pair_pepper"), []byte("pepper-bytes"), 0o600)
	os.WriteFile(filepath.Join(d, "hub_sign.key"), []byte("hub-key-bytes"), 0o600)
	os.WriteFile(filepath.Join(d, "pull", "id_ed25519"), []byte("PRIVATE KEY"), 0o600)
	os.WriteFile(filepath.Join(d, "pull", "id_ed25519.pub"), []byte("ssh-ed25519 AAAA"), 0o644)
	return d
}

func TestExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := openTest(t, t.TempDir())
	seedDevice(t, src)
	for i := 0; i < 5; i++ {
		if err := ingestOne(ctx, src, fmt.Sprintf("r%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	src.Write(ctx, func(tx *Tx) error {
		return tx.PutAlertSetting(AlertKeyPublicURL, []byte(`"https://old.example"`), 1)
	})
	out := filepath.Join(t.TempDir(), "exports", "hub.wyx")
	res, err := src.Export(ctx, ExportOptions{Out: out, StateDir: state(t), MovedTo: "https://new.example", WyVersion: "0.5.0"})
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(out); st.Mode().Perm() != 0o600 {
		t.Errorf("archive mode %v", st.Mode())
	}
	if res.Manifest.Counts.Records != 5 || res.Manifest.Counts.Devices != 1 || !res.Manifest.Frozen || res.Manifest.PublicURL != "https://old.example" {
		t.Errorf("%+v", res.Manifest)
	}
	// the source hub is frozen and refuses ingest; frozen state is visible
	if err := ingestOne(ctx, src, "late"); !errors.Is(err, ErrFrozen) {
		t.Fatalf("frozen hub accepted a record: %v", err)
	}
	if f, _ := src.Frozen(ctx); f == nil || f.MovedTo == nil || *f.MovedTo != "https://new.example" {
		t.Fatalf("%+v", f)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(out), ".export-*")); len(left) != 0 {
		t.Errorf("temp left: %v", left)
	}

	// import into an empty data folder
	dst := t.TempDir()
	dstState := t.TempDir()
	os.WriteFile(filepath.Join(dstState, "pair_pepper"), []byte("other"), 0o600)
	ir, err := Import(ctx, ImportOptions{File: out, DBPath: filepath.Join(dst, "hub.db"), StateDir: dstState, WyVersion: "0.5.1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ir.Backups) != 1 || ir.PublicURL != "https://old.example" {
		t.Errorf("%+v", ir)
	}
	if b, _ := os.ReadFile(filepath.Join(dstState, "pair_pepper")); string(b) != "pepper-bytes" {
		t.Errorf("pepper %q", b)
	}
	if b, _ := os.ReadFile(ir.Backups[0]); string(b) != "other" {
		t.Errorf("backup %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dstState, "pull", "id_ed25519")); string(b) != "PRIVATE KEY" {
		t.Errorf("pull key %q", b)
	}
	if st, _ := os.Stat(filepath.Join(dstState, "pull", "id_ed25519")); st.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", st.Mode())
	}
	d := openTest(t, dst)
	if f, _ := d.Frozen(ctx); f != nil {
		t.Error("imported hub is frozen")
	}
	if _, ok, _ := d.AlertSetting(ctx, SettingImported); !ok {
		t.Error("hub.imported not set")
	}
	var n int
	d.R().QueryRow(`SELECT count(*) FROM record`).Scan(&n)
	if n != 5 {
		t.Errorf("records %d", n)
	}
	// the device row (token hash) is there, so the old token authenticates
	var hash []byte
	d.R().QueryRow(`SELECT token_hash FROM device WHERE id='dev_1'`).Scan(&hash)
	if !bytes.Equal(hash, bytes.Repeat([]byte{7}, 32)) {
		t.Error("token hash lost")
	}
	if err := ingestOne(ctx, d, "after"); err != nil {
		t.Fatalf("imported hub refuses ingest: %v", err)
	}
	// --public-url overrides
	dst2 := t.TempDir()
	ir, err = Import(ctx, ImportOptions{File: out, DBPath: filepath.Join(dst2, "hub.db"), StateDir: t.TempDir(), PublicURL: "https://new.example"})
	if err != nil || !ir.URLChanged || ir.PublicURL != "https://new.example" {
		t.Fatalf("%+v %v", ir, err)
	}
}

func TestImportRefusesEachNonEmptyTable(t *testing.T) {
	ctx := context.Background()
	src := openTest(t, t.TempDir())
	seedDevice(t, src)
	out := filepath.Join(t.TempDir(), "x.wyx")
	if _, err := src.Export(ctx, ExportOptions{Out: out, NoFreeze: true}); err != nil {
		t.Fatal(err)
	}
	if f, _ := src.Frozen(ctx); f != nil {
		t.Error("--no-freeze froze the hub")
	}
	inserts := map[string]string{
		"device":      `INSERT INTO device (id, kind, name, machine_id, token_hash, token_hint, scopes, created_at, created_by) VALUES ('d', 'machine', 'n', 'm', zeroblob(32), 'h', '[]', 1, 'cli')`,
		"record":      `INSERT INTO record (id, device_id, machine_id, kind, v, ts, received_at, body) VALUES ('r', 'd', 'm', 'k', 1, 1, 1, '{}')`,
		"machine":     `INSERT INTO machine (id, name, first_seen) VALUES ('m', 'n', 1)`,
		"push_target": `INSERT INTO push_target (id, kind, endpoint, created_at, updated_at) VALUES ('pt', 'ntfy', 'https://x/y', 1, 1)`,
		"pull_target": `INSERT INTO pull_target (id, name, host, user, host_keys, created_at) VALUES ('pt_1', 'a', 'h', 'u', '[]', 1)`,
	}
	for table, ins := range inserts {
		dir := t.TempDir()
		s := openTest(t, dir)
		err := s.Write(ctx, func(tx *Tx) error {
			// satisfy foreign keys only as far as the table needs
			if table == "device" || table == "record" {
				tx.exec(`INSERT INTO machine (id, name, first_seen) VALUES ('m', 'n', 1)`)
			}
			if table == "record" {
				tx.exec(`INSERT INTO device (id, kind, name, machine_id, token_hash, token_hint, scopes, created_at, created_by) VALUES ('d', 'machine', 'n', 'm', zeroblob(32), 'h', '[]', 1, 'cli')`)
			}
			if table == "push_target" {
				tx.exec(`INSERT OR IGNORE INTO channel (kind) VALUES ('ntfy')`)
			}
			_, err := tx.exec(ins)
			return err
		})
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		s.Close()
		before, _ := os.ReadFile(filepath.Join(dir, "hub.db"))
		_, err = Import(ctx, ImportOptions{File: out, DBPath: filepath.Join(dir, "hub.db"), StateDir: t.TempDir()})
		var ne *NonEmptyError
		if !errors.As(err, &ne) || ne.Counts[table] == 0 {
			t.Errorf("%s: want NonEmptyError, got %v", table, err)
			continue
		}
		after, _ := os.ReadFile(filepath.Join(dir, "hub.db"))
		if !bytes.Equal(before, after) {
			t.Errorf("%s: refused import changed the database", table)
		}
	}
	// an existing empty hub is replaced
	dir := t.TempDir()
	openTest(t, dir).Close()
	if _, err := Import(ctx, ImportOptions{File: out, DBPath: filepath.Join(dir, "hub.db"), StateDir: t.TempDir()}); err != nil {
		t.Fatalf("empty hub: %v", err)
	}
}

func TestImportRefusesRunningHub(t *testing.T) {
	ctx := context.Background()
	src := openTest(t, t.TempDir())
	out := filepath.Join(t.TempDir(), "x.wyx")
	if _, err := src.Export(ctx, ExportOptions{Out: out, NoFreeze: true}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	live := openTest(t, dir)
	started := make(chan struct{})
	release := make(chan struct{})
	go live.Write(ctx, func(tx *Tx) error { close(started); <-release; return nil })
	<-started
	_, err := Import(ctx, ImportOptions{File: out, DBPath: filepath.Join(dir, "hub.db"), StateDir: t.TempDir()})
	close(release)
	if !errors.Is(err, ErrHubRunning) {
		t.Fatalf("got %v", err)
	}
}

func TestEncryptedRoundTripFlipAndTruncate(t *testing.T) {
	ctx := context.Background()
	src := openTest(t, t.TempDir())
	seedDevice(t, src)
	out := filepath.Join(t.TempDir(), "enc.wyx")
	res, err := src.Export(ctx, ExportOptions{Out: out, NoFreeze: true, Passphrase: "correct horse", StateDir: state(t)})
	if err != nil || !res.Encrypted {
		t.Fatal(err)
	}
	if enc, _ := IsEncryptedExport(out); !enc {
		t.Fatal("no magic")
	}
	raw, _ := os.ReadFile(out)
	if bytes.Contains(raw, []byte("PRIVATE KEY")) || bytes.Contains(raw, []byte("SQLite format")) {
		t.Fatal("plaintext in the encrypted file")
	}
	imp := func(name string, data []byte, pass string) error {
		p := filepath.Join(t.TempDir(), name)
		os.WriteFile(p, data, 0o600)
		_, err := Import(ctx, ImportOptions{File: p, Passphrase: pass, DBPath: filepath.Join(t.TempDir(), "hub.db"), StateDir: t.TempDir()})
		return err
	}
	if err := imp("ok.wyx", raw, "correct horse"); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if err := imp("nopass.wyx", raw, ""); !errors.Is(err, ErrNeedPassphrase) {
		t.Errorf("no passphrase: %v", err)
	}
	if err := imp("wrong.wyx", raw, "battery staple"); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	for _, off := range []int{20, 40, len(raw) / 2, len(raw) - 1} {
		c := append([]byte{}, raw...)
		c[off] ^= 1
		if err := imp("flip.wyx", c, "correct horse"); err == nil {
			t.Errorf("flipped byte at %d accepted", off)
		}
	}
	for _, n := range []int{len(raw) - 1, len(raw) - 20, len(raw) / 2, 34, 10} {
		if err := imp("trunc.wyx", raw[:n], "correct horse"); err == nil {
			t.Errorf("truncated to %d accepted", n)
		}
	}
	// a truncation exactly on a chunk boundary (drop the last chunk) must fail too
	if len(raw) > 34+4+wyxeChunk+wyxeTag+4 {
		first := 34 + 4 + wyxeChunk + wyxeTag
		if err := imp("boundary.wyx", raw[:first], "correct horse"); err == nil {
			t.Error("chunk-boundary truncation accepted")
		}
	}
	// appended junk fails
	if err := imp("extra.wyx", append(append([]byte{}, raw...), 1, 2, 3, 4, 5), "correct horse"); err == nil {
		t.Error("trailing bytes accepted")
	}
}

func TestPlainArchiveTamperAndTruncate(t *testing.T) {
	ctx := context.Background()
	src := openTest(t, t.TempDir())
	seedDevice(t, src)
	out := filepath.Join(t.TempDir(), "p.wyx")
	if _, err := src.Export(ctx, ExportOptions{Out: out, NoFreeze: true}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	try := func(data []byte) error {
		p := filepath.Join(t.TempDir(), "x.wyx")
		os.WriteFile(p, data, 0o600)
		_, err := Import(ctx, ImportOptions{File: p, DBPath: filepath.Join(t.TempDir(), "hub.db"), StateDir: t.TempDir()})
		return err
	}
	if err := try(raw); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 3, len(raw) / 2, len(raw) - 1, len(raw) - 8} {
		if try(raw[:n]) == nil {
			t.Errorf("truncated to %d accepted", n)
		}
	}
	// A flipped byte must fail whenever it changes what the archive holds. Inside a long run
	// of zeros a flip can change only a back-reference's distance, so the stream inflates to
	// the same bytes and nothing was tampered with: such a flip proves nothing, skip it.
	plain := gunzipAll(t, raw)
	flips := 0
	for _, at := range []int{len(raw) / 3, len(raw) / 2, 2 * len(raw) / 3, len(raw) - 6} {
		c := append([]byte{}, raw...)
		c[at] ^= 0xff
		if out, ok := gunzip(c); ok && bytes.Equal(out, plain) {
			continue
		}
		flips++
		if try(c) == nil {
			t.Errorf("flipped byte at %d accepted", at)
		}
	}
	if flips == 0 {
		t.Error("no flip changed the archive's content")
	}
	// A changed byte inside hub.db, with the gzip stream rebuilt so its checksum is right,
	// is refused by the manifest's sha256.
	db := bytes.Index(plain, []byte("SQLite format 3\x00"))
	if db < 0 || db+5000 > len(plain) {
		t.Fatal("no hub.db in the export")
	}
	tampered := append([]byte{}, plain...)
	tampered[db+5000] ^= 0x01
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(tampered)
	zw.Close()
	if try(buf.Bytes()) == nil {
		t.Error("changed database byte accepted")
	}
}

func gunzip(b []byte) ([]byte, bool) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, false
	}
	out, err := io.ReadAll(zr)
	return out, err == nil
}

func gunzipAll(t *testing.T, b []byte) []byte {
	t.Helper()
	out, ok := gunzip(b)
	if !ok {
		t.Fatal("the export does not inflate")
	}
	return out
}

func TestImportRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	src := openTest(t, t.TempDir())
	out := filepath.Join(t.TempDir(), "x.wyx")
	if _, err := src.Export(ctx, ExportOptions{Out: out, NoFreeze: true}); err != nil {
		t.Fatal(err)
	}
	// rebuild with a manifest claiming a future schema
	dir := t.TempDir()
	m, err := unpack(out, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	m.Schema = LatestVersion() + 1
	if m.Schema <= LatestVersion() {
		t.Fatal("setup")
	}
	// the manifest is covered by nothing but the file hashes, so edit it directly
	mb := mustJSON(m)
	members := []member{{name: memberManifest, data: mb}, {name: memberDB, path: filepath.Join(dir, "hub.db")}}
	var buf bytes.Buffer
	if err := writeMembers(&buf, members); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "future.wyx")
	os.WriteFile(p, buf.Bytes(), 0o600)
	_, err = Import(ctx, ImportOptions{File: p, DBPath: filepath.Join(t.TempDir(), "hub.db"), StateDir: t.TempDir()})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("update wy here first")) {
		t.Fatalf("got %v", err)
	}
}

func TestExportUnderConcurrentIngest(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, t.TempDir())
	seedDevice(t, s)
	var mu sync.Mutex
	accepted := map[string]bool{}
	var refused int32
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				id := fmt.Sprintf("g%d-%d", g, i)
				err := ingestOne(ctx, s, id)
				switch {
				case err == nil:
					mu.Lock()
					accepted[id] = true
					mu.Unlock()
				case errors.Is(err, ErrFrozen):
					atomic.AddInt32(&refused, 1)
					time.Sleep(time.Millisecond)
				default:
					t.Errorf("ingest: %v", err)
					return
				}
			}
		}(g)
	}
	time.Sleep(50 * time.Millisecond)
	out := filepath.Join(t.TempDir(), "c.wyx")
	if _, err := s.Export(ctx, ExportOptions{Out: out}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	close(stop)
	wg.Wait()
	if refused == 0 {
		t.Error("no ingest was refused after the freeze")
	}
	dir := t.TempDir()
	if _, err := Import(ctx, ImportOptions{File: out, DBPath: filepath.Join(dir, "hub.db"), StateDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	d := openTest(t, dir)
	rows, _ := d.R().Query(`SELECT id FROM record`)
	inSnap := map[string]bool{}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		inSnap[id] = true
	}
	// every id the hub accepted before it froze is in the snapshot, and nothing accepted after
	// is: the live database and the snapshot hold the same records.
	var live int
	s.R().QueryRow(`SELECT count(*) FROM record`).Scan(&live)
	if len(accepted) != live || len(inSnap) != live {
		t.Fatalf("accepted %d, live %d, snapshot %d", len(accepted), live, len(inSnap))
	}
	for id := range accepted {
		if !inSnap[id] {
			t.Fatalf("accepted %s is missing from the snapshot", id)
		}
	}
}

func TestFreezeUnfreeze(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, t.TempDir())
	if was, err := s.Unfreeze(ctx); err != nil || was {
		t.Fatal(was, err)
	}
	// a failing export (output exists) does not freeze at all; one failing later unfreezes
	out := filepath.Join(t.TempDir(), "o.wyx")
	os.WriteFile(out, nil, 0o600)
	if _, err := s.Export(ctx, ExportOptions{Out: out}); err == nil {
		t.Fatal("export over an existing file")
	}
	if f, _ := s.Frozen(ctx); f != nil {
		t.Error("frozen by a refused export")
	}
	bad := filepath.Join(t.TempDir(), "o2.wyx")
	if _, err := s.Export(ctx, ExportOptions{Out: bad}); err != nil {
		t.Fatalf("no state folder given is fine: %v", err)
	}
	f, fresh, err := s.Freeze(ctx, "")
	if err != nil || fresh || f.Since == 0 {
		t.Fatalf("second freeze: %+v %v %v", f, fresh, err)
	}
	if was, err := s.Unfreeze(ctx); err != nil || !was {
		t.Fatal(was, err)
	}
	var _ sql.NullInt64
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
