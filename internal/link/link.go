// Package link is the wire format of the link (docs/spec/link.md), shared by the hub
// (internal/hub/link), the machine client (internal/linkclient) and the test clients
// (internal/link/linktest): frame shapes, the signed inputs of the challenge, the hello and
// every hub frame, frame verification in link.md 4.3's order, HPKE sealing for machines,
// close codes and frame types. The phones' RFC 8291 sealing is internal/hub/push/webpush's
// Encrypt, called by the hub.
//
// Standard library and internal/keys only.
//
// Contract: docs/spec/link.md 3-8.
package link

import (
	"errors"
	"regexp"
	"slices"
)

// Subprotocol is the WebSocket subprotocol (link.md 2).
const Subprotocol = "wy-link.1"

// Subprotocols is what a client offers and the hub accepts.
var Subprotocols = []string{Subprotocol}

// MaxMessage is the largest WebSocket message either side reads (link.md 2).
const MaxMessage = 1 << 20

// Version is the `v` of the challenge, the hello and every body.
const Version = 1

// CtxQueue is the signature context of queued frames (link.md 4.3).
const CtxQueue = "queue"

// Hub→device frame types (link.md 6.3, 6.4).
const (
	TypeChallenge    = "challenge"
	TypeWelcome      = "welcome"
	TypeActionResult = "action_result"
	TypePeers        = "peers"
	TypePeerAck      = "peer_ack"
	TypeNotice       = "notice"

	TypeAlert      = "alert"
	TypeRefresh    = "refresh"
	TypePeerMsg    = "peer_msg"
	TypePeerStatus = "peer_status"
	TypeMatrix     = "matrix"
	TypeRevoke     = "revoke"
	// TypePeerThreadResume clears a thread's loop-guard stop on a machine (peers.md 6.3).
	TypePeerThreadResume = "peer_thread_resume"
)

// Device→hub frame types (link.md 6.2). peer_msg is both a device→hub frame and a queued
// hub→device frame.
const (
	TypeHello      = "hello"
	TypeAck        = "ack"
	TypeReject     = "reject"
	TypeAction     = "action"
	TypePeerResult = "peer_result"
	TypePeerLog    = "peer_log"
	TypePeersList  = "peers_list"
	// TypePeerTag adds or removes a session's tag (peers.md 3.4); answered with `peers`.
	TypePeerTag = "peer_tag"
)

// Queued and Ephemeral list the hub→device types (link.md 6.3, 6.4).
var (
	Queued    = []string{TypeAlert, TypeRefresh, TypePeerMsg, TypePeerStatus, TypeMatrix, TypeRevoke, TypePeerThreadResume}
	Ephemeral = []string{TypeWelcome, TypeActionResult, TypePeers, TypePeerAck, TypeNotice}
)

// IsQueued reports whether t is a queued type; for an unknown type the seq decides, as
// the Android client does (Frames.isQueued).
func IsQueued(t string, seq int64) bool {
	switch {
	case slices.Contains(Queued, t):
		return true
	case slices.Contains(Ephemeral, t):
		return false
	}
	return seq > 0
}

// tagRE is link.md 5.3's tag rule (also a schema CHECK).
var tagRE = regexp.MustCompile(`^[A-Za-z0-9_:.-]{0,64}$`)

// ValidTag reports whether tag may be a collapse key.
func ValidTag(tag string) bool { return tagRE.MatchString(tag) }

// Close codes (link.md 8).
const (
	CloseNormal       = 1000
	CloseGoingAway    = 1001
	CloseBadFrame     = 4400
	CloseUnauthorized = 4401
	CloseRevoked      = 4403
	CloseIdle         = 4408
	CloseSuperseded   = 4409
	CloseTooBig       = 4413
	CloseUpgrade      = 4426
	CloseRateLimited  = 4429
	CloseInternal     = 4500
	CloseFrozen       = 4503
)

var closeWords = map[int]string{
	CloseNormal: "normal", CloseGoingAway: "going_away", CloseBadFrame: "bad_frame", CloseUnauthorized: "unauthorized",
	CloseRevoked: "revoked", CloseIdle: "idle", CloseSuperseded: "superseded", CloseTooBig: "too_big",
	CloseUpgrade: "upgrade", CloseRateLimited: "rate_limited", CloseInternal: "internal", CloseFrozen: "frozen",
}

// CloseWord is the word of a close code ("closed" for one this version does not know).
func CloseWord(code int) string {
	if w, ok := closeWords[code]; ok {
		return w
	}
	return "closed"
}

// Errors of frame checks. A frame that fails any of them is rejected (link.md 4.3).
var (
	ErrSignature = errors.New("link: bad signature")
	ErrSeq       = errors.New("link: seq does not match the frame type")
	ErrFrame     = errors.New("link: malformed frame")
	ErrOpen      = errors.New("link: body does not open")
)
