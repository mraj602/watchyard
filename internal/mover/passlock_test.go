//go:build unix

package mover

import (
	"os"
	"path/filepath"
	"testing"
)

// wy move --plan runs Moving every few seconds beside the passes and the moves by hand: it
// writes nothing, says true only while a move holds the session's lock, and never makes a
// move's SessionLock fail.
func TestMovingReadOnly(t *testing.T) {
	st := State{Dir: filepath.Join(t.TempDir(), "move")}
	sid := "0d2d67fd-2f68-4bd4-bf8c-9970f3a720a3"
	if st.Moving(sid) {
		t.Fatal("moving before any lock")
	}
	if _, err := os.Stat(st.Dir); !os.IsNotExist(err) {
		t.Fatalf("Moving created %s (%v)", st.Dir, err)
	}
	unlock, ok, err := st.SessionLock(sid)
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	if !st.Moving(sid) {
		t.Error("not moving while the lock is held")
	}
	if _, ok2, _ := st.SessionLock(sid); ok2 {
		t.Error("a second SessionLock got the held lock")
	}
	unlock()
	if st.Moving(sid) {
		t.Error("moving after the unlock")
	}
	// Probes side by side with a lock taker: the lock is always got.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				st.Moving(sid)
			}
		}
	}()
	for i := 0; i < 50; i++ {
		u, ok, err := st.SessionLock(sid)
		if err != nil || !ok {
			t.Fatalf("lock %d beside probes: %v %v", i, ok, err)
		}
		u()
	}
	close(stop)
	<-done
}
