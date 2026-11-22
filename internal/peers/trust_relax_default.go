//go:build !trusttest

package peers

// TrustGuardOff is false in every real build: `wy peers trust doorbell` (and --off) refuse
// inside a Claude Code session and ask y/N on a terminal (peers.md 4.1a). The trusttest build
// tag (trust_relax_trusttest.go) is a developer test aid that never ships.
const TrustGuardOff = false

// TrustTestWarning is empty outside the trusttest build.
const TrustTestWarning = ""
