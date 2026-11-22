//go:build trusttest

package peers

// The trusttest build (peers.md 15): a developer test aid that never ships. It turns off the
// approval guard (CheckApprover: the terminal, the environment names, the walks) and nothing
// else: `wy peers trust doorbell` and `--off` may run inside a Claude Code session, without a
// terminal, and ask no question, so the trusted sender can be tested unattended. The identity
// check of a send, the install checks, tag-only sends and the audit are the release build's;
// the record it writes is marked test_build, which every build honours and shows.

// TrustGuardOff turns the approval guard off.
const TrustGuardOff = true

// TrustTestWarning is printed by every approval and revocation of this build.
// check-release-seams.sh proves a release binary does not contain it.
const TrustTestWarning = "TEST BUILD: approval guard is off"
