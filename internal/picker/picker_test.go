package picker

import (
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/config"
)

// A fixed clock: Wednesday 2026-11-18 12:00 UTC. Text times are written in time.Local, so
// the tests pin it to UTC.
var t0 = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

func defaults() Settings { return SettingsFrom(config.Defaults().Next) }

func near(a, b float64) bool { return math.Abs(a-b) < 0.051 }

// win builds a current window read ago before t0, resetting in resetsIn.
func win(key string, used float64, resetsIn, ago time.Duration) Window {
	return Window{Key: key, UsedPct: used, ResetsAt: t0.Add(resetsIn), UpdatedAt: t0.Add(-ago), Source: "hub"}
}

func cand(label string, ws ...Window) Candidate {
	return Candidate{Folder: "~/." + label, Dir: "/home/u/." + label, Label: label, LoggedIn: true,
		Account: "acct-" + label, Org: "org", Email: label + "@example.test", Source: "hub", Windows: ws}
}

func TestHistoryRate(t *testing.T) {
	linear := func(from time.Duration, perStep float64, n int) []Sample {
		var s []Sample
		for i := 0; i <= n; i++ {
			s = append(s, Sample{At: t0.Add(-from + time.Duration(i)*10*time.Minute), Pct: 10 + perStep*float64(i)})
		}
		return s
	}
	cases := []struct {
		name    string
		samples []Sample
		start   time.Time
		lb      time.Duration
		want    float64
		ok      bool
	}{
		{"steady 12 per hour", linear(2*time.Hour, 2, 12), time.Time{}, 2 * time.Hour, 12, true},
		{"older history ignored by the lookback", append(linear(6*time.Hour, 0, 24), linear(2*time.Hour, 2, 12)...), time.Time{}, 2 * time.Hour, 12, true},
		{"flat since the last reading pulls the rate down", linear(3*time.Hour, 2, 6), time.Time{}, 2 * time.Hour, 0, true},
		{"too little history", []Sample{{At: t0.Add(-5 * time.Minute), Pct: 3}}, time.Time{}, 2 * time.Hour, 0, false},
		{"no readings", nil, time.Time{}, 2 * time.Hour, 0, false},
		{"drops count as flat", []Sample{{At: t0.Add(-time.Hour), Pct: 50}, {At: t0.Add(-30 * time.Minute), Pct: 10}, {At: t0, Pct: 10}},
			time.Time{}, 2 * time.Hour, 0, true},
		{"readings before the window start are not used", []Sample{{At: t0.Add(-90 * time.Minute), Pct: 95},
			{At: t0.Add(-50 * time.Minute), Pct: 0}, {At: t0, Pct: 10}}, t0.Add(-time.Hour), 2 * time.Hour, 12, true},
	}
	for _, c := range cases {
		got, ok := historyRate(c.samples, t0, c.start, c.lb)
		if ok != c.ok {
			t.Errorf("%s: ok %v, want %v", c.name, ok, c.ok)
			continue
		}
		if !c.ok {
			continue
		}
		if math.Abs(got-c.want) > 0.01 {
			t.Errorf("%s: rate %.3f, want %.3f", c.name, got, c.want)
		}
	}
}

