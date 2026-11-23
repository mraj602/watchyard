//go:build linux

package peers

import (
	"net"

	"golang.org/x/sys/unix"
)

// soPeerPIDFD is SO_PEERPIDFD (Linux 6.5; the asm-generic value, which x/sys/unix does not
// name yet).
const soPeerPIDFD = 77

// PeerAlive takes a pidfd of the process on the other end of c (SO_PEERPIDFD) and returns a
// check that it is still alive, and a close for the pidfd. A pid is not reused while its
// process lives, so a /proc read that this check follows was of the process that connected
// (peers.md 2.3). alive is nil when the kernel cannot give a pidfd; the callers then rely on
// the start times alone.
func PeerAlive(c *net.UnixConn) (alive func() bool, closeFn func()) {
	closeFn = func() {}
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, closeFn
	}
	fd := -1
	_ = raw.Control(func(s uintptr) {
		if v, err := unix.GetsockoptInt(int(s), unix.SOL_SOCKET, soPeerPIDFD); err == nil {
			fd = v
		}
	})
	if fd < 0 {
		return nil, closeFn
	}
	return func() bool { return unix.PidfdSendSignal(fd, 0, nil, 0) == nil }, func() { _ = unix.Close(fd) }
}
