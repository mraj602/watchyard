//go:build unix

package mover

import (
	"errors"
	"os"
	"syscall"
)

// PassLock takes <state>/move/pass.lock without waiting, so only one Smart Account Switch
// pass plans at a time. ok is false when another pass holds it; unlock releases it.
func (s State) PassLock() (unlock func(), ok bool, err error) {
	return s.flock(s.path("pass.lock"))
}

// SessionLock takes <state>/move/locks/<session>.lock without waiting: held while a move of
// the session waits for its step and runs, so another pass (or wy move by hand) leaves it.
func (s State) SessionLock(sessionID string) (unlock func(), ok bool, err error) {
	if !sidRe.MatchString(sessionID) {
		return nil, false, errors.New("bad session id")
	}
	if err := os.MkdirAll(s.path("locks"), 0o700); err != nil {
		return nil, false, err
	}
	return s.flock(s.path("locks", sessionID+".lock"))
}

// Moving says whether a move of the session holds its lock now.
func (s State) Moving(sessionID string) bool {
	unlock, ok, err := s.SessionLock(sessionID)
	if err != nil {
		return false
	}
	if ok {
		unlock()
	}
	return !ok
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
