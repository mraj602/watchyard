//go:build unix

package mover

import (
	"errors"
	"os"
	"syscall"
)

// PassLock takes <state>/move/pass.lock without waiting, so only one Smart Account Switch
// pass runs at a time (a pass may wait minutes for a step to end). ok is false when another
// pass holds it; unlock releases it.
func (s State) PassLock() (unlock func(), ok bool, err error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(s.path("pass.lock"), os.O_CREATE|os.O_RDWR, 0o600)
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
