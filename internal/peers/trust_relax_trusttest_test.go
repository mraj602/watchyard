//go:build trusttest

package peers_test

import (
	"testing"

	"github.com/mraj602/watchyard/internal/peers"
)

func TestTrustGuardOffInTrustTestBuild(t *testing.T) {
	if !peers.TrustGuardOff || peers.TrustTestWarning != "TEST BUILD: approval guard is off" {
		t.Fatal("the trusttest build keeps the guard")
	}
}
