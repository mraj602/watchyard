package record

import "testing"

// The pin reserve fields of a verdict (record.md 4.11): levels in range, a time in RFC 3339.
func TestAccountVerdictPins(t *testing.T) {
	f := func(x float64) *float64 { return &x }
	s := func(x string) *string { return &x }
	n := 2
	ok := AccountVerdict{Account: "a", Org: "o", Eligible: true, Pins: &n, PinReserve5h: f(20), PinReserve7d: f(2.1),
		PinFull5h: f(20), PinFull7d: f(10), WeeklyResetsAt: s("2026-11-26T03:30:00Z")}
	for _, c := range []struct {
		name string
		mod  func(*AccountVerdict)
		good bool
	}{
		{"scaled reserve", func(*AccountVerdict) {}, true},
		{"negative reserve", func(a *AccountVerdict) { a.PinReserve7d = f(-1) }, false},
		{"bad reset time", func(a *AccountVerdict) { a.WeeklyResetsAt = s("Thu 03:30") }, false},
		{"negative pins", func(a *AccountVerdict) { m := -1; a.Pins = &m }, false},
	} {
		a := ok
		c.mod(&a)
		p := AccountVerdictsPayload{Strategy: "lanes", Accounts: []AccountVerdict{a}}
		if err := p.Validate(); (err == nil) != c.good {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
