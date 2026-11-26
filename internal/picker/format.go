package picker

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mraj602/watchyard/internal/calib"
	"github.com/mraj602/watchyard/internal/config"
)

// EnvArgs is what `--env` prints for a pick: `CLAUDE_CONFIG_DIR=<dir>`, or for the default
// view of ~/.claude `-u CLAUDE_CONFIG_DIR` (that view exists only with the variable unset:
// set to ~/.claude, Claude Code reads another state file and maybe another login), so
// `env $(wy account next --env) claude` starts the right login either way.
func EnvArgs(r Ranked) string {
	if r.Default {
		return "-u CLAUDE_CONFIG_DIR"
	}
	return "CLAUDE_CONFIG_DIR=" + r.Dir
}

// Line is the default output: "claude-3  ~/.claude-3  (7d 23% → ~41% at reset Wed; …)".
func Line(r Ranked) string {
	return fmt.Sprintf("%s  %s  (%s)", r.Label, r.Folder, r.Reason)
}

// NoneLine says that no account can be used now (or how many of the sessions asked for
// could not be placed) and when the earliest one frees up.
func NoneLine(res Result) string {
	s := "No account can take a new session now"
	if len(res.Picks) > 0 && res.Waiting > 0 {
		s = fmt.Sprintf("%d of %d sessions placed; no account can take the other %d now", len(res.Picks), len(res.Picks)+res.Waiting, res.Waiting)
	}
	if res.NextFree != nil {
		s += "; the earliest frees up " + when(*res.NextFree, res.Now) + " (" + res.NextFree.Local().Format("2006-01-02 15:04 MST") + ")"
	} else {
		s += "; none frees up at a known time (log in, or change next.accounts settings)"
	}
	return s + ". `wy account next --explain` shows why."
}

// Doc is the `--json` document.
type Doc struct {
	V        int        `json:"v"`
	Time     time.Time  `json:"time"`
	Strategy string     `json:"strategy"`
	Model    string     `json:"model,omitempty"`
	Data     string     `json:"data"` // hub | local
	HubError string     `json:"hub_error,omitempty"`
	Reserved bool       `json:"reserved"`
	Picked   *Picked    `json:"picked"`
	Picks    []Picked   `json:"picks"`
	Waiting  int        `json:"waiting"`
	NextFree *time.Time `json:"next_free"`
	// SkippedLedgerLines counts the ledger lines that could not be read.
	SkippedLedgerLines int      `json:"skipped_ledger_lines"`
	Accounts           []Ranked `json:"accounts"`
	Warnings           []string `json:"warnings,omitempty"`
}

// Picked names a pick in Doc.
type Picked struct {
	Label     string `json:"label"`
	Folder    string `json:"folder"`
	Dir       string `json:"dir"`
	Env       string `json:"env"`
	Reason    string `json:"reason"`
	SessionID string `json:"session_id,omitempty"`
	Urgent    bool   `json:"urgent"`
	// Drain picks (lanes, setup.md 8.5 Drain): finish_by and weekly_reset are null otherwise.
	Drain       bool       `json:"drain"`
	FinishBy    *time.Time `json:"finish_by"`
	WeeklyReset *time.Time `json:"weekly_reset"`
	Spill       bool       `json:"spill"`
	Why         string     `json:"why,omitempty"`
	Hint        string     `json:"hint,omitempty"`
	// Expect is the lane size class used (short, medium, long, measured); Over: placed past a
	// soft limit (burst cap, protected gate, drain capacity) because no account was free under them.
	Expect string `json:"expect,omitempty"`
	Over   bool   `json:"over"`
}