func TestProjectionMath(t *testing.T) {
	base := projectInput{now: t0, kind: Kind5h, length: 5 * time.Hour, cap: 90, perSession: 15, session: 2 * time.Hour,
		stale: 30 * time.Minute, rate: 10, rateFrom: RateHistory}
	cases := []struct {
		name                       string
		w                          Window
		mod                        func(*projectInput)
		now, unc, end, reset, left float64
		over, stale                bool
		resets                     bool
	}{
		{name: "fresh reading", w: win("five_hour", 40, 3*time.Hour, 10*time.Minute),
			now: 41.7, unc: 0, end: 76.7, reset: 86.7, left: 3.3, resets: true},
		{name: "two pending reservations push it over", w: win("five_hour", 40, 3*time.Hour, 10*time.Minute),
			mod: func(p *projectInput) { p.pending = 2 }, now: 41.7, end: 106.7, reset: 116.7, over: true, resets: true},
		{name: "stale reading is widened, never zero", w: win("five_hour", 40, 3*time.Hour, 90*time.Minute),
			now: 55, unc: 15, end: 105, reset: 100, over: true, stale: true, resets: true},
		{name: "window that reset since the reading", w: win("five_hour", 80, -time.Hour, 3*time.Hour),
			mod: func(p *projectInput) { p.rate = 0 }, now: 0, unc: 7.5, end: 22.5, reset: 15, left: 75, stale: true},
		{name: "reset soon: only part of the session lands in this window", w: win("five_hour", 70, 30*time.Minute, time.Minute),
			mod: func(p *projectInput) { p.rate = 0 }, now: 70, end: 73.8, reset: 85, left: 5, resets: true},
		{name: "live sessions set a floor under the rate", w: win("five_hour", 10, 4*time.Hour, 0),
			mod: func(p *projectInput) { p.rate, p.live = 1, 2 }, now: 10, end: 55, reset: 85, left: 5, resets: true},
	}
	for _, c := range cases {
		in := base
		if c.mod != nil {
			c.mod(&in)
		}
		p := project(c.w, in)
		if !near(p.Now, c.now) || !near(p.Uncertainty, c.unc) || !near(p.AtSessionEnd, c.end) || !near(p.AtReset, c.reset) ||
			!near(p.Unused, c.left) || p.Over != c.over || p.Stale != c.stale || p.ResetsAt.IsZero() == c.resets {
			t.Errorf("%s: got now %.1f unc %.1f end %.1f reset %.1f unused %.1f over %v stale %v resets %v", c.name,
				p.Now, p.Uncertainty, p.AtSessionEnd, p.AtReset, p.Unused, p.Over, p.Stale, p.ResetsAt)
		}
	}
	p := project(win("five_hour", 10, 4*time.Hour, 0), projectInput{now: t0, kind: Kind5h, length: 5 * time.Hour, cap: 90,
		perSession: 15, session: 2 * time.Hour, stale: 30 * time.Minute, rate: 1, rateFrom: RateHistory, live: 2})
	if p.RateFrom != "live_sessions" || p.Rate != 15 {
		t.Errorf("live floor: rate %.2f from %s", p.Rate, p.RateFrom)
	}
}

func TestCapsAndModes(t *testing.T) {
	ws := func() []Window {
		return []Window{win("five_hour", 50, 3*time.Hour, time.Minute), win("seven_day", 60, 72*time.Hour, time.Minute)}
	}
	cases := []struct {
		name     string
		acct     config.NextAccount
		eligible bool
		cap5, c7 float64
	}{
		{"normal", config.NextAccount{}, true, 90, 90},
		{"conservative caps 5h at 60", config.NextAccount{Mode: "conservative"}, false, 60, 70},
		{"aggressive", config.NextAccount{Mode: "aggressive"}, true, 98, 98},
		{"explicit caps override the mode", config.NextAccount{Mode: "conservative", Cap5h: 80, Cap7d: 75}, true, 80, 75},
		{"reserve_pct comes off every cap", config.NextAccount{ReservePct: 30}, false, 60, 60},
		{"excluded", config.NextAccount{Exclude: true}, false, 90, 90},
	}
	for _, c := range cases {
		s := defaults()
		s.Accounts = map[string]config.NextAccount{"a@example.test": c.acct}
		cd := cand("a", ws()...)
		res := Rank(Input{Now: t0, Settings: s, Candidates: []Candidate{cd}})
		r := res.Ranked[0]
		if r.Eligible != c.eligible {
			t.Errorf("%s: eligible %v (%s)", c.name, r.Eligible, r.Excluded)
		}
		if r.Windows[0].Cap != c.cap5 || r.Windows[1].Cap != c.c7 {
			t.Errorf("%s: caps %.0f/%.0f, want %.0f/%.0f", c.name, r.Windows[0].Cap, r.Windows[1].Cap, c.cap5, c.c7)
		}
	}
}

