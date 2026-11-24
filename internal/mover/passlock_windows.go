//go:build windows

package mover

// PassLock: moving sessions is not supported on Windows; a pass moves nothing there.
func (s State) PassLock() (unlock func(), ok bool, err error) { return func() {}, true, nil }