// pickedOf is a pick in Doc.
func pickedOf(p Pick) Picked {
	x := Picked{Label: p.Label, Folder: p.Folder, Dir: p.Dir, Env: EnvArgs(p.Ranked), Reason: p.Reason,
		SessionID: p.SessionID, Urgent: p.Urgent, Drain: p.Drain, Spill: p.Spill, Expect: p.Expect, Over: p.Over}
	if p.Drain {
		fb, rs := p.FinishBy.UTC(), p.Reset.UTC()
		x.FinishBy, x.WeeklyReset, x.Hint = &fb, &rs, p.Hint()
		if v := p.Lanes; v != nil && v.Drain != nil {
			x.Why = fmt.Sprintf("drain: %.1f%% of the week resets in %.1f h and would expire unused", v.Drain.RoomPct, v.HoursToReset)
		}
	}
	return x
}

// NewDoc builds the `--json` document of a result; skipped is the ledger's unreadable lines.
func NewDoc(res Result, data, hubErr string, reserved bool, skipped int) Doc {
	d := Doc{V: 1, Time: res.Now.UTC(), Strategy: res.Strategy, Model: res.Model, Data: data, HubError: hubErr,
		Reserved: reserved && len(res.Picks) > 0, NextFree: res.NextFree, Accounts: res.Ranked, Warnings: res.Warnings,
		Picks: []Picked{}, Waiting: res.Waiting, SkippedLedgerLines: skipped}
	if d.Accounts == nil {
		d.Accounts = []Ranked{}
	}
	for _, p := range res.Picks {
		d.Picks = append(d.Picks, pickedOf(p))
	}
	if len(d.Picks) > 0 {
		first := d.Picks[0]
		d.Picked = &first
	}
	return d
}

