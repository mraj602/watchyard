//go:build windows

package grokpeer

import "errors"

// Lock is unsupported on Windows (peers are too).
type Lock struct{}

func tryLock(string) (*Lock, error) {
	return nil, errors.New("grok peers are not supported on Windows")
}

// Unlock does nothing.
func (*Lock) Unlock() {}

func spawnDetached(string) error { return errors.New("grok peers are not supported on Windows") }
