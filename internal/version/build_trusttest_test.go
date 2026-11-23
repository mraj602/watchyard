//go:build trusttest

package version

import (
	"strings"
	"testing"
)

func TestTrustBuildSuffix(t *testing.T) {
	if !strings.HasPrefix(String(), "wy "+Version+"+trusttest (") {
		t.Fatalf("String() = %q", String())
	}
}