// WriteJSON writes the document, indented.
func WriteJSON(w io.Writer, d Doc) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// Explain writes the ranking as a table.
func Explain(w io.Writer, res Result, data, hubErr string) error {
	head := fmt.Sprintf("Strategy %s", res.Strategy)
	if res.Model != "" {
		head += ", model " + res.Model
	}
	switch data {
	case "local":
		head += "; local data from this machine's status line ("
		if hubErr != "" {
			head += "the hub was not read: " + hubErr
		} else {
			head += "no hub is set up"
		}
		head += "), other machines' use is missing"
	default:
		head += "; data from the hub"
	}
	fmt.Fprintln(w, head+".")
	if res.Strategy == config.NextStrategyLanes {
		return explainLanes(w, res)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tACCOUNT\tFOLDER\tOK\tSCORE\t5H NOW→END/CAP\t7D NOW→RESET/CAP\tOTHER\tDATA\tWHY")
	for i, r := range res.Ranked {
		n := "-"
		ok := "no"
		score := "-"
		if r.Eligible {
			n, ok, score = fmt.Sprint(i+1), "yes", fmt.Sprintf("%.2f", r.Score)
		}
		var five, seven, other []string
		for _, p := range r.Windows {
			switch p.Kind {
			case Kind5h:
				five = append(five, fmt.Sprintf("%.0f→%.0f/%.0f", p.Now+p.Uncertainty, p.AtSessionEnd, p.Cap))
			case Kind7d:
				s := fmt.Sprintf("%.0f→%.0f/%.0f", p.Now+p.Uncertainty, p.AtReset, p.Cap)
				if !p.ResetsAt.IsZero() {
					s += " " + when(p.ResetsAt, res.Now)
				}
				seven = append(seven, s)
			default:
				other = append(other, fmt.Sprintf("%s %.0f→%.0f/%.0f", shortKey(p.Key), p.Now+p.Uncertainty, p.AtSessionEnd, p.Cap))
			}
		}
		dataCol := r.Source
		if r.Freshest != nil {
			dataCol += " " + age(res.Now.Sub(*r.Freshest))
		}
		if r.Stale {
			dataCol += " stale"
		}
		why := r.Reason
		if r.Eligible {
			why = r.Mode
			if r.Pending > 0 {
				why += fmt.Sprintf(", %d reserved", r.Pending)
			}
			if r.Live > 0 {
				why += fmt.Sprintf(", %d live", r.Live)
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", n, r.Label, r.Folder, ok, score, dash(five), dash(seven),
			dash(other), dataCol, why)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if res.Pick != nil {
		fmt.Fprintln(w, "Pick: "+Line(*res.Pick))
	} else {
		fmt.Fprintln(w, NoneLine(res))
	}
	for _, x := range res.Warnings {
		fmt.Fprintln(w, "note: "+x)
	}
	return nil
}

// explainLanes is the lanes table: kind, weekly level (reserve level for protected) and
// reset, 5-hour level, leases and burst, data, why.
func explainLanes(w io.Writer, res Result) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tACCOUNT\tFOLDER\tKIND\tOK\t7D (RESERVE) RESET\t5H\tLEASES BURST\tDATA\tWHY")
	rows := map[string]bool{} // each folder once (live on pre4.1: blocked accounts showed twice)
	for i, r := range res.Ranked {
		if id := r.Label + "\x00" + r.Folder; rows[id] {
			continue
		} else {
			rows[id] = true
		}
		n, ok := "-", "no"
		if r.Eligible {
			n, ok = fmt.Sprint(i+1), "yes"
		}
		seven, five, ls := "-", "-", "-"
		if v := r.Lanes; v != nil {
			seven = fmt.Sprintf("%.0f", v.WeeklyPct)
			if len(v.Unknown) > 0 {
				seven = "?"
			}
			if r.Kind == "protected" {
				seven += fmt.Sprintf(" (%.0f)", v.WeeklyReservePct)
			}
			if v.HoursToReset > 0 {
				seven += " " + when(res.Now.Add(hoursDur(v.HoursToReset)), res.Now)
			}
			five = fmt.Sprintf("%.0f", v.FiveHourPct)
			ls = fmt.Sprintf("%d %d/%d", v.Leases, v.Burst, v.BurstCap)
		}
		dataCol := r.Source
		if r.Freshest != nil {
			dataCol += " " + age(res.Now.Sub(*r.Freshest))
		}
		if r.Stale {
			dataCol += " stale"
		}
		why := r.Reason
		if r.Eligible {
			why = "eligible"
			switch {
			case r.Lanes != nil && r.Lanes.Class == ClassDrain && r.Lanes.Drain != nil && r.Lanes.Drain.Owner && r.Lanes.Drain.FinishBy != nil:
				why += ", " + OwnerDrainText(*r.Lanes.Drain.FinishBy, res.Now)
			case r.Lanes != nil && r.Lanes.Class == ClassDrain:
				why += ", drain"
			case r.Lanes != nil && r.Lanes.Class == ClassAtRisk:
				why += ", at risk"
			case r.Lanes != nil && r.Lanes.Expiring && r.Lanes.Class == "":
				why += ", expiring first"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", n, r.Label, r.Folder, KindText(r), ok, seven, five, ls, dataCol, why)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, l := range kindLines(res.Ranked) {
		fmt.Fprintln(w, l)
	}
	pinned := map[string]bool{}
	for _, r := range res.Ranked {
		if r.Lanes == nil || r.Lanes.Pins == 0 || pinned[r.key] {
			continue
		}
		pinned[r.key] = true
		fmt.Fprintln(w, pinLine(r.Label, *r.Lanes, res.Now))
	}
	expiring := map[string]bool{}
	for _, r := range res.Ranked {
		if r.Lanes == nil || r.Lanes.ExpiringRoom <= 0 || expiring[r.key] {
			continue
		}
		expiring[r.key] = true
		fmt.Fprintf(w, "Expiring room %s: protected, its week resets in %.1f h: it may be drained to %s%% of its week until then (its 5-hour cap stays %s%%)\n",
			r.Label, r.Lanes.HoursToReset, f0(r.Lanes.ExpiringRoom), f0(r.Lanes.FiveHourCap))
	}
	drained := map[string]bool{}
	for _, r := range res.Ranked {
		if r.Lanes == nil || r.Lanes.Drain == nil || !r.Lanes.Drain.On || drained[r.key] {
			continue
		}
		drained[r.key] = true
		fmt.Fprintln(w, drainLine(r.Label, *r.Lanes.Drain, res.Now))
	}
	seen := map[string]bool{}
	for _, r := range res.Ranked {
		if r.Lanes == nil || r.Lanes.Calibration == nil || seen[r.key] {
			continue
		}
		seen[r.key] = true
		fmt.Fprintln(w, calibLine(r.Label, *r.Lanes.Calibration))
	}
	shown := map[string]bool{}
	for _, r := range res.Ranked {
		if r.Lanes == nil || shown[r.key] {
			continue
		}
		shown[r.key] = true
		for _, lc := range r.Lanes.LeaseCharges {
			fmt.Fprintln(w, leaseLine(r.Label, lc))
		}
	}
	if res.LaneBurn != "" {
		fmt.Fprintln(w, res.LaneBurn)
	}
	for _, p := range res.Picks {
		fmt.Fprintln(w, "Pick: "+Line(p.Ranked))
	}
	if len(res.Picks) == 0 || res.Waiting > 0 {
		fmt.Fprintln(w, NoneLine(res))
	}
	for _, x := range res.Warnings {
		fmt.Fprintln(w, "note: "+x)
	}
	return nil
}

// pinLine is the --explain line of an account with live pinned sessions: "Pins claude-3: 2
// live pinned sessions keep 30% of the 5-hour window and 6% of the week free (caps 5h 15, 7d 79)".
// When the reserve is scaled to the time left (setup.md 8.6, Pinned sessions) each window says
// until when: "... 20% of the 5-hour window and 2.1% of the week free until Thu 03:30, scaled
// to time left (full 10) (caps now ...)".
func pinLine(label string, v LaneView, now time.Time) string {
	n := "1 live pinned session keeps"
	if v.Pins != 1 {
		n = fmt.Sprintf("%d live pinned sessions keep", v.Pins)
	}
	pct := func(x float64) string {
		if x == math.Trunc(x) {
			return f0(x)
		}
		return strconv.FormatFloat(x, 'f', 1, 64)
	}
	var r5, r7 *time.Time
	if v.FiveHourResetsAt != nil {
		r5 = v.FiveHourResetsAt
	}
	if v.HoursToReset > 0 {
		t := now.Add(hoursDur(v.HoursToReset))
		r7 = &t
	}
	scaled := func(r, full float64, what string, until *time.Time) string {
		if full > r+0.05 && until != nil {
			return "; the " + what + " part until " + when(*until, now) + ", scaled to time left (full " + pct(full) + ")"
		}
		return ""
	}
	return fmt.Sprintf("Pins %s: %s %s%% of the 5-hour window and %s%% of the week free%s%s (caps now 5h %s, 7d %s)", label, n,
		pct(v.PinReserve5hPct), pct(v.PinReserve7dPct),
		scaled(v.PinReserve5hPct, v.PinFull5hPct, "5-hour", r5), scaled(v.PinReserve7dPct, v.PinFull7dPct, "weekly", r7),
		f0(v.FiveHourCap), f0(v.WeeklyCap))
}

// drainLine is the --explain line of a draining account: "Drain claude-2: room 9.3% (71 pts),
// need 3.6 h at full 5h speed, horizon 24 h; capacity 3 (weekly 6, 5h 3); running burn 12 pts
// before reset; projected 96% at reset; unavoidable waste 0%; finish by 09:20".
func drainLine(label string, d DrainView, now time.Time) string {
	s := fmt.Sprintf("Drain %s: room %.1f%% (%.0f pts), need %.1f h at full 5h speed, horizon %.0f h", label, d.RoomPct, d.RoomPts,
		d.NeedHours, d.HorizonHours)
	if d.SlackX > 0 {
		s += fmt.Sprintf(" (entry %g × need)", d.SlackX)
	}
	if d.Owner && d.FinishBy != nil {
		s = fmt.Sprintf("Drain %s, %s: room %.1f%% (%.0f pts), need %.1f h at full 5h speed", label, OwnerDrainText(*d.FinishBy, now), d.RoomPct, d.RoomPts, d.NeedHours)
	}
	if d.Capacity != nil {
		c5 := "not limiting"
		if d.Capacity5h != nil {
			c5 = strconv.Itoa(*d.Capacity5h)
		}
		s += fmt.Sprintf("; capacity %d (weekly %d, 5h %s", *d.Capacity, *d.CapacityWeekly, c5)
		if d.LimitedBy == LimitSpill || d.LimitedBy == LimitStale || d.LimitedBy == LimitLanding {
			s += ", " + d.LimitedBy
		}
		s += ")"
	}
	s += fmt.Sprintf("; %d running, burn %.0f pts before reset", d.Running, d.ForwardBurn)
	if d.ProjectedPct != nil {
		s += fmt.Sprintf("; projected %.0f%% at reset", *d.ProjectedPct)
	}
	if d.UnavoidablePct != nil {
		s += fmt.Sprintf("; unavoidable waste %.1f%%", *d.UnavoidablePct)
	}
	if d.FinishBy != nil {
		s += "; finish by " + d.FinishBy.Local().Format("15:04")
	}
	if d.AimPct != nil && d.AimAtResetPct != nil {
		// "aim 98.3% now, 99% at 03:20" (the ramp; setup.md 8.5, Drain aim)
		s += fmt.Sprintf("; aim %.1f%% now", *d.AimPct)
		if d.ResetsAt != nil && *d.AimAtResetPct > *d.AimPct+0.05 {
			s += fmt.Sprintf(", %s%% at %s", strconv.FormatFloat(*d.AimAtResetPct, 'f', -1, 64), when(*d.ResetsAt, now))
		}
	}
	return s
}

// calibLine is the --explain calibration line of one account:
// "Calibration a (hub): f 1.52/M units (weight 1.00, n 143), ratio 7.2 (fallback, n 3), lane
// charge 8.0 from lane price 4.0 (fallback, n 0); margins 5h +1.2, 7d +0.15; ...".
func calibLine(label string, c CalibView) string {
	factor := func(f calib.Factor, digits int, unit string) string {
		v := strconv.FormatFloat(f.Value, 'f', digits, 64) + unit
		switch {
		case f.Implausible && f.Measured != nil:
			return v + " (fallback; measured " + strconv.FormatFloat(*f.Measured, 'f', digits, 64) + " implausible)"
		case f.Weight <= 0:
			return v + fmt.Sprintf(" (fallback, n %d)", f.N)
		}
		return v + fmt.Sprintf(" (weight %.2f, n %d)", f.Weight, f.N)
	}
	s := fmt.Sprintf("Calibration %s (%s): f %s, ratio %s, lane charge %.1f from lane price %s",
		label, c.Source, factor(c.F, 2, "/M units"), factor(c.Ratio, 1, ""), c.LaneCharge, factor(c.LanePrice, 1, ""))
	if c.ResetReason != nil && c.Since != nil {
		s += fmt.Sprintf("; restarted %s (%s)", c.Since.Format("Jan 2 15:04"), *c.ResetReason)
	}
	s += fmt.Sprintf("; margins 5h +%.1f, 7d +%.2f", c.Margin5hPct, c.Margin7dPct)
	if c.UnexplainedFrom != nil {
		s += "; unexplained rate from " + *c.UnexplainedFrom + " readings"
	}
	if c.UnexplainedPctPerHour != nil {
		s += fmt.Sprintf("; claude.ai-only %.1f%%/h over %.1f h", *c.UnexplainedPctPerHour, c.UnexplainedHours)
	}
	return s
}

// leaseLine is the --explain line of one lease: "Lease claude-3 10:42 (other machine):
// active; burned 1.20M units from hub = 1.7 points; charge 5h 6.3, reserve 6.3".
func leaseLine(label string, lc LeaseChargeView) string {
	who := "this machine"
	if lc.Machine == "other" {
		who = "other machine"
		if lc.Copy {
			who += ", kept copy"
		}
	} else if lc.SessionID != "" {
		who += ", session " + lc.SessionID
	}
	state := lc.State
	switch {
	case lc.State == LeaseSettled:
		state += " (a reading 10 minutes after the pick holds its use; nothing measures its session)"
	case !lc.Launched:
		state += ", not launched yet"
	}
	s := fmt.Sprintf("Lease %s %s (%s): %s; burned ", label, lc.At.Local().Format("Jan 2 15:04"), who, state)
	if lc.BurnedFrom == burnNone {
		s += "none (no source)"
	} else {
		s += fmt.Sprintf("%.2fM units from %s = %.1f points", lc.BurnedUnits/1e6, lc.BurnedFrom, lc.BurnedPct)
	}
	switch {
	case lc.State == LeaseActive:
		s += fmt.Sprintf("; charge 5h %.1f, reserve %.1f", lc.Charge5hPct, lc.ReservePct)
	case lc.WeeklyKeptPct > 0:
		s += fmt.Sprintf("; no 5h charge, weekly kept %.2f", lc.WeeklyKeptPct)
	default:
		s += "; no charge"
	}
	return s
}

func dash(xs []string) string {
	if len(xs) == 0 {
		return "-"
	}
	return strings.Join(xs, ", ")
}

// KindText is the --explain kind of a lanes account with where it came from (setup.md 8.5,
// --explain): "protected (hub)", "protected (set on this machine); hub says aggressive",
// "aggressive (proposed here)" or "not chosen (treated as aggressive)".
func KindText(r Ranked) string {
	kind := r.Kind
	if kind == "" {
		kind = laneKind(r.policy)
	}
	switch r.ModeFrom {
	case ModeFromNone:
		return "not chosen (treated as aggressive)"
	case ModeFromHub:
		if r.cand.HubModeCached {
			return kind + " (hub, cached)"
		}
		return kind + " (hub)"
	case ModeFromProposed:
		return kind + " (proposed here)"
	case ModeFromLocal:
		s := kind + " (set on this machine)"
		if h := r.policy.HubMode; h != "" && h != kind {
			s += "; hub says " + h
		}
		return s
	}
	return kind
}

// kindLines are the lines under the --explain table: one definition per kind shown
// (ModeWords), then, when a logged-in account is not chosen, how to choose (with its email,
// else its label, when it is the only one).
func kindLines(rs []Ranked) []string {
	var prot, aggr, off, none bool
	var unchosen []Ranked
	seen := map[string]bool{}
	for _, r := range rs {
		kind := r.Kind
		if kind == "" {
			kind = laneKind(r.policy)
		}
		switch {
		case r.ModeFrom == ModeFromNone:
			if r.LoggedIn && !seen[r.key] { // a folder with no login is no account to choose for
				seen[r.key] = true
				unchosen = append(unchosen, r)
			}
			none = true
		case kind == config.NextModeProtected:
			prot = true
		case kind == config.NextModeOff:
			off = true
		default:
			aggr = true
		}
	}
	var out []string
	if prot {
		out = append(out, "Protected: "+ModeWords.Protected)
	}
	if aggr {
		out = append(out, "Aggressive: "+ModeWords.Aggressive)
	}
	if off {
		out = append(out, "Off: "+ModeWords.Off)
	}
	if none {
		out = append(out, "Not chosen: "+ModeWords.NotChosen)
	}
	if len(unchosen) > 0 {
		key := "<email>"
		if len(unchosen) == 1 {
			switch {
			case unchosen[0].Email != "":
				key = unchosen[0].Email
			case unchosen[0].Label != "":
				key = unchosen[0].Label
			}
		}
		out = append(out, "Choose on the board (Accounts, the account, How should the picker use this account?) or: wy config set next.accounts."+key+".mode protected")
	}
	return out
}
