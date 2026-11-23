package peers

import "fmt"

// Refusal codes: the one reason word a refused or failed message carries (peers.md 4.2, 7.2).
const (
	CodeOff            = "off"
	CodePaused         = "paused"
	CodeNotAllowed     = "not_allowed"
	CodeLoopGuard      = "loop_guard"
	CodeTooBig         = "too_big"
	CodeNotText        = "not_text"
	CodeSecret         = "secret"
	CodeRateLimited    = "rate_limited"
	CodeUnknownTarget  = "unknown_target"
	CodeTargetGone     = "target_gone"
	CodeNotASession    = "not_a_session"
	CodeAmbiguous      = "ambiguous"
	CodeNoMatrix       = "no_matrix"
	CodeExpired        = "expired"
	CodeRejected       = "rejected"
	CodeFailed         = "failed"
	CodeUnsupported    = "unsupported"
	CodeHubUnreachable = "hub_unreachable"
	// CodeNoHolder: a tag send found no holder it may deliver to (peers.md 3.4); exit 3 like
	// unsupported, since both mean "fall back now".
	CodeNoHolder = "no_holder"
	// CodeUsage: a malformed argument (a tag that is not a slug); exit 2.
	CodeUsage = "usage"
	// CodeLimitTags: the session holds the most tags it may (peers.md 3.4); exit 6.
	CodeLimitTags = "too_many_tags"
	// CodeHeld: a trusted send (peers.md 4.1a) that Claude Code would or might hold for
	// approval: the receiving session's permission mode is not known (nothing written), or an
	// older receiving machine framed it in another class; exit 9, never a success.
	CodeHeld = "held"
)

// ReasonReceiverTooOld is a hub reason word (not an exit code): the receiving machine's agent
// did not announce the peer_service capability, so it cannot take a trusted sender's message
// (peers.md 8.3).
const ReasonReceiverTooOld = "receiver_too_old"

// Exit codes of `wy peers` (peers.md 7.2).
const (
	ExitOK          = 0
	ExitInternal    = 1
	ExitUsage       = 2
	ExitUnsupported = 3
	ExitGuard       = 4 // refused by the signing guard, or not inside a session
	ExitPolicy      = 5
	ExitLimits      = 6
	ExitNotSent     = 7
	ExitQueued      = 8
	ExitHeld        = 9 // a trusted send held or likely held for approval (peers.md 4.1a)
)

// Refusal is a final "no" with its reason word. Nothing is retried after one.
type Refusal struct {
	Code   string
	Detail string
}

func (r *Refusal) Error() string {
	if r.Detail == "" {
		return r.Code
	}
	return r.Code + ": " + r.Detail
}

// Refuse builds a Refusal with a formatted detail.
func Refuse(code, format string, args ...any) *Refusal {
	return &Refusal{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// ExitCode maps a refusal code to the exit code of peers.md 7.2. unknown_target and
// ambiguous targets are 2 only when the target did not resolve at all (a usage problem).
func ExitCode(code string) int {
	switch code {
	case CodeOff, CodePaused, CodeNotAllowed, CodeLoopGuard, CodeNoMatrix:
		return ExitPolicy
	case CodeTooBig, CodeNotText, CodeSecret, CodeRateLimited, CodeLimitTags:
		return ExitLimits
	case CodeAmbiguous, CodeUsage:
		return ExitUsage
	case CodeNotASession:
		return ExitGuard
	case CodeUnsupported, CodeNoHolder:
		return ExitUnsupported
	case CodeUnknownTarget, CodeTargetGone, CodeExpired, CodeRejected, CodeFailed, CodeHubUnreachable:
		return ExitNotSent
	case CodeHeld:
		return ExitHeld
	}
	return ExitInternal
}
