// Package rules is the pure part of the alert engine (push.md 3 and 5): which trigger a
// subject is in, episode keys, due and stale arithmetic, rule matching with specificity,
// the reconcile decision (Decide), quiet-hour windows, push text and deep links, and "who
// has room". Nothing here reads a clock, a database or the network: the engine passes the
// current facts in and applies the returned operations.
package rules

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// Window keys a limit or reset rule watches when its scope names none (push.md 3.4).
var DefaultWindows = []string{"five_hour", "seven_day"}

// Timing constants of push.md 3.1 and 3.3.
const (
	// SampleFreshMS: a limit alert needs a sample at most this old (3.1).
	SampleFreshMS = 30 * 60 * 1000
	// ResetOpenMS: a fired reset alert stays open this long after its resets_at, so it can
	// still be marked seen, then it is closed (reset_passed).
	ResetOpenMS = 60 * 60 * 1000
)

// SessionTrigger maps a session state to its trigger (push.md 3.1). failed counts only
// when it needs you (not overloaded / server_error).
func SessionTrigger(state string, needsYou bool) (model.Trigger, bool) {
	switch state {
	case "needs_permission":
		return model.TriggerNeedsPermission, true
	case "asked":
		return model.TriggerAsked, true
	case "failed":
		if needsYou {
			return model.TriggerFailed, true
		}
	}
	return "", false
}

// IsSessionTrigger reports whether t is about a session.
func IsSessionTrigger(t model.Trigger) bool {
	return t == model.TriggerNeedsPermission || t == model.TriggerAsked || t == model.TriggerFailed
}

// IsWindowTrigger reports whether t is about a seat's usage window.
func IsWindowTrigger(t model.Trigger) bool {
	return t == model.TriggerLimit80 || t == model.TriggerLimit95 || t == model.TriggerReset
}

// SessionEpisode is s:<session id>:<state>:<since_at ms> (push.md 3.2).
func SessionEpisode(id, state string, sinceMS int64) string {
	return "s:" + id + ":" + state + ":" + strconv.FormatInt(sinceMS, 10)
}

// LimitEpisode is l:<seat>:<window>:<resets_at ms>:80 or :95.
func LimitEpisode(seat, window string, resetsMS int64, threshold int) string {
	return fmt.Sprintf("l:%s:%s:%d:%d", seat, window, resetsMS, threshold)
}

// ResetEpisode is r:<seat>:<window>:<resets_at ms>.
func ResetEpisode(seat, window string, resetsMS int64) string {
	return fmt.Sprintf("r:%s:%s:%d", seat, window, resetsMS)
}

// SilentEpisode is m:<machine>:<last_seen ms>.
func SilentEpisode(machine string, lastSeenMS int64) string {
	return "m:" + machine + ":" + strconv.FormatInt(lastSeenMS, 10)
}

// LimitChangeEpisode is x:<model label>:<up|down>:<hub day YYYY-MM-DD> (push.md 3.2).
func LimitChangeEpisode(model string, up bool, day string) string {
	return LimitChangePrefix(model, up) + day
}

// LimitChangePrefix is the episode up to the day: the limit_change dedupe looks for it.
func LimitChangePrefix(model string, up bool) string {
	dir := "down"
	if up {
		dir = "up"
	}
	return "x:" + model + ":" + dir + ":"
}

// UsageResetEpisode is z:<seat>:<ms of the first low reading>.
func UsageResetEpisode(seat string, tMS int64) string {
	return "z:" + seat + ":" + strconv.FormatInt(tMS, 10)
}

// SeatChangeEpisode is c:<slot id>:<since_at ms of the new login>.
func SeatChangeEpisode(slot, sinceMS int64) string {
	return fmt.Sprintf("c:%d:%d", slot, sinceMS)
}

// WeeklyDropEpisode is w:<seat>:<weekly resets_at ms>:<ms of the first low reading>.
func WeeklyDropEpisode(seat string, resetsMS, tMS int64) string {
	return fmt.Sprintf("w:%s:%d:%d", seat, resetsMS, tMS)
}

// Due is clamp(since + delay, now, now + delay) (push.md 3.3), all in ms.
func Due(sinceMS, delayMS, nowMS int64) int64 {
	return min(max(sinceMS+delayMS, nowMS), nowMS+delayMS)
}

// Stale reports whether an alert created now would first push more than maxAge after the
// moment it was due (push.md 3.3).
func Stale(sinceMS, delayMS, maxAgeMS, nowMS int64) bool {
	return nowMS > sinceMS+delayMS+maxAgeMS
}

// Overdue reports whether the scheduler found something more than maxAge past its time.
func Overdue(dueMS, maxAgeMS, nowMS int64) bool { return nowMS > dueMS+maxAgeMS }

// Subject is what a rule scope is compared with. Empty fields match only empty scope lists.
type Subject struct {
	Seat, Project, Machine, Window string
}

// windowsOf is a rule's effective window list for window triggers.
func windowsOf(r model.Rule) []string {
	if len(r.Scope.Windows) == 0 {
		return DefaultWindows
	}
	return r.Scope.Windows
}

func listMatches(list []string, v string) bool { return len(list) == 0 || slices.Contains(list, v) }

// Matches reports whether rule r applies to trigger t for subject s.
func Matches(r model.Rule, t model.Trigger, s Subject) bool {
	if r.Trigger != t {
		return false
	}
	if !listMatches(r.Scope.Seats, s.Seat) || !listMatches(r.Scope.Projects, s.Project) || !listMatches(r.Scope.Machines, s.Machine) {
		return false
	}
	if IsWindowTrigger(t) && !slices.Contains(windowsOf(r), s.Window) {
		return false
	}
	return true
}

// specificity counts the non-empty scope lists (windows only for window triggers).
func specificity(r model.Rule) int {
	n := 0
	for _, l := range [][]string{r.Scope.Seats, r.Scope.Projects, r.Scope.Machines} {
		if len(l) > 0 {
			n++
		}
	}
	if IsWindowTrigger(r.Trigger) && len(r.Scope.Windows) > 0 {
		n++
	}
	return n
}

// better reports whether a beats b (push.md 3.4): more non-empty lists, then an override
// over the base rule, then the newest updated_at, then the lowest id.
func better(a, b model.Rule) bool {
	if sa, sb := specificity(a), specificity(b); sa != sb {
		return sa > sb
	}
	if a.Base != b.Base {
		return !a.Base
	}
	if a.UpdatedAt != b.UpdatedAt {
		return a.UpdatedAt > b.UpdatedAt
	}
	return a.ID < b.ID
}

// Match picks the rule in force for trigger t and subject s. ok is false when no rule
// matches (a window no rule watches, or a deleted base rule).
func Match(rules []model.Rule, t model.Trigger, s Subject) (model.Rule, bool) {
	var best model.Rule
	found := false
	for _, r := range rules {
		if !Matches(r, t, s) {
			continue
		}
		if !found || better(r, best) {
			best, found = r, true
		}
	}
	return best, found
}

// SubjectOf is the rule subject of an existing alert (for the re-check at fire time).
func SubjectOf(a model.Alert, project string) Subject {
	s := Subject{Project: project}
	if a.Seat != nil {
		s.Seat = *a.Seat
	}
	if a.Machine != nil {
		s.Machine = *a.Machine
	}
	if a.WindowKey != nil {
		s.Window = *a.WindowKey
	}
	return s
}