// Aggressive accounts are preferred and conservative ones ranked lower, all else equal
// ("spend claude-7 conservatively, go rogue on claude-3").
func TestModePreference(t *testing.T) {
	s := defaults()
	s.Accounts = map[string]config.NextAccount{"claude-7": {Mode: "conservative"}, "claude-3": {Mode: "aggressive"}}
	mk := func(l string) Candidate {
		return cand(l, win("five_hour", 10, 3*time.Hour, time.Minute), win("seven_day", 30, 72*time.Hour, time.Minute))
	}
	res := Rank(Input{Now: t0, Settings: s, Candidates: []Candidate{mk("claude-7"), mk("claude-5"), mk("claude-3")}})
	got := []string{res.Ranked[0].Label, res.Ranked[1].Label, res.Ranked[2].Label}
	if got[0] != "claude-3" || got[1] != "claude-5" || got[2] != "claude-7" {
		t.Errorf("order %v, want claude-3, claude-5, claude-7", got)
	}
}

func TestStalenessCanExclude(t *testing.T) {
	// 5h at 65% read 2 h ago: fresh it fits (65 + 15 for the session), stale the unknown
	// 2 h widen it by up to one session's worth and it may not.
	hist := []Sample{{At: t0.Add(-3 * time.Hour), Pct: 45}, {At: t0.Add(-2 * time.Hour), Pct: 65}}
	fresh := cand("fresh", Window{Key: "five_hour", UsedPct: 65, ResetsAt: t0.Add(2 * time.Hour), UpdatedAt: t0, Source: "hub"})
	stale := cand("stale", Window{Key: "five_hour", UsedPct: 65, ResetsAt: t0.Add(2 * time.Hour), UpdatedAt: t0.Add(-2 * time.Hour),
		Samples: hist, Source: "hub"})
	res := Rank(Input{Now: t0, Settings: defaults(), Candidates: []Candidate{stale, fresh}})
	byLabel := map[string]Ranked{}
	for _, r := range res.Ranked {
		byLabel[r.Label] = r
	}
	if !byLabel["fresh"].Eligible {
		t.Errorf("fresh: %s", byLabel["fresh"].Excluded)
	}
	st := byLabel["stale"]
	if !st.Stale || st.Windows[0].Uncertainty <= 0 || st.Windows[0].Now < 65 {
		t.Errorf("stale reading not widened: %+v", st.Windows[0])
	}
	if st.Eligible {
		t.Errorf("stale 65%% with an unknown 2 h should not fit a 90%% cap: %+v", st.Windows[0])
	}
}

func TestLoginExclusion(t *testing.T) {
	out := cand("out")
	out.LoggedIn = false
	since := t0.Add(-time.Hour)
	so := cand("so")
	so.SignedOut = &since
	ok := cand("ok")
	res := Rank(Input{Now: t0, Settings: defaults(), Candidates: []Candidate{out, so, ok}})
	if res.Pick == nil || res.Pick.Label != "ok" {
		t.Fatalf("pick %+v", res.Pick)
	}
	for _, r := range res.Ranked[1:] {
		if r.Eligible || r.Excluded == "" {
			t.Errorf("%s: eligible %v %q", r.Label, r.Eligible, r.Excluded)
		}
	}
	// Logged out everywhere: nothing frees up at a known time.
	res = Rank(Input{Now: t0, Settings: defaults(), Candidates: []Candidate{out}})
	if res.Pick != nil || res.NextFree != nil {
		t.Errorf("logged out: pick %v next free %v", res.Pick, res.NextFree)
	}
}

func TestNoEligibleNamesEarliestReset(t *testing.T) {
	a := cand("a", win("five_hour", 95, 2*time.Hour, time.Minute), win("seven_day", 50, 48*time.Hour, time.Minute))
	b := cand("b", win("five_hour", 20, 1*time.Hour, time.Minute), win("seven_day", 95, 30*time.Hour, time.Minute))
	c := cand("c", win("five_hour", 92, 40*time.Minute, time.Minute), win("seven_day", 96, 20*time.Hour, time.Minute))
	res := Rank(Input{Now: t0, Settings: defaults(), Candidates: []Candidate{a, b, c}})
	if res.Pick != nil {
		t.Fatalf("picked %s", res.Pick.Label)
	}
	// a frees at its 5h reset (2 h); b at its 7d reset (30 h); c needs both (20 h).
	if res.NextFree == nil || !res.NextFree.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("next free %v, want %v", res.NextFree, t0.Add(2*time.Hour))
	}
	if NoneLine(res) == "" {
		t.Error("empty none line")
	}
}

