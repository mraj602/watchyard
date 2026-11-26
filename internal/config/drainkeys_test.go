package config

import (
	"testing"

	"github.com/mraj602/watchyard/internal/projection"
)

func TestDrainKeyDefaultsMatchProjection(t *testing.T) {
	c := Defaults()
	if c.Account.DrainAim7d != projection.DrainAimFar || c.Account.DrainCapMax7d != projection.DrainCapMax7 {
		t.Fatalf("defaults %v %v, want %v %v", c.Account.DrainAim7d, c.Account.DrainCapMax7d, projection.DrainAimFar, projection.DrainCapMax7)
	}
}
