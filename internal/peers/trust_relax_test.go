//go:build !trusttest

package peers_test

import (
	"testing"

	"github.com/mraj602/watchyard/internal/peers"
)

// A normal build keeps the approval guard (peers.md 4.1a, 15).
func TestTrustGuardOnInDefaultBuild(t *testing.T) {
	if peers.TrustGuardOff || peers.TrustTestWarning != "" {
		t.Fatal("the approval guard is off in a default build")
	}
}