// Ten agents launched in a burst spread over the accounts instead of all landing on one,
// and stop when the caps are reached.
func TestBurstReservationsSpread(t *testing.T) {
	cs := []Candidate{
		cand("a", win("five_hour", 10, 3*time.Hour, time.Minute), win("seven_day", 30, 72*time.Hour, time.Minute)),
		cand("b", win("five_hour", 10, 3*time.Hour, time.Minute), win("seven_day", 30, 72*time.Hour, time.Minute)),
		cand("c", win("five_hour", 10, 3*time.Hour, time.Minute), win("seven_day", 30, 72*time.Hour, time.Minute)),
	}
	s := defaults()
	var l Ledger
	counts := map[string]int{}
	for i := 0; i < 9; i++ {
		now := t0.Add(time.Duration(i) * 5 * time.Second)
		res := Rank(Input{Now: now, Settings: s, Candidates: cs, Pending: l.Pending(now), LastPicked: l.Last, LastKey: l.LastKey})
		if res.Pick == nil {
			t.Fatalf("pick %d: none", i)
		}
		counts[res.Pick.Label]++
		l.Record(res.Pick.Key(), res.Pick.Folder, now, s.ReserveTTL)
	}
	for _, k := range []string{"a", "b", "c"} {
		if counts[k] != 3 {
			t.Errorf("counts %v, want 3 each", counts)
			break
		}
	}
	// One account at 50% 5h: 50 + 15 per session fits twice under 90, then nothing.
	one := []Candidate{cand("x", win("five_hour", 50, 3*time.Hour, time.Minute))}
	var l2 Ledger
	picks := 0
	var last Result
	for i := 0; i < 5; i++ {
		last = Rank(Input{Now: t0, Settings: s, Candidates: one, Pending: l2.Pending(t0)})
		if last.Pick == nil {
			break
		}
		picks++
		l2.Record(last.Pick.Key(), last.Pick.Folder, t0, s.ReserveTTL)
	}
	if picks != 2 || last.NextFree == nil || !last.NextFree.Equal(t0.Add(3*time.Hour)) {
		t.Errorf("picks %d next free %v", picks, last.NextFree)
	}
	// Reservations expire after the TTL.
	if p := l2.Pending(t0.Add(s.ReserveTTL + time.Second)); len(p) != 0 {
		t.Errorf("pending after ttl: %v", p)
	}
}

func TestStrategies(t *testing.T) {
	// a: weekly room that expires in 10 h; b: a fresh week; c: little weekly room, early 5h.
	a := cand("a", win("five_hour", 20, 3*time.Hour, time.Minute), win("seven_day", 20, 10*time.Hour, time.Minute))
	b := cand("b", win("five_hour", 0, 5*time.Hour, time.Minute), win("seven_day", 10, 144*time.Hour, time.Minute))
	c := cand("c", win("five_hour", 5, 4*time.Hour, time.Minute), win("seven_day", 85, 48*time.Hour, time.Minute))
	cs := []Candidate{a, b, c}
	s := defaults()
	s.Accounts = map[string]config.NextAccount{"c": {Priority: 1}, "b": {Priority: 2}}
	cases := []struct {
		strategy string
		last     map[string]time.Time
		lastKey  string
		want     string
	}{
		{"smart", nil, "", "a"},
		{"expiring", nil, "", "a"},
		{"most-room", nil, "", "b"},
		{"fill", nil, "", "c"},
		{"spread", map[string]time.Time{a.Key(): t0.Add(-time.Minute), b.Key(): t0.Add(-time.Hour)}, "", "c"},
		{"spread", map[string]time.Time{a.Key(): t0.Add(-time.Minute), b.Key(): t0.Add(-time.Hour), c.Key(): t0}, "", "b"},
		{"sticky", map[string]time.Time{b.Key(): t0}, b.Key(), "b"},
		{"sticky", map[string]time.Time{c.Key(): t0}, c.Key(), "a"}, // c is past its soft cap: smart
	}
	for _, x := range cases {
		res := Rank(Input{Now: t0, Strategy: x.strategy, Settings: s, Candidates: cs, LastPicked: x.last, LastKey: x.lastKey})
		if res.Pick == nil || res.Pick.Label != x.want {
			got := "none"
			if res.Pick != nil {
				got = res.Pick.Label
			}
			t.Errorf("%s (last %s): picked %s, want %s", x.strategy, x.lastKey, got, x.want)
		}
	}
}

