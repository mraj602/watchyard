//go:build !linux

package peers

import "net"

// PeerAlive has no pidfd off Linux: the callers rely on the start times alone.
func PeerAlive(*net.UnixConn) (alive func() bool, closeFn func()) { return nil, func() {} }
