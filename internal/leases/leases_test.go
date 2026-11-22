package leases

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func lease(key, sid string, at time.Time) Line {
	c5 := 8.0
	return Line{Op: OpLease, ID: "L-" + key + "-" + sid, At: at, Key: key, Kind: KindAggressive, SessionID: sid, C5: &c5}
}

func TestReadSkipsAndCountsBadLines(t *testing.T) {
	g := Ledger{Dir: t.TempDir()}
	if err := g.Append(lease("acct:a/o", "g1", now.Add(-6*time.Minute))); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(g.Dir, "leases", dayName(now)), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	at := now.Add(-time.Minute).Format(time.RFC3339)
	for _, l := range []string{
		`{"v":1,"op":"lease","at":"` + at + `"}` + "\n", // no key
		"5\n",    // not an object
		"null\n", // not an object
		`{"v":1,"op":"lease","key":"k","at":"yesterday"}` + "\n",    // bad time
		`{"v":1,"op":"lease","key":"k","at":null}` + "\n",           // no time
		"\xff\xfe\x00garbage\n",                                     // not JSON
		`{"v":1,"op":"reserve","key":"k","at":"` + at + `"}` + "\n", // unknown op
		`{"v":2,"op":"lease","key":"k","at":"` + at + `"}` + "\n",   // unknown version
		`{"v":1,"op":"launched","at":"` + at + `"}` + "\n",          // no session id
		"\n", // blank: not counted
		`{"v":1,"op":"lease","key":"k","at":"` + at + `","session_id":"g2"`, // torn last line
	} {
		if _, err := f.WriteString(l); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	got, skipped, err := g.Read(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "g1" || skipped != 10 {
		t.Fatalf("got %d leases (%v), skipped %d; want g1 only, 10 skipped", len(got), got, skipped)
	}
	// A lease written after a torn last line starts a line of its own and reads.
	if err := g.Append(lease("acct:b/o", "n1", now)); err != nil {
		t.Fatal(err)
	}
	got, skipped, _ = g.Read(now)
	if skipped != 10 || len(got) != 2 || got[1].SessionID != "n1" {
		t.Fatalf("after append to a torn file: %v, %d skipped; want g1 and n1, 10 skipped", got, skipped)
	}
}

func TestAppendReadRoundTripAndModes(t *testing.T) {
	g := Ledger{Dir: t.TempDir()}
	l := lease("acct:a/o", "s1", now)
	l.Account, l.Org, l.Folder, l.Label, l.Model, l.Strategy, l.Batch = "a", "o", "/f", "claude-2", "opus", "lanes", "B1"
	l.Urgent = true
	cf := 10.0
	l.CF = &cf
	if err := g.Append(l); err != nil {
		t.Fatal(err)
	}
	got, skipped, err := g.Read(now)
	if err != nil || skipped != 0 || len(got) != 1 {
		t.Fatalf("read: %v %d %v", got, skipped, err)
	}
	r := got[0]
	if r.V != 1 || r.Key != l.Key || r.Account != "a" || r.Org != "o" || r.Folder != "/f" || r.Label != "claude-2" ||
		r.Model != "opus" || r.Strategy != "lanes" || r.Batch != "B1" || !r.Urgent || *r.C5 != 8 || *r.CF != 10 ||
		!r.At.Equal(now) || !r.Launched.IsZero() || !r.Ended.IsZero() {
		t.Fatalf("round trip lost a field: %+v", r)
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{
			filepath.Join(g.Dir, "leases"):               0o700,
			filepath.Join(g.Dir, "leases", dayName(now)): 0o600,
		} {
			st, err := os.Stat(p)
			if err != nil || st.Mode().Perm() != want {
				t.Fatalf("%s: mode %v, want %v (%v)", p, st.Mode().Perm(), want, err)
			}
		}
	}
	b, _ := os.ReadFile(filepath.Join(g.Dir, "leases", dayName(now)))
	if strings.Count(string(b), "\n") != 1 || !strings.HasPrefix(string(b), `{"v":1,"op":"lease"`) {
		t.Fatalf("one line per lease: %q", b)
	}
}

func TestMarkLaunchedAndEnded(t *testing.T) {
	g := Ledger{Dir: t.TempDir()}
	if ok, err := g.Mark("s1", OpLaunched, now); ok || err != nil {
		t.Fatalf("no ledger: Mark = %v %v, want nothing written", ok, err)
	}
	// A lease picked yesterday (UTC) is still found.
	if err := g.Append(lease("acct:a/o", "s1", now.Add(-13*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if ok, _ := g.Mark("other", OpLaunched, now); ok {
		t.Fatal("Mark wrote a line for a session id with no lease")
	}
	if ok, err := g.Mark("s1", OpLaunched, now.Add(-12*time.Hour)); !ok || err != nil {
		t.Fatalf("Mark launched = %v %v", ok, err)
	}
	if ok, _ := g.Mark("s1", OpLaunched, now); ok {
		t.Fatal("a resumed session wrote a second launched line")
	}
	if ok, _ := g.Mark("s1", "busy", now); ok {
		t.Fatal("Mark accepted an unknown op")
	}
	if ok, _ := g.Mark("s1", OpEnded, now); !ok {
		t.Fatal("Mark ended wrote nothing")
	}
	got, skipped, _ := g.Read(now)
	if len(got) != 1 || skipped != 0 || !got[0].Launched.Equal(now.Add(-12*time.Hour)) || !got[0].Ended.Equal(now) {
		t.Fatalf("lease after marks: %+v (skipped %d)", got, skipped)
	}
	if got[0].Dropped(now) {
		t.Fatal("a launched lease is never dropped")
	}
	// Two days later the lease is out of the hook's window: nothing is written.
	if ok, _ := g.Mark("s1", OpEnded, now.Add(48*time.Hour)); ok {
		t.Fatal("Mark looked further back than yesterday")
	}
}

func TestUnlaunchedDroppedAfterTenMinutes(t *testing.T) {
	young := Lease{Line: lease("k", "u2", now.Add(-3*time.Minute))}
	old := Lease{Line: lease("k", "u1", now.Add(-15*time.Minute))}
	if young.Dropped(now) || !old.Dropped(now) {
		t.Fatalf("Dropped: young %v old %v; want false true", young.Dropped(now), old.Dropped(now))
	}
}

func TestReusedSessionIDMarksTheNewestLease(t *testing.T) {
	g := Ledger{Dir: t.TempDir()}
	// The first launch never happened; the id is leased again later and then launched.
	if err := g.Append(lease("acct:a/o", "s1", now.Add(-30*time.Minute)), lease("acct:b/o", "s1", now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if ok, _ := g.Mark("s1", OpLaunched, now); !ok {
		t.Fatal("Mark wrote nothing for the new lease")
	}
	got, _, _ := g.Read(now)
	if len(got) != 2 || !got[0].Launched.IsZero() || !got[1].Launched.Equal(now) || !got[0].Dropped(now) {
		t.Fatalf("leases: %+v", got)
	}
}

func TestReadsEightDaysAndPrunesOlder(t *testing.T) {
	g := Ledger{Dir: t.TempDir()}
	for d := 0; d <= 9; d++ {
		if err := g.Append(lease("k", fmt.Sprintf("d%d", d), now.AddDate(0, 0, -d))); err != nil {
			t.Fatal(err)
		}
	}
	stray := filepath.Join(g.Dir, "leases", "notes.txt")
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, _ := g.Read(now)
	if len(got) != Days || got[0].SessionID != "d7" || got[len(got)-1].SessionID != "d0" {
		t.Fatalf("read %d leases from %v to %v; want %d, oldest first", len(got), got[0].SessionID, got[len(got)-1].SessionID, Days)
	}
	if err := g.Prune(now); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Join(g.Dir, "leases"))
	if len(ents) != Days+1 { // 8 day files and the stray file
		t.Fatalf("after prune: %d entries, want %d", len(ents), Days+1)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatal("prune deleted a file that is not a day file")
	}
	if err := (Ledger{Dir: t.TempDir()}).Prune(now); err != nil {
		t.Fatalf("prune with no folder: %v", err)
	}
}

func TestConcurrentPickersLoseNothing(t *testing.T) {
	g := Ledger{Dir: t.TempDir()}
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for k := 0; k < 4; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				unlock, err := g.Lock()
				if err != nil {
					errs <- err
					return
				}
				ls, _, err := g.Read(now)
				if err == nil {
					// Pick the key with the fewest leases, like a burst cap would spread them.
					n := map[string]int{}
					for _, l := range ls {
						n[l.Key]++
					}
					key := "a"
					for _, c := range []string{"b", "c", "d"} {
						if n[c] < n[key] {
							key = c
						}
					}
					err = g.Append(lease(key, fmt.Sprintf("p%d-%d", k, j), now))
				}
				unlock()
				if err != nil {
					errs <- err
					return
				}
			}
		}(k)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	got, skipped, _ := g.Read(now)
	seen := map[string]bool{}
	per := map[string]int{}
	for _, l := range got {
		seen[l.SessionID] = true
		per[l.Key]++
	}
	if len(got) != 40 || len(seen) != 40 || skipped != 0 {
		t.Fatalf("%d leases, %d distinct, %d skipped; want 40, 40, 0", len(got), len(seen), skipped)
	}
	for k, n := range per {
		if n != 10 {
			t.Fatalf("pickers did not see each other's leases under the lock: %v (%s)", per, k)
		}
	}
}

func TestPendingAndLast(t *testing.T) {
	ls := []Lease{
		{Line: lease("a", "", now.Add(-20*time.Minute))},
		{Line: lease("a", "", now.Add(-5*time.Minute))},
		{Line: lease("b", "", now.Add(-time.Minute))},
		{Line: lease("c", "", now.Add(time.Minute))}, // from a clock ahead: not pending
	}
	p := Pending(ls, now, 12*time.Minute)
	if p["a"] != 1 || p["b"] != 1 || p["c"] != 0 {
		t.Fatalf("Pending = %v", p)
	}
	last, key := Last(ls)
	if !last["a"].Equal(now.Add(-5*time.Minute)) || key != "c" {
		t.Fatalf("Last = %v %q", last, key)
	}
}
