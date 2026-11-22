// Package picker implements `wy account next`: it recommends the account (config folder)
// to start the next Claude Code session on, from each folder's login on this machine and
// the usage limits of the seat it is logged in to, with pluggable strategies, per-account
// settings and a lease per pick (internal/leases) so bursts of launches spread out.
//
// It only recommends. It never switches a running session and never changes a login.
//
// Contract: docs/spec/setup.md 8.5.
//
// Depends on: config, record, leases, calib.
package picker

import (
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/calib"
)

// Sample is one reading of a window: percent used at a time.
type Sample struct {
	At  time.Time
	Pct float64
	// ResetsAt is the window end the reading named (zero: none). Source is the hub's sample
	// source ("statusline", "web") or "local" for this machine's outbox.
	ResetsAt time.Time
	Source   string
}

// Window is the newest reading of one limit window of a seat, with its recent history.
type Window struct {
	Key       string
	UsedPct   float64
	ResetsAt  time.Time // zero: unknown (an idle window that starts on first use)
	UpdatedAt time.Time // when the reading was taken
	Idle      bool      // the window has not started (hub state idle)
	// Unknown: there is no reading at all for this window of the seat.
	Unknown bool
	// Samples is the history of this key, oldest first (any order is accepted).
	Samples []Sample
	// Source is where the newest reading came from: "hub" or "local".
	Source string
}

// Candidate is one config folder on this machine with what is known about its seat.
type Candidate struct {
	Folder  string // "~/.claude-3"
	Dir     string // absolute
	Default bool   // ~/.claude with CLAUDE_CONFIG_DIR unset (state in ~/.claude.json)
	Label   string // this machine's label (config slots.labels, else the folder name)

	LoggedIn bool
	Account  string // account uuid ("" when unknown)
	Org      string // org uuid
	Email    string

	SeatID    string // hub seat id; "" when no hub seat matched
	HubLabel  string
	MatchedBy string // how the hub seat was found: "account", "place", "email"
	// SignedOut is set when the hub lists this folder on this machine as signed out.
	SignedOut    *time.Time
	LiveSessions int

	Windows []Window
	// Source of the usage data: "hub", "hub+local", "local" or "none".
	Source string
	// RemoteLeases are other machines' leases of the seat, from the hub (lanes), or from the
	// kept copy of the last hub read when RemoteCopy is set.
	RemoteLeases []HubLease
	RemoteCopy   bool
	// OwnLeases are this machine's recent leases of the seat as the hub measured them (lanes).
	OwnLeases []HubLease
	// Calib is the hub's calibration of the seat (lanes; nil: none).
	Calib *calib.Factors
}

// Key is the identity used for leases, burst caps and the round robin: the seat (account and
// org) when known, so two folders logged in to one seat share their leases; else the
// hub seat id; else the folder.
func (c Candidate) Key() string {
	switch {
	case c.Account != "":
		return "acct:" + c.Account + "/" + c.Org
	case c.SeatID != "":
		return "seat:" + c.SeatID
	}
	return "slot:" + c.Folder
}

// Window kinds. A five-hour kind is capped by cap_5h, seven_day by cap_7d, a model-scoped
// weekly window by cap_model.
const (
	Kind5h      = "5h"
	Kind7d      = "7d"
	KindModel5h = "model_5h"
	KindModel7d = "model_7d"
)

// windowKind classifies a window key for a model ("" = no model given). ok is false for
// windows that do not limit a Claude Code session of that model (other models' weeks,
// apps, cowork, cloud credits).
func windowKind(key, model string) (kind string, length time.Duration, ok bool) {
	switch key {
	case "five_hour":
		return Kind5h, 5 * time.Hour, true
	case "seven_day":
		return Kind7d, 7 * 24 * time.Hour, true
	case "seven_day_oauth_apps", "seven_day_cowork":
		return "", 0, false
	}
	var rest string
	var long bool
	switch {
	case strings.HasPrefix(key, "seven_day_"):
		rest, long = strings.TrimPrefix(key, "seven_day_"), true
	case strings.HasPrefix(key, "weekly_"):
		rest, long = strings.TrimPrefix(key, "weekly_"), true
	case strings.HasPrefix(key, "session_"):
		rest = strings.TrimPrefix(key, "session_")
	default:
		return "", 0, false
	}
	if model == "" || rest == "" || !modelMatches(rest, model) {
		return "", 0, false
	}
	if long {
		return KindModel7d, 7 * 24 * time.Hour, true
	}
	return KindModel5h, 5 * time.Hour, true
}

// modelMatches: "opus" matches --model opus, Opus and claude-opus-5.
func modelMatches(windowModel, model string) bool {
	a, b := strings.ToLower(windowModel), strings.ToLower(model)
	return strings.Contains(b, a) || strings.Contains(a, b)
}

func weekly(kind string) bool { return kind == Kind7d || kind == KindModel7d }
func short(kind string) bool  { return kind == Kind5h || kind == KindModel5h }

// limitKey reports whether a window key can limit a Claude Code session of some model.
func limitKey(key string) bool {
	for _, pre := range []string{"seven_day_", "weekly_", "session_"} {
		if m, ok := strings.CutPrefix(key, pre); ok {
			_, _, ok := windowKind(key, m)
			return ok
		}
	}
	_, _, ok := windowKind(key, "")
	return ok
}
