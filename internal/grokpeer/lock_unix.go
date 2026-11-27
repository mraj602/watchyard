//go:build !windows

package grokpeer

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// Lock is an exclusive advisory lock on a session's lock file; it dies with the process.
type Lock struct{ f *os.File }

func tryLock(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f}, nil
}

// Unlock releases the lock.
func (l *Lock) Unlock() {
	if l == nil || l.f == nil {
		return
	}
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

// spawnDetached starts `<this wy> grok drain <id>` in its own session with no stdio.
func spawnDetached(id string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(exe, "grok", "drain", id)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}
