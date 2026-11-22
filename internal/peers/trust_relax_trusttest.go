//go:build trusttest

package peers

// The trusttest build (peers.md 15): a developer test aid that never ships. `wy peers trust
// doorbell` and `--off` may run inside a Claude Code session and ask no question, so an agent
// can test the trusted sender unattended. Everything else (the identity check, tag-only sends,
// the audit, the record's format) is the same as the release build.

// TrustGuardOff turns the approval guard off.
const TrustGuardOff = true

// TrustTestWarning is printed by every approval and revocation of this build.
// check-release-seams.sh proves a release binary does not contain it.
const TrustTestWarning = "TEST BUILD: approval guard is off"
