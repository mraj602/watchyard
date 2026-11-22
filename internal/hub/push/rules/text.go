package rules

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// Field limits of push.md 5.5.
const (
	TitleMax = 80
	BodyMax  = 180
	// SummaryLines is how many alert lines a summary lists before "+N more".
	SummaryLines = 4
)

// Room is the seat with the most room in a window (push.md 5.7).
type Room struct {
	Label string
	Left  float64 // percent left
}

// TextOptions are the per-target inputs of a push text.
type TextOptions struct {
	Private bool
	Loc     *time.Location // times in push text; nil = UTC
	Now     time.Time
	// Repush: this is a snooze re-push ("Still waiting · 19 min · ").
	Repush bool
	// Room for rate-limit failures and limit alerts; nil says nothing.
	Room *Room
}

// Text is a rendered push.
type Text struct {
	Title, Body, URL, Tag string
}

// Cut shortens s to at most n characters, ending with … when cut.
func Cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}

// WindowLabel is a window key for people: five_hour → 5-hour.
func WindowLabel(key string) string {
	switch key {
	case "five_hour":
		return "5-hour"
	case "seven_day":
		return "7-day"
	case "seven_day_opus":
		return "7-day Opus"
	case "seven_day_sonnet":
		return "7-day Sonnet"
	}
	return key
}

// SeatLabel is the seat's label, else the account email's local part, else "Unattributed".
func SeatLabel(label, email string) string {
	if strings.TrimSpace(label) != "" {
		return strings.TrimSpace(label)
	}
	if at := strings.IndexByte(email, '@'); at > 0 {
		return email[:at]
	}
	if email != "" {
		return email
	}
	return "Unattributed"
}

// Clock formats t for push text: HH:MM today, "Thu 17:00" within a week, else "12 Oct 17:00".
func Clock(t, now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	lt, ln := t.In(loc), now.In(loc)
	switch {
	case lt.Year() == ln.Year() && lt.YearDay() == ln.YearDay():
		return lt.Format("15:04")
	case lt.Sub(ln) < 7*24*time.Hour && lt.Sub(ln) > -7*24*time.Hour:
		return lt.Format("Mon 15:04")
	}
	return lt.Format("2 Jan 15:04")
}

func pct(v float64) string { return fmt.Sprintf("%.0f%%", math.Floor(v)) }

// join joins the non-empty parts with " · ".
func join(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, " · ")
}

