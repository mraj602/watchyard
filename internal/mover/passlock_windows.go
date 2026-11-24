//go:build windows

package mover

// PassLock: moving sessions is not supported on Windows; a pass moves nothing there.
func (s State) PassLock() (unlock func(), ok bool, err error) { return func() {}, true, nil }

// SessionLock: as PassLock, nothing is moved on Windows.
func (s State) SessionLock(string) (unlock func(), ok bool, err error) { return func() {}, true, nil }

// Moving: no move runs on Windows.
func (s State) Moving(string) bool { return false }