func TestModelWindows(t *testing.T) {
	c := cand("a", win("five_hour", 10, 3*time.Hour, time.Minute), win("seven_day", 30, 72*time.Hour, time.Minute),
		win("seven_day_opus", 89, 72*time.Hour, time.Minute), win("seven_day_oauth_apps", 99, 72*time.Hour, time.Minute))
	res := Rank(Input{Now: t0, Settings: defaults(), Candidates: []Candidate{c}})
	if res.Pick == nil || len(res.Pick.Windows) != 2 {
		t.Fatalf("no model: %+v", res.Ranked[0])
	}
	res = Rank(Input{Now: t0, Model: "claude-opus-5", Settings: defaults(), Candidates: []Candidate{c}})
	if res.Pick != nil || len(res.Ranked[0].Windows) != 3 {
		t.Errorf("opus: the opus week at 89%% + a session is over 90%%: %+v", res.Ranked[0])
	}
	res = Rank(Input{Now: t0, Model: "sonnet", Settings: defaults(), Candidates: []Candidate{c}})
	if res.Pick == nil {
		t.Error("sonnet: the opus week must not count")
	}
}

func TestAllowedHours(t *testing.T) {
	s := defaults()
	s.Accounts = map[string]config.NextAccount{"work": {Hours: "09:00-17:00", Days: "mon-fri"}}
	sat := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	res := Rank(Input{Now: sat, Settings: s, Candidates: []Candidate{cand("work")}})
	want := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	if res.Pick != nil || res.NextFree == nil || !res.NextFree.Equal(want) {
		t.Errorf("saturday: pick %v next %v, want %v", res.Pick, res.NextFree, want)
	}
	res = Rank(Input{Now: t0, Settings: s, Candidates: []Candidate{cand("work")}})
	if res.Pick == nil {
		t.Errorf("wednesday noon: %s", res.Ranked[0].Excluded)
	}
	s.Accounts = map[string]config.NextAccount{"work": {Hours: "22:00-06:00"}}
	for _, h := range []int{23, 3} {
		res = Rank(Input{Now: time.Date(2026, 11, 18, h, 0, 0, 0, time.UTC), Settings: s, Candidates: []Candidate{cand("work")}})
		if res.Pick == nil {
			t.Errorf("over midnight at %d:00: %s", h, res.Ranked[0].Excluded)
		}
	}
}

