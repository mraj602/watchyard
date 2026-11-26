package picker

import "testing"

// A protected account whose room needs N hours at full speed and whose weekly reset is 1.25 N
// away (need 10 h, reset in 12.5 h): with drain_slack_protected 1 it is not draining, at the
// product default 1.5 it is. The aggressive slack is max(24, slack × need).
func TestDrainSlackKeys(t *testing.T) {
	run := func(h, slackProt, slack float64) DrainInfo {
		s := dworld()
		s[dZ] = dsmp(80, 10, h, 3)
		in := laneIn(s, 1)
		in.Params = DefaultLaneParams
		in.Params.ProtDrainSlack, in.Params.DrainSlack = slackProt, slack
		for i := range in.Accounts {
			if a := &in.Accounts[i]; a.Key == dZ {
				a.Protected = true
			}
		}
		return rowOf(rank(t, in), dZ).Drain
	}
	probe := run(200, 1.5, 2)
	if probe.NeedH <= 0 || probe.SlackX != 1.5 {
		t.Fatalf("probe: %+v", probe)
	}
	h := 1.25 * probe.NeedH
	for _, c := range []struct {
		slack float64
		on    bool
	}{{1, false}, {1.5, true}, {1.2, false}, {1.3, true}} {
		d := run(h, c.slack, 2)
		if d.On != c.on || d.SlackX != c.slack {
			t.Errorf("protected slack %v, need %.1f h, reset in %.1f h: on %v (slack %v), want %v", c.slack, d.NeedH, h, d.On, d.SlackX, c.on)
		}
	}
	if DefaultLaneParams.ProtDrainSlack != 1.5 || DefaultLaneParams.DrainSlack != 2 {
		t.Errorf("defaults %v %v", DefaultLaneParams.DrainSlack, DefaultLaneParams.ProtDrainSlack)
	}
	// the aggressive floor (24 h) holds whatever the slack
	a := func(slack float64) float64 {
		s := dworld()
		s[dZ] = dsmp(80, 10, 200, 3)
		in := laneIn(s, 1)
		in.Params = DefaultLaneParams
		in.Params.DrainSlack = slack
		return rowOf(rank(t, in), dZ).Drain.HorizonH
	}
	if a(0.1) != 24 {
		t.Errorf("aggressive horizon with a small slack: %v, want the 24 h floor", a(0.1))
	}
}
