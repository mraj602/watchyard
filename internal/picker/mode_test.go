package picker

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/config"
)

// The mode of an account, per machine (setup.md 8.5, Kind): this machine's
// next.accounts.<key>.mode, then the seat's hub setting, then this machine's slots.modes
// proposal; none is normal (aggressive) with mode_from none, as before modes were shared.
func TestModePrecedence(t *testing.T) {
	c := cand("a")
	for _, tc := range []struct {
		name               string
		local, hub, prop   string
		wantMode, wantFrom string
		protected          bool
	}{
		{"none", "", "", "", "normal", ModeFromNone, false},
		{"proposed", "", "", "protected", "protected", ModeFromProposed, true},
		{"hub over proposed", "", "aggressive", "protected", "aggressive", ModeFromHub, false},
		{"local over hub", "conservative", "aggressive", "aggressive", "conservative", ModeFromLocal, true},
		{"local normal over hub", "normal", "protected", "", "normal", ModeFromLocal, false},
		{"bad hub value", "", "normal", "", "normal", ModeFromNone, false},
		{"bad proposal", "", "", "normal", "normal", ModeFromNone, false},
		{"bad local value falls through", "fast", "protected", "", "protected", ModeFromHub, true},
	} {
		s := defaults()
		if tc.local != "" {
			s.Accounts = map[string]config.NextAccount{c.Email: {Mode: tc.local}}
		}
		if tc.prop != "" {
			s.Proposed = map[string]string{c.Folder: tc.prop}
		}
		cc := c
		cc.HubMode = tc.hub
		p, _ := s.PolicyFor(cc)
		if p.Mode != tc.wantMode || p.ModeFrom != tc.wantFrom || p.Protected() != tc.protected {
			t.Errorf("%s: mode %q from %q protected %v, want %q from %q %v", tc.name, p.Mode, p.ModeFrom, p.Protected(),
				tc.wantMode, tc.wantFrom, tc.protected)
		}
		want := Presets[tc.wantMode]
		if p.Cap7d != want.Cap7d || p.Pref != want.Pref {
			t.Errorf("%s: preset %+v, want %+v", tc.name, p, want)
		}
	}
	// A key with other settings but no mode leaves the mode to the hub.
	s := defaults()
	s.Accounts = map[string]config.NextAccount{"a": {Priority: 1}}
	cc := c
	cc.HubMode = "protected"
	if p, _ := s.PolicyFor(cc); p.ModeFrom != ModeFromHub || p.Key != "a" || p.HubMode != "protected" {
		t.Errorf("key without mode: %+v", p)
	}
}

// Candidates take the seat's hub mode from /v1/usage or /v1/accounts (Seat.mode).
func TestCandidatesHubMode(t *testing.T) {
	str := func(s string) *string { return &s }
	seats := []HubSeat{{ID: "s1", Account: "a1", Org: "o1", Mode: str("protected")},
		{ID: "s2", Account: "a2", Org: "o2", Mode: str("normal")}, {ID: "s3", Account: "a3", Org: "o3"}}
	slots := []Slot{{Folder: "~/.c1", Account: "a1", Org: "o1"}, {Folder: "~/.c2", Account: "a2", Org: "o2"},
		{Folder: "~/.c3", Account: "a3", Org: "o3"}}
	cs := Candidates(slots, Sources{Hub: &HubData{Seats: seats}})
	if cs[0].HubMode != "protected" || cs[1].HubMode != "" || cs[2].HubMode != "" {
		t.Fatalf("hub modes %q %q %q", cs[0].HubMode, cs[1].HubMode, cs[2].HubMode)
	}
}

// Lanes: each account carries mode_from and its caps (record.md 4.11): protected the pace line
// min(T, T × (1 − h/168) + 10) and the 5-hour floor 45, aggressive 98 and 98.
func TestLanesModeFromAndCaps(t *testing.T) {
	s := SettingsFrom(config.Next{})
	s.Proposed = map[string]string{"~/.c": "aggressive"}
	a := cand("a", win("five_hour", 10, 3*time.Hour, time.Minute), win("seven_day", 10, 72*time.Hour, time.Minute))
	b := cand("b", win("five_hour", 10, 3*time.Hour, time.Minute), win("seven_day", 10, 72*time.Hour, time.Minute))
	b.HubMode = "protected"
	c := cand("c", win("five_hour", 10, 3*time.Hour, time.Minute), win("seven_day", 10, 72*time.Hour, time.Minute))
	res, err := Decide(Input{Now: t0, Settings: s, Candidates: []Candidate{a, b, c}, N: 1})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Ranked{}
	for _, r := range res.Ranked {
		by[r.Label] = r
	}
	if r := by["a"]; r.ModeFrom != ModeFromNone || r.Kind != "aggressive" || r.Mode != "normal" ||
		r.Lanes.WeeklyCap != 98 || r.Lanes.FiveHourCap != 98 {
		t.Errorf("a: %s %s %s %+v", r.ModeFrom, r.Kind, r.Mode, *r.Lanes)
	}
	pace := 85*(1-72.0/168) + 10
	if r := by["b"]; r.ModeFrom != ModeFromHub || r.Kind != "protected" || !near(r.Lanes.WeeklyCap, pace) ||
		r.Lanes.FiveHourCap != 45 {
		t.Errorf("b: %s %s %+v, want weekly cap %.1f", r.ModeFrom, r.Kind, *r.Lanes, pace)
	}
	if r := by["c"]; r.ModeFrom != ModeFromProposed || r.Kind != "aggressive" {
		t.Errorf("c: %s %s", r.ModeFrom, r.Kind)
	}

	// --explain: the kind column, one definition per kind shown, and the hint.
	var out bytes.Buffer
	if err := Explain(&out, res, "hub", ""); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"not chosen (treated as aggressive)", "protected (hub)", "aggressive (proposed here)",
		"Protected: " + ModeWords.Protected, "Aggressive: " + ModeWords.Aggressive, "Not chosen: " + ModeWords.NotChosen,
		"Choose on the board (Accounts, the account, How should the picker use this account?) or: wy config set next.accounts.a@example.test.mode protected",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("--explain lacks %q:\n%s", want, text)
		}
	}

	// A local setting names the hub's when it differs; no hint when every account is chosen.
	s.Accounts = map[string]config.NextAccount{"b": {Mode: "aggressive"}, "a": {Mode: "protected"}}
	res, _ = Decide(Input{Now: t0, Settings: s, Candidates: []Candidate{a, b}, N: 1})
	out.Reset()
	_ = Explain(&out, res, "hub", "")
	text = out.String()
	for _, want := range []string{"aggressive (set on this machine); hub says protected", "protected (set on this machine)"} {
		if !strings.Contains(text, want) {
			t.Errorf("--explain lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Not chosen") || strings.Contains(text, "Choose on the board") {
		t.Errorf("no account is unchosen:\n%s", text)
	}
}

// The one wording (setup.md 8.5, Kind; the board and the phone say the same).
func TestModeWords(t *testing.T) {
	if ModeWords.Protected != "Kept for you. New sessions only while this week is behind an even pace and the 5-hour window is under 45%." ||
		ModeWords.Aggressive != "Use it up. Sessions go here until 98% of the week or 5 hours." ||
		ModeWords.NotChosen != "Not chosen yet; treated as aggressive." {
		t.Fatalf("%+v", ModeWords)
	}
}