// Labels differ between machines: the hub says claude-1 where this machine says claude.
// Seats are matched by login, and settings keyed by email or by either label apply.
func TestSeatMappingAcrossMachines(t *testing.T) {
	str := func(s string) *string { return &s }
	slot2 := "~/.claude-2"
	seats := []HubSeat{
		{ID: "seat_1", Account: "a1", Org: "o1", Email: str("one@example.test"), Label: str("claude-1"),
			Windows: []HubWindow{{Key: "five_hour", UsedPct: ptr(30), ResetsAt: tp(t0.Add(time.Hour)), UpdatedAt: tp(t0), State: "current"}}},
		{ID: "seat_2", Account: "a2", Org: "o2", Email: str("two@example.test"), LiveSessions: 2},
		{ID: "seat_3", Account: "a3", Org: "o3", Email: str("three@example.test")},
		{ID: "seat_4a", Account: "a4", Org: "o4a", Email: str("dup@example.test")},
		{ID: "seat_4b", Account: "a4", Org: "o4b", Email: str("dup@example.test")},
	}
	seats[1].Places = append(seats[1].Places, struct {
		Machine string  `json:"machine"`
		Slot    *string `json:"slot"`
	}{"m_me", &slot2})
	seats[2].SignedOut = append(seats[2].SignedOut, struct {
		Machine string    `json:"machine"`
		Slot    string    `json:"slot"`
		Since   time.Time `json:"since"`
	}{"m_me", "~/.claude-3", t0.Add(-time.Hour)})
	slots := []Slot{
		{Folder: "~/.claude", Label: "claude", LoggedIn: true, Account: "a1", Org: "o1", Email: "one@example.test"},
		{Folder: "~/.claude-2", Label: "claude-2", LoggedIn: true, Email: "elsewhere@example.test"}, // auth status only
		{Folder: "~/.claude-3", Label: "claude-3", LoggedIn: true, Email: "THREE@example.test"},
		{Folder: "~/.claude-4", Label: "claude-4", LoggedIn: true, Email: "dup@example.test"},
	}
	cs := Candidates(slots, Sources{Machine: "m_me", Hub: &HubData{Seats: seats}})
	want := []struct{ seat, by string }{{"seat_1", "account"}, {"seat_2", "place"}, {"seat_3", "email"}, {"", ""}}
	for i, w := range want {
		if cs[i].SeatID != w.seat || cs[i].MatchedBy != w.by {
			t.Errorf("%s: seat %q by %q, want %q by %q", cs[i].Folder, cs[i].SeatID, cs[i].MatchedBy, w.seat, w.by)
		}
	}
	if cs[0].HubLabel != "claude-1" || cs[1].LiveSessions != 2 || cs[2].SignedOut == nil {
		t.Errorf("join: %+v", cs[:3])
	}
	for _, key := range []string{"one@example.test", "ONE@example.test", "claude-1", "claude", "seat_1", "~/.claude"} {
		s := defaults()
		s.Accounts = map[string]config.NextAccount{key: {Mode: "aggressive"}}
		if p, _ := s.PolicyFor(cs[0]); p.Mode != "aggressive" {
			t.Errorf("settings key %q did not apply", key)
		}
	}
	s := defaults()
	s.Accounts = map[string]config.NextAccount{"claude-2": {Mode: "conservative"}}
	if p, _ := s.PolicyFor(cs[0]); p.Mode != "normal" {
		t.Error("another folder's label applied")
	}
}

func TestLocalOverlayAndFallback(t *testing.T) {
	str := func(s string) *string { return &s }
	seat := HubSeat{ID: "seat_1", Account: "a1", Org: "o1", Email: str("one@example.test"),
		Windows: []HubWindow{{Key: "five_hour", UsedPct: ptr(30), ResetsAt: tp(t0.Add(time.Hour)), UpdatedAt: tp(t0.Add(-40 * time.Minute)), State: "current"}}}
	local := []LocalSample{
		{At: t0.Add(-50 * time.Minute), Slot: "~/.claude", Account: "a1", Org: "o1", Five: ptr(28), FiveReset: tp(t0.Add(time.Hour))},
		{At: t0.Add(-5 * time.Minute), Slot: "~/.claude-9", Account: "a1", Org: "o1", Five: ptr(41), FiveReset: tp(t0.Add(time.Hour)),
			Seven: ptr(12), SevenReset: tp(t0.Add(50 * time.Hour))},
		{At: t0.Add(-time.Minute), Slot: "~/.claude-2", Account: "a2", Org: "o2", Five: ptr(99)},
	}
	slots := []Slot{{Folder: "~/.claude", Label: "claude", LoggedIn: true, Account: "a1", Org: "o1"}}
	cs := Candidates(slots, Sources{Machine: "m", Hub: &HubData{Seats: []HubSeat{seat}}, Local: local})
	c := cs[0]
	if c.Source != "hub+local" || len(c.Windows) != 2 {
		t.Fatalf("overlay: %+v", c)
	}
	if w := c.Windows[0]; w.UsedPct != 41 || w.Source != "local" || len(w.Samples) != 1 {
		t.Errorf("five_hour: %+v (the newer local reading of the same seat wins; older ones are not merged)", w)
	}
	// No hub: local readings only.
	cs = Candidates(slots, Sources{Machine: "m", Local: local})
	if cs[0].Source != "local" || cs[0].Windows[0].UsedPct != 41 {
		t.Errorf("local only: %+v", cs[0])
	}
	res := Rank(Input{Now: t0, Settings: defaults(), Candidates: cs})
	if res.Pick == nil || res.Pick.Source != "local" {
		t.Fatalf("local pick: %+v", res.Ranked)
	}
	if got := res.Pick.Reason; !contains(got, "local data") {
		t.Errorf("reason %q does not say the data is local", got)
	}
}

