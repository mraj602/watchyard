//go:build !trusttest

package version

import (
	"strings"
	"testing"
)

// A normal build has no build suffix (peers.md 15: only the trusttest build shows one).
func TestTrustBuildSuffixAbsent(t *testing.T) {
	if strings.Contains(String(), "+trusttest") {
		t.Fatalf("String() = %q", String())
	}
}