func firstLine(s string) string {
	s = strings.TrimSpace(strings.TrimLeft(s, "…"))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func seatOf(f model.Facts) string {
	if f.Seat == "" {
		return "Unattributed"
	}
	return f.Seat
}

// what is the short wording of a session alert ("asked a question", "needs permission").
func what(trigger model.Trigger, f model.Facts) string {
	switch trigger {
	case model.TriggerNeedsPermission:
		return "needs permission"
	case model.TriggerAsked:
		switch f.Detail {
		case "tool":
			if f.Tool == "ExitPlanMode" {
				return "plan ready"
			}
			return "asked a question"
		case "elicitation":
			return "is waiting for input"
		}
		return "asked a question"
	case model.TriggerFailed:
		switch f.Detail {
		case "rate_limit":
			return "rate limited"
		case "authentication_failed":
			return "login needed"
		case "billing_error":
			return "billing problem"
		}
		if f.Detail == "" {
			return "stopped"
		}
		return "stopped: " + f.Detail
	}
	return string(trigger)
}

// resetsBody is "Resets 17:00 · work has 59% left".
func resetsBody(f model.Facts, o TextOptions) string {
	var resets, room string
	if f.ResetsAt != 0 {
		resets = "Resets " + Clock(f.ResetsAt.Std(), o.Now, o.Loc)
	}
	if o.Room != nil {
		room = o.Room.Label + " has " + pct(o.Room.Left) + " left"
	}
	return join(resets, room)
}

// URL is the deep link of an alert for a non-private push (push.md 5.8).
func URL(a model.Alert) string {
	switch {
	case IsSessionTrigger(a.Trigger) && a.Session != nil:
		return "/#/sessions/" + *a.Session + "?alert=" + a.ID
	case IsWindowTrigger(a.Trigger) && a.Seat != nil:
		return "/#/accounts?seat=" + *a.Seat
	case a.Trigger == model.TriggerMachineSilent && a.Machine != nil:
		return "/#/machines?machine=" + *a.Machine
	case a.Trigger.IsNotice() && a.Seat != nil:
		return "/#/accounts?seat=" + *a.Seat
	case a.Trigger.IsNotice():
		return "/#/accounts"
	}
	return "/#/alerts/" + a.ID
}

// Tag is the notification tag (push.md 6.5): the same tag replaces the earlier notification.
func Tag(a model.Alert) string {
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	switch {
	case IsSessionTrigger(a.Trigger):
		return "wy-s-" + deref(a.Session)
	case a.Trigger == model.TriggerReset:
		return "wy-r-" + deref(a.Seat) + "-" + deref(a.WindowKey)
	case IsWindowTrigger(a.Trigger):
		return "wy-l-" + deref(a.Seat) + "-" + deref(a.WindowKey)
	case a.Trigger == model.TriggerMachineSilent:
		return "wy-m-" + deref(a.Machine)
	case a.Trigger == model.TriggerLimitChange:
		return "wy-u-" + string(a.Trigger) + "-" + a.Facts.Model
	case a.Trigger.IsNotice():
		return "wy-u-" + string(a.Trigger) + "-" + deref(a.Seat)
	}
	return "wy-" + a.ID
}

// privateText is the neutral wording of a private push (push.md 5.5).
func privateText(t model.Trigger) string {
	switch {
	case IsSessionTrigger(t):
		return "1 session needs you"
	case t == model.TriggerReset:
		return "A limit has reset"
	case IsWindowTrigger(t):
		return "An account is near its limit"
	case t == model.TriggerMachineSilent:
		return "A machine went quiet"
	case t.IsNotice():
		return "Account usage changed"
	}
	return "Something needs you"
}

// PrivateTitle is the title of every private push.
const PrivateTitle = "Watchyard"

// waited is the "19 min" of a re-push: how long the condition has held.
func waited(a model.Alert, now time.Time) string {
	from := a.SinceAt
	if from == 0 || from.Std().After(now) {
		from = a.CreatedAt
	}
	d := now.Sub(from.Std())
	switch {
	case d < time.Minute:
		return "1 min"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return fmt.Sprintf("%d h %d min", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

// Render is the title, body, deep link and tag of one alert's push (push.md 5.5, 5.8).
func Render(a model.Alert, o TextOptions) Text {
	f := a.Facts
	out := Text{URL: URL(a), Tag: Tag(a)}
	if o.Private {
		out.Title, out.Body, out.URL = PrivateTitle, privateText(a.Trigger), "/#/alerts/"+a.ID
		return out
	}
	switch a.Trigger {
	case model.TriggerNeedsPermission, model.TriggerAsked, model.TriggerFailed:
		out.Title = join(seatOf(f), f.Project, what(a.Trigger, f))
		out.Body = sessionBody(a.Trigger, f, o)
	case model.TriggerLimit80, model.TriggerLimit95:
		if f.Pct >= 100 || f.Peak >= 100 {
			out.Title = join(seatOf(f), WindowLabel(f.Window)+" limit reached")
		} else {
			out.Title = join(seatOf(f), WindowLabel(f.Window)+" at "+pct(max(f.Pct, f.Peak)))
		}
		out.Body = resetsBody(f, o)
	case model.TriggerReset:
		out.Title = join(seatOf(f), WindowLabel(f.Window)+" reset")
		out.Body = join("Back to 0%", "peaked at "+pct(f.Peak))
	case model.TriggerMachineSilent:
		name := f.Machine
		if name == "" && a.Machine != nil {
			name = *a.Machine
		}
		out.Title = name + " silent for " + silentFor(a, o.Now)
		live := "1 live session"
		if f.Sessions != 1 {
			live = fmt.Sprintf("%d live sessions", f.Sessions)
		}
		var last string
		if f.LastSeen != 0 {
			last = "last record " + Clock(f.LastSeen.Std(), o.Now, o.Loc)
		}
		out.Body = join(live, last)
	case model.TriggerLimitChange, model.TriggerUsageReset, model.TriggerSeatChange, model.TriggerWeeklyDrop:
		out.Title, out.Body = noticeText(a, o)
	default:
		out.Title = string(a.Trigger)
	}
	if o.Repush {
		out.Title = "Still waiting · " + waited(a, o.Now) + " · " + out.Title
	}
	out.Title, out.Body = Cut(out.Title, TitleMax), Cut(out.Body, BodyMax)
	return out
}

// noticeText is the title and body of a usage notice (push.md 5.5).
func noticeText(a model.Alert, o TextOptions) (string, string) {
	f := a.Facts
	resets := func() string {
		if f.ResetsAt == 0 {
			return ""
		}
		return Clock(f.ResetsAt.Std(), o.Now, o.Loc)
	}
	switch a.Trigger {
	case model.TriggerLimitChange:
		word, name := "lower", f.Model
		if f.Up {
			word = "higher"
		}
		if name == "" {
			name = "Model"
		}
		var body string
		if f.Refs > 0 {
			body = fmt.Sprintf("5-hour capacity %s× last week on %d of %d accounts", strconv.FormatFloat(f.Change, 'f', -1, 64),
				f.Moved, f.Refs)
		}
		return name + " limits look " + word, join(body, "check")
	case model.TriggerUsageReset:
		var still, agree string
		if r := resets(); r != "" {
			still = "7-day still resets " + r
		}
		switch {
		case f.Sessions == 1:
			agree = "1 session agrees"
		case f.Sessions > 1:
			agree = fmt.Sprintf("%d sessions agree", f.Sessions)
		}
		return join(seatOf(f), "usage reset"), join("5-hour and 7-day back to 0%", still, agree)
	case model.TriggerSeatChange:
		what := "changed account"
		if f.OrgOnly {
			what = "changed org"
		}
		where := f.Folder
		if f.Machine != "" {
			where = strings.TrimSpace(where + " on " + f.Machine)
		}
		body := "Was " + f.Was + "; now this account"
		if f.Was == "" {
			body = "Now this account"
		}
		if f.OrgOnly {
			body = "Same account, another org"
			if f.Org != "" {
				body = "Same account, now in " + f.Org
			}
		}
		return join(seatOf(f), strings.TrimSpace(where+" "+what)), body
	case model.TriggerWeeklyDrop:
		reset := "no reset"
		if r := resets(); r != "" {
			reset = "no reset (resets " + r + ")"
		}
		return join(seatOf(f), "7-day dropped "+pct(f.Peak)+" → "+pct(f.Pct)), join("Something changed, check", reset)
	}
	return string(a.Trigger), ""
}

// silentFor is "20 min" under 55 min, else "1 h" or "2 h" (rounded down, at least 1 h), since
// the last record.
func silentFor(a model.Alert, now time.Time) string {
	from := a.Facts.LastSeen
	if from == 0 {
		from = a.SinceAt
	}
	if from == 0 {
		return "1 h"
	}
	d := now.Sub(from.Std())
	// A rule delay under an hour (delay_s may be 0-86400) fires early: say minutes then,
	// not "1 h". From 55 min on it reads in whole hours (at least 1 h) as before.
	if d < 55*time.Minute {
		return fmt.Sprintf("%d min", max(1, int(d/time.Minute)))
	}
	return fmt.Sprintf("%d h", max(1, int(d/time.Hour)))
}

func sessionBody(t model.Trigger, f model.Facts, o TextOptions) string {
	fallback := join("Waiting for your answer", f.Machine)
	switch t {
	case model.TriggerNeedsPermission:
		tool := f.Tool
		if tool != "" && strings.Contains(f.Message, tool) {
			tool = ""
		}
		return join(tool, f.Message, f.Machine)
	case model.TriggerAsked:
		switch f.Detail {
		case "elicitation":
			if f.Message != "" {
				return f.Message
			}
			return fallback
		}
		if f.Question != "" {
			return f.Question
		}
		if l := firstLine(f.Message); l != "" {
			return l
		}
		return fallback
	case model.TriggerFailed:
		if f.Detail == "rate_limit" {
			if b := resetsBody(f, o); b != "" {
				return b
			}
		}
		return f.Machine
	}
	return ""
}

// summaryWord is the short state word of a summary line.
func summaryWord(t model.Trigger, f model.Facts) string {
	switch t {
	case model.TriggerNeedsPermission:
		return "needs permission"
	case model.TriggerAsked:
		return "asked"
	case model.TriggerFailed:
		return "failed"
	}
	return ""
}

// RenderSummary is one push for several alerts (push.md 5.3): "3 sessions need you" with up
// to four "work · api — asked" lines, then "+2 more". A summary of non-session alerts (a
// flood-cap release) is titled "N alerts" and lists each alert's own title.
func RenderSummary(alerts []model.Alert, o TextOptions) Text {
	n := len(alerts)
	allSessions := true
	for _, a := range alerts {
		if !IsSessionTrigger(a.Trigger) {
			allSessions = false
		}
	}
	out := Text{URL: "/#/", Tag: "wy-summary"}
	if allSessions {
		out.Title = fmt.Sprintf("%d sessions need you", n)
	} else {
		out.Title = fmt.Sprintf("%d alerts", n)
	}
	if o.Private {
		if allSessions {
			out.Body = out.Title
		} else {
			out.Body = fmt.Sprintf("%d things need you", n)
		}
		out.Title = PrivateTitle
		return out
	}
	var lines []string
	for i, a := range alerts {
		if i == SummaryLines {
			lines = append(lines, fmt.Sprintf("+%d more", n-SummaryLines))
			break
		}
		if IsSessionTrigger(a.Trigger) {
			lines = append(lines, join(seatOf(a.Facts), a.Facts.Project)+" — "+summaryWord(a.Trigger, a.Facts))
		} else {
			lines = append(lines, Render(a, TextOptions{Loc: o.Loc, Now: o.Now}).Title)
		}
	}
	out.Body = strings.Join(lines, "\n")
	out.Title = Cut(out.Title, TitleMax)
	if utf8.RuneCountInString(out.Body) > BodyMax {
		out.Body = string([]rune(out.Body)[:BodyMax-1]) + "…"
	}
	return out
}

// RoomCandidate is one seat for WhoHasRoom, in list order: its current window of the key
// (HasWindow: it has a reading) and whether it has a reading for any window (HasReading).
type RoomCandidate struct {
	Seat       string
	Label      string
	Hidden     bool
	HasWindow  bool
	UsedPct    float64
	ResetsMS   int64
	HasReading bool
}

// EffectiveUse is a seat's use of the window for ranking (push.md 5.7, the board's rule): 0
// when the window's reset has passed, or when the seat has no reading for it but has one for
// another window; otherwise its used % (an old reading counts until its reset). ok is false
// for a seat with no reading at all.
func EffectiveUse(c RoomCandidate, nowMS int64) (used float64, ok bool) {
	switch {
	case c.HasWindow && c.ResetsMS > nowMS:
		return c.UsedPct, true
	case c.HasWindow, c.HasReading:
		return 0, true
	}
	return 0, false
}

// WhoHasRoom picks the non-hidden seat other than subject with the lowest effective use of the
// window (EffectiveUse), the first in list order on a tie, and only if it is under 80%
// (push.md 5.7). Seats with no reading at all are skipped.
func WhoHasRoom(subject string, cands []RoomCandidate, nowMS int64) *Room {
	var best *RoomCandidate
	bestUse := 0.0
	for i := range cands {
		c := &cands[i]
		if c.Seat == subject || c.Hidden {
			continue
		}
		use, ok := EffectiveUse(*c, nowMS)
		if !ok || use >= 80 {
			continue
		}
		if best == nil || use < bestUse {
			best, bestUse = c, use
		}
	}
	if best == nil {
		return nil
	}
	return &Room{Label: best.Label, Left: 100 - bestUse}
}

// Delivery parameters of push.md 6.4 and 7.2.

// TTL is how long a push service may hold the push.
func TTL(t model.Trigger, summary bool, resetsMS, nowMS int64) time.Duration {
	switch {
	case summary || IsSessionTrigger(t):
		return 6 * time.Hour
	case t == model.TriggerLimit95:
		d := time.Duration(resetsMS-nowMS) * time.Millisecond
		return min(max(d, time.Minute), 24*time.Hour)
	case t == model.TriggerReset:
		return time.Hour
	case t.IsNotice():
		return 24 * time.Hour
	}
	return 6 * time.Hour
}

// Urgency is the Web Push Urgency header.
func Urgency(t model.Trigger, summary bool) string {
	switch {
	case summary || IsSessionTrigger(t) || t == model.TriggerLimit95:
		return "high"
	case t == model.TriggerReset || t.IsNotice():
		return "low"
	}
	return "normal"
}

// Priority is the ntfy priority.
func Priority(t model.Trigger, summary bool) int {
	switch {
	case summary || IsSessionTrigger(t) || t == model.TriggerLimit95:
		return 4
	case t == model.TriggerReset || t.IsNotice():
		return 2
	}
	return 3
}