func TestReason(t *testing.T) {
	s := defaults()
	s.Accounts = map[string]config.NextAccount{"claude-3": {Mode: "aggressive"}}
	c := cand("claude-3", win("five_hour", 0, 5*time.Hour, time.Minute), win("seven_day", 23, 24*time.Hour, time.Minute))
	c.Windows[0].Idle = true
	res := Rank(Input{Now: t0, Settings: s, Candidates: []Candidate{c}})
	want := "claude-3  ~/.claude-3  (7d 23% → ~29% at reset Thu 12:00; 5h fresh; aggressive)"
	if got := Line(*res.Pick); got != want {
		t.Errorf("line\n got %q\nwant %q", got, want)
	}
	if EnvArgs(*res.Pick) != "CLAUDE_CONFIG_DIR=/home/u/.claude-3" {
		t.Errorf("env %q", EnvArgs(*res.Pick))
	}
	res.Pick.Default = true
	if EnvArgs(*res.Pick) != "-u CLAUDE_CONFIG_DIR" {
		t.Errorf("default view env %q", EnvArgs(*res.Pick))
	}
}

// Concurrent `wy account next` runs each see the others' reservations.
func TestStoreConcurrentUpdates(t *testing.T) {
	st := Store{Dir: filepath.Join(t.TempDir(), "next")}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := st.Update(t0, func(l *Ledger) error {
				l.Record("k", "~/.claude", t0, 10*time.Minute)
				return nil
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	l, err := st.Read(t0)
	if err != nil || l.Pending(t0)["k"] != 20 {
		t.Errorf("pending %v err %v", l.Pending(t0), err)
	}
	if l, _ := st.Read(t0.Add(11 * time.Minute)); len(l.Reservations) != 0 || l.LastKey != "k" {
		t.Errorf("after the ttl: %+v", l)
	}
	if err := os.WriteFile(filepath.Join(st.Dir, "picks.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l, err := st.Read(t0); err != nil || len(l.Reservations) != 0 {
		t.Errorf("damaged file: %+v %v", l, err)
	}
}

func ptr(f float64) *float64      { return &f }
func tp(t time.Time) *time.Time   { return &t }
func contains(s, sub string) bool { return len(sub) == 0 || indexOf(s, sub) >= 0 }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// An account with no usage data at all is treated as uncertain, behind one whose usage is
// known to fit.
func TestNoDataRanksBehindKnown(t *testing.T) {
	unknown := cand("unknown")
	unknown.Source = "none"
	known := cand("known", win("five_hour", 10, 3*time.Hour, time.Minute), win("seven_day", 30, 72*time.Hour, time.Minute))
	res := Rank(Input{Now: t0, Settings: defaults(), Candidates: []Candidate{unknown, known}})
	if res.Pick == nil || res.Pick.Label != "known" {
		t.Fatalf("pick %+v", res.Ranked)
	}
	u := res.Ranked[1]
	if !u.Eligible || !u.Stale || u.Windows[0].Uncertainty != 15 || !contains(u.Reason, "no usage data") {
		t.Errorf("unknown %+v", u)
	}
}
