package picker

import (
	"strings"
	"testing"
)

// setup.md 8.6, Pinned sessions: an account's live pinned sessions keep a reserve free; every
// limit of the account is that much lower for new lanes, and --explain shows it.
func TestLanesPinReserve(t *testing.T) {
	s := world(40, 20)
	s["claude-2"] = smp(40, 85, 5, 72) // under 98, over 98 - 15
	in := laneIn(s, 1)
	in.Params = DefaultLaneParams
	r := rank(t, in)
	row := func(r LaneResult, k string) LaneRow {
		for _, x := range r.Table {
			if x.Key == k {
				return x
			}
		}
		t.Fatalf("no row %s", k)
		return LaneRow{}
	}
	if x := row(r, "claude-2"); x.Skip && x.SkipLevel == 3 && strings.HasPrefix(x.Reason, "5h 85%") {
		t.Fatalf("skipped without a reserve: %+v", x)
	}
	for i := range in.Accounts {
		if in.Accounts[i].Key == "claude-2" {
			in.Accounts[i].Pins, in.Accounts[i].PinReserve5, in.Accounts[i].PinReserve7 = 1, 15, 3
		}
	}
	r = rank(t, in)
	x := row(r, "claude-2")
	if !x.Skip || x.Reason != "5h 85% >= 83" || x.FiveHourCap != 83 || x.WeeklyCap != 95 || x.Pins != 1 {
		t.Fatalf("with the reserve: %+v", x)
	}
	if len(r.Picks) != 1 || r.Picks[0].Key == "claude-2" {
		t.Fatalf("picks %+v", r.Picks)
	}
	v := laneView(x, 0, lnow)
	if v.Pins != 1 || v.PinReserve5hPct != 15 || v.PinReserve7dPct != 3 {
		t.Fatalf("view %+v", v)
	}
	if l := pinLine("claude-2", v); l != "Pins claude-2: 1 live pinned session keeps 15% of the 5-hour window and 3% of the week free (caps now 5h 83, 7d 95)" {
		t.Fatalf("line %q", l)
	}
}
