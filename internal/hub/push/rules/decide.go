package rules

import (
	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// SessionNow is a session as reconcile sees it (push.md 3.5).
type SessionNow struct {
	ID       string
	Found    bool // false: no such session (treated as ended)
	State    string
	NeedsYou bool
	SinceMS  int64 // session_state.since_at (client ts)
	Seat     string
	Project  string
	Machine  string
	Remote   string      // session.remote_url
	Detail   string      // session_state.detail
	Peers    string      // session_state.peers: "", "message" or "wait" (states.md 3.1)
	Facts    model.Facts // text inputs captured into alert.facts
}

// WindowNow is one current usage window of a seat.
type WindowNow struct {
	Key         string
	ResetsMS    int64
	UpdatedMS   int64 // ts of the newest sample
	Peak, Final float64
	HasPeak     bool
}

// SeatNow is a seat as reconcile sees it: its current windows.
type SeatNow struct {
	ID      string
	Windows []WindowNow
	Facts   model.Facts // Seat label filled
}

// MachineNow is a machine as the silence sweep sees it.
type MachineNow struct {
	ID         string
	LastSeenMS int64 // receive time of its newest record (0: never)
	Live       int   // sessions in working / needs_permission / asked that are not stale
	Facts      model.Facts
}

// Current is the subject of one reconcile: exactly one field is set.
type Current struct {
	Session *SessionNow
	Seat    *SeatNow
	Machine *MachineNow
	Notice  *model.Notice
}

// Usage notice timing (push.md 3.9).
const (
	NoticeMaxAgeMS = 6 * 3600_000  // an older since_at is history: skipped stale
	NoticeLifeMS   = 24 * 3600_000 // closes this long after it fired
)

// Env is what every decision needs besides the subject.
type Env struct {
	Rules    []model.Rule
	Settings model.Settings
	NowMS    int64
	AwakeMS  int64 // awake_at (push.md 3.3)
	Baseline bool  // first start: record what holds as skipped/baseline (3.8)
}

// OpKind is what an Op does.
type OpKind int

// The operations.
const (
	// OpCreate inserts Alert (ID empty: the engine assigns one; the insert is a no-op if
	// the episode exists).
	OpCreate OpKind = iota + 1
	// OpEnd moves Alert to Status with Reason (cancelled before a push, closed after).
	OpEnd
)

// Op is one change reconcile asks for.
type Op struct {
	Kind   OpKind
	Alert  model.Alert
	Status model.Status // OpEnd
	Reason string       // OpEnd
}

// Decide is the reconcile decision for one subject (push.md 3.5): given what holds now and
// the subject's alerts (every open one, plus any alert whose episode reconcile may want),
// it returns the alerts to create and the alerts to end. It is idempotent: applying its
// ops and calling it again returns nothing.
func Decide(cur Current, alerts []model.Alert, env Env) []Op {
	switch {
	case cur.Session != nil:
		return decideSession(*cur.Session, alerts, env)
	case cur.Seat != nil:
		return decideSeat(*cur.Seat, alerts, env)
	case cur.Machine != nil:
		return decideMachine(*cur.Machine, alerts, env)
	case cur.Notice != nil:
		return decideNotice(*cur.Notice, alerts, env)
	}
	return nil
}

// decideNotice creates a usage notice's alert once (push.md 3.9): pending and due now, or
// skipped when its rule is off or it is history.
func decideNotice(n model.Notice, alerts []model.Alert, env Env) []Op {
	if !n.Trigger.IsNotice() || n.Episode == "" || hasEpisode(alerts, n.Episode) {
		return nil
	}
	rule, ok := Match(env.Rules, n.Trigger, Subject{Seat: n.Seat, Machine: n.Machine})
	if !ok {
		return nil
	}
	a := model.Alert{
		AlertBrief: model.AlertBrief{Trigger: n.Trigger},
		Rule:       strp(rule.ID), SinceAt: model.Time(n.SinceMS), CreatedAt: model.Time(env.NowMS),
		Episode: n.Episode, Facts: n.Facts,
	}
	if n.Seat != "" {
		a.Seat = strp(n.Seat)
	}
	if n.Machine != "" {
		a.Machine = strp(n.Machine)
	}
	switch {
	case n.Direct:
		a.Status, a.Reason, a.EndedAt = model.StatusSkipped, strp(model.ReasonDirect), model.Time(env.NowMS)
	case !rule.Enabled:
		a.Status, a.Reason, a.EndedAt = model.StatusSkipped, strp(model.ReasonRuleOff), model.Time(env.NowMS)
	case n.SinceMS < env.NowMS-NoticeMaxAgeMS:
		a.Status, a.Reason, a.EndedAt = model.StatusSkipped, strp(model.ReasonStale), model.Time(env.NowMS)
	default:
		a.Status, a.DueAt = model.StatusPending, model.Time(env.NowMS)
	}
	return []Op{{Kind: OpCreate, Alert: a}}
}

// NoticeExpired reports whether a usage notice that pushed is past its life (push.md 3.9).
func NoticeExpired(a model.Alert, nowMS int64) bool {
	return a.Trigger.IsNotice() && a.FiredAt != 0 && int64(a.FiredAt)+NoticeLifeMS <= nowMS &&
		(a.Status == model.StatusFired || a.Status == model.StatusSnoozed || a.Status == model.StatusSeen)
}

// OpenStatus reports whether reconcile may still end an alert in status s.
func OpenStatus(s model.Status) bool { return s.Open() || s == model.StatusSeen }

// EndStatus is the status an open alert ends in: cancelled before any push, else closed.
func EndStatus(s model.Status) model.Status {
	if s == model.StatusPending {
		return model.StatusCancelled
	}
	return model.StatusClosed
}

func endOp(a model.Alert, reason string) Op {
	return Op{Kind: OpEnd, Alert: a, Status: EndStatus(a.Status), Reason: reason}
}

func hasEpisode(alerts []model.Alert, ep string) bool {
	for _, a := range alerts {
		if a.Episode == ep {
			return true
		}
	}
	return false
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// EndReason is why a session alert ends when the session is now in state (push.md 3.5).
func EndReason(state string, found bool) string {
	switch {
	case !found || state == "ended" || state == "gone":
		return model.ReasonEnded
	case state == "working" || state == "done":
		return model.ReasonAnswered
	}
	return model.ReasonSuperseded
}

// SessionWant is the episode the session should have an alert for, "" if none.
func SessionWant(cur SessionNow) (model.Trigger, string) {
	if !cur.Found {
		return "", ""
	}
	t, ok := SessionTrigger(cur.State, cur.NeedsYou)
	if !ok {
		return "", ""
	}
	return t, SessionEpisode(cur.ID, cur.State, cur.SinceMS)
}

func decideSession(cur SessionNow, alerts []model.Alert, env Env) []Op {
	trig, want := SessionWant(cur)
	var ops []Op
	for _, a := range alerts {
		if OpenStatus(a.Status) && a.Episode != want {
			ops = append(ops, endOp(a, EndReason(cur.State, cur.Found)))
		}
	}
	if want == "" || hasEpisode(alerts, want) {
		return ops
	}
	rule, ok := Match(env.Rules, trig, Subject{Seat: cur.Seat, Project: cur.Project, Machine: cur.Machine})
	a := model.Alert{
		AlertBrief: model.AlertBrief{Trigger: trig},
		Session:    strp(cur.ID), Seat: strp(cur.Seat), Machine: strp(cur.Machine),
		SinceAt: model.Time(cur.SinceMS), CreatedAt: model.Time(env.NowMS),
		Episode: want, Facts: cur.Facts,
	}
	delay := int64(0)
	if ok {
		a.Rule = strp(rule.ID)
		delay = int64(rule.DelayS) * 1000
	}
	reason := ""
	switch {
	case env.Baseline:
		reason = model.ReasonBaseline
	case !ok || !rule.Enabled:
		reason = model.ReasonRuleOff
	case env.Settings.SkipRemote && cur.Remote != "" && (trig == model.TriggerNeedsPermission || trig == model.TriggerAsked):
		reason = model.ReasonRemoteControl
	case env.Settings.SkipPeers && PeersOnly(trig, cur.Detail, cur.Peers):
		reason = model.ReasonPeers
	case Stale(cur.SinceMS, delay, int64(env.Settings.MaxAgeS)*1000, env.NowMS):
		reason = model.ReasonStale
	}
	if reason != "" {
		a.Status, a.Reason, a.EndedAt = model.StatusSkipped, strp(reason), model.Time(env.NowMS)
	} else {
		a.Status, a.DueAt = model.StatusPending, model.Time(Due(cur.SinceMS, delay, env.NowMS))
	}
	return append(ops, Op{Kind: OpCreate, Alert: a})
}

// PeersOnly reports whether a session alert is only about sessions coordinating with each
// other (push.md 3.5, peers.md 6.4): an asked:question (a plain-text question at the end of a
// turn) in a turn that a peer message started or that ended waiting on another session's
// reply. A permission prompt, AskUserQuestion, ExitPlanMode, an elicitation dialog and a
// failure are always for a person and are never peers-only.
func PeersOnly(trig model.Trigger, detail, peers string) bool {
	return trig == model.TriggerAsked && detail == "question" && peers != ""
}

// SeatEpisodes lists every episode reconcile may want for the seat's current windows, so
// the engine can load those alerts whatever their status.
func SeatEpisodes(cur SeatNow) []string {
	var out []string
	for _, w := range cur.Windows {
		out = append(out, LimitEpisode(cur.ID, w.Key, w.ResetsMS, 80), LimitEpisode(cur.ID, w.Key, w.ResetsMS, 95),
			ResetEpisode(cur.ID, w.Key, w.ResetsMS))
	}
	return out
}

func decideSeat(cur SeatNow, alerts []model.Alert, env Env) []Op {
	var ops []Op
	// Close what has passed: a limit alert never stays open past its window (3.5); a fired
	// reset alert stays open for ResetOpenMS after the reset, then closes.
	for _, a := range alerts {
		if !OpenStatus(a.Status) {
			continue
		}
		resets := int64(a.Facts.ResetsAt)
		switch a.Trigger {
		case model.TriggerLimit80, model.TriggerLimit95:
			if resets <= env.NowMS {
				ops = append(ops, endOp(a, model.ReasonResetPassed))
			}
		case model.TriggerReset:
			if a.Status != model.StatusPending && resets+ResetOpenMS <= env.NowMS {
				ops = append(ops, endOp(a, model.ReasonResetPassed))
			}
		}
	}
	for _, w := range cur.Windows {
		if w.ResetsMS <= env.NowMS || !w.HasPeak {
			continue
		}
		subj := Subject{Seat: cur.ID, Window: w.Key}
		fresh := w.UpdatedMS >= env.NowMS-SampleFreshMS
		facts := cur.Facts
		facts.Window, facts.Pct, facts.Peak, facts.ResetsAt = w.Key, w.Final, w.Peak, model.Time(w.ResetsMS)
		mk := func(t model.Trigger, ep string, rule model.Rule) model.Alert {
			return model.Alert{
				AlertBrief: model.AlertBrief{Trigger: t},
				Rule:       strp(rule.ID), Seat: strp(cur.ID), WindowKey: strp(w.Key),
				SinceAt: model.Time(w.UpdatedMS), CreatedAt: model.Time(env.NowMS), Episode: ep, Facts: facts,
			}
		}
		// limit_95 first: whether it will push decides if a new limit_80 is superseded.
		push95 := false
		var created []Op
		for _, th := range []int{95, 80} {
			t := model.TriggerLimit80
			if th == 95 {
				t = model.TriggerLimit95
			}
			ep := LimitEpisode(cur.ID, w.Key, w.ResetsMS, th)
			rule, ok := Match(env.Rules, t, subj)
			if !ok || !fresh || w.Peak < float64(th) || hasEpisode(alerts, ep) {
				continue
			}
			a := mk(t, ep, rule)
			reason := ""
			switch {
			case env.Baseline:
				reason = model.ReasonBaseline
			case !rule.Enabled:
				reason = model.ReasonRuleOff
			case th == 80 && push95:
				reason = model.ReasonSuperseded
			}
			if reason != "" {
				a.Status, a.Reason, a.EndedAt = model.StatusSkipped, strp(reason), model.Time(env.NowMS)
			} else {
				a.Status, a.DueAt = model.StatusPending, model.Time(env.NowMS)
				if th == 95 {
					push95 = true
				}
			}
			created = append(created, Op{Kind: OpCreate, Alert: a})
		}
		// Keep 80 before 95 in the output (creation order is the log order).
		for i := len(created) - 1; i >= 0; i-- {
			ops = append(ops, created[i])
		}
		if rule, ok := Match(env.Rules, model.TriggerReset, subj); ok && rule.Enabled && w.Peak >= 80 {
			ep := ResetEpisode(cur.ID, w.Key, w.ResetsMS)
			if !hasEpisode(alerts, ep) {
				a := mk(model.TriggerReset, ep, rule)
				a.Status, a.DueAt = model.StatusPending, model.Time(w.ResetsMS)
				ops = append(ops, Op{Kind: OpCreate, Alert: a})
			}
		}
	}
	return ops
}

// MachineEpisode is the episode the machine's silence would have now.
func MachineEpisode(cur MachineNow) string { return SilentEpisode(cur.ID, cur.LastSeenMS) }

func decideMachine(cur MachineNow, alerts []model.Alert, env Env) []Op {
	want := MachineEpisode(cur)
	var ops []Op
	for _, a := range alerts {
		if a.Trigger == model.TriggerMachineSilent && OpenStatus(a.Status) && a.Episode != want {
			ops = append(ops, endOp(a, model.ReasonRecovered))
		}
	}
	if cur.Live == 0 || hasEpisode(alerts, want) {
		return ops
	}
	rule, ok := Match(env.Rules, model.TriggerMachineSilent, Subject{Machine: cur.ID})
	if !ok {
		return ops
	}
	from := max(cur.LastSeenMS, env.AwakeMS)
	if from+int64(rule.DelayS)*1000 > env.NowMS {
		return ops
	}
	facts := cur.Facts
	facts.Sessions, facts.LastSeen = cur.Live, model.Time(cur.LastSeenMS)
	a := model.Alert{
		AlertBrief: model.AlertBrief{Trigger: model.TriggerMachineSilent},
		Rule:       strp(rule.ID), Machine: strp(cur.ID), SinceAt: model.Time(from),
		CreatedAt: model.Time(env.NowMS), Episode: want, Facts: facts,
	}
	switch {
	case env.Baseline:
		a.Status, a.Reason, a.EndedAt = model.StatusSkipped, strp(model.ReasonBaseline), model.Time(env.NowMS)
	case !rule.Enabled:
		a.Status, a.Reason, a.EndedAt = model.StatusSkipped, strp(model.ReasonRuleOff), model.Time(env.NowMS)
	default:
		a.Status, a.DueAt = model.StatusPending, model.Time(env.NowMS)
	}
	return append(ops, Op{Kind: OpCreate, Alert: a})
}
