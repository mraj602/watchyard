//go:build unix

package mover

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// PassLock takes <state>/move/pass.lock without waiting, so only one Smart Account Switch
// pass plans at a time. ok is false when another pass holds it; unlock releases it.
func (s State) PassLock() (unlock func(), ok bool, err error) {
	return s.flock(s.path("pass.lock"))
}

// SessionLock takes <state>/move/locks/<session>.lock: held while a move of the session
// waits for its step and runs, so another pass (or wy move by hand) leaves it. It waits at
// most sessionLockWait (a Moving probe holds the lock for an instant), never for a move.
func (s State) SessionLock(sessionID string) (unlock func(), ok bool, err error) {
	if !sidRe.MatchString(sessionID) {
		return nil, false, errors.New("bad session id")
	}
	if err := os.MkdirAll(s.path("locks"), 0o700); err != nil {
		return nil, false, err
	}
	deadline := time.Now().Add(sessionLockWait)
	for {
		unlock, ok, err = s.flock(s.path("locks", sessionID+".lock"))
		if ok || err != nil || time.Now().After(deadline) {
			return unlock, ok, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// sessionLockWait: how long SessionLock tries before it says another move holds the lock.
const sessionLockWait = time.Second

// Moving says whether a move of the session holds its lock now. Read-only (wy move --plan
// runs it every few seconds alongside the passes and moves by hand): it creates nothing and
// only tests the lock with a shared, non-blocking flock.
func (s State) Moving(sessionID string) bool {
	if !sidRe.MatchString(sessionID) {
		return false
	}
	f, err := os.Open(s.path("locks", sessionID+".lock"))
	if err != nil {
		return false // no lock file: never moved
	}
	defer f.Close()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	return errors.Is(err, syscall.EWOULDBLOCK)
}

func (s State) flock(path string) (unlock func(), ok bool, err error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	switch {
	case err == nil:
		return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		_ = f.Close()
		return nil, false, nil
	}
	_ = f.Close()
	return nil, false, err
}
