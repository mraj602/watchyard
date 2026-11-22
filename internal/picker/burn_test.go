package picker

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/leases"
)

// pts is the weighted units worth p five-hour points at the fallback meter factor.
func pts(p float64) float64 { return p / DefaultLaneParams.F5 }

// burnAt is a hub burn: pairs of minutes after the pick and points.
func burnAt(mp ...float64) *[][2]float64 {
	var b [][2]float64
	for i := 0; i+1 < len(mp); i += 2 {
		b = append(b, [2]float64{mp[i] * 60, pts(mp[i+1])})
	}
	if b == nil {
		b = [][2]float64{}
	}
	return &b
}

// burnAccount is account a with a 5-hour reading of 20 % taken `ago` before t0.
func burnAccount(ago time.Duration) Candidate {
	return cand("a", win("five_hour", 20, 3*time.Hour, ago), win("seven_day", 30, 72*time.Hour, ago))
}

func decideA(t *testing.T, in Input) (LaneView, string) {
	t.Helper()
	in.Now, in.N = t0, 1
	in.Settings = SettingsFrom(config.Next{})
	res, err := Decide(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Ranked {
		if r.Label == "a" && r.Lanes != nil {
			var b bytes.Buffer
			if err := Explain(&b, res, "hub", ""); err != nil {
				t.Fatal(err)
			}
			return *r.Lanes, b.String()
		}
	}
	t.Fatalf("no lanes view for a: %+v", res.Ranked)
	return LaneView{}, ""
}

func nearB(a, b float64) bool { return math.Abs(a-b) < 0.051 }

// Another machine's lease measured by the hub: what its session burned before the reading is
// in the reading, what it burned after is in the meter, and the lease charges only the rest
// of its expected cost, so h = reading + meter + max(0, c − b) and nothing counts twice
// (setup.md 8.5, effective state 3).
func TestLeaseBurnFromHub(t *testing.T) {
	cases := []struct {
		name               string
		burn               *[][2]float64
		h, meter, charge   float64
		burnedPct, from, s string
	}{
		// 1.4 points 5 minutes after the pick (before the reading 20 minutes ago), 2.8 after it.
		{"under its charge", burnAt(5, 1.4, 30, 2.8), 20 + 2.8 + (8 - 4.2), 2.8, 3.8, "", burnHub, LeaseActive},
		// 5.6 before the reading and 2.8 after: b = 8.4 > c, no charge left.
		{"over its charge", burnAt(5, 5.6, 30, 2.8), 20 + 2.8, 2.8, 0, "", burnHub, LeaseActive},
		// The hub measured it and saw no token yet: the full charge, but no settling either.
		{"no token yet", burnAt(), 28, 0, 8, "", burnNone, LeaseActive},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := burnAccount(20 * time.Minute)
			a.RemoteLeases = []HubLease{{ID: "L1", At: t0.Add(-40 * time.Minute), Kind: "aggressive", C5: 8, CF: 8, HasSession: true,
				SessionHash: sessionHash("remote-1"), Launched: true, Burn: c.burn}}
			v, explain := decideA(t, Input{Candidates: []Candidate{a}})
			if !nearB(v.FiveHourPct, c.h) || !nearB(v.Meter5hPct, c.meter) || v.Leases != 1 {
				t.Errorf("h %v meter %v leases %d, want h %v meter %v", v.FiveHourPct, v.Meter5hPct, v.Leases, c.h, c.meter)
			}
			if len(v.LeaseCharges) != 1 {
				t.Fatalf("lease charges %+v", v.LeaseCharges)
			}
			lc := v.LeaseCharges[0]
			if lc.Machine != "other" || lc.SessionID != "" || lc.BurnedFrom != c.from || lc.State != c.s || !nearB(lc.Charge5hPct, c.charge) {
				t.Errorf("lease charge %+v", lc)
			}
			if !strings.Contains(explain, "Lease a ") || !strings.Contains(explain, "(other machine): active") {
				t.Errorf("explain:\n%s", explain)
			}
		})
	}
}

// A session this machine has (found by the hub's session hash) is counted from one source
// only, the larger: its local rows, or the hub's burn, never both.
func TestLeaseBurnOneSource(t *testing.T) {
	for _, c := range []struct {
		name         string
		local, hub   float64
		from         string
		meter, total float64
	}{
		{"local is larger (the hub lags)", 3, 2, burnLocal, 3, 28},
		{"hub is larger", 1, 3, burnHub, 3, 28},
		{"equal: local", 2, 2, burnLocal, 2, 28},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := burnAccount(20 * time.Minute)
			a.RemoteLeases = []HubLease{{ID: "L1", At: t0.Add(-40 * time.Minute), Kind: "aggressive", C5: 8, CF: 8, HasSession: true,
				SessionHash: sessionHash("here-1"), Launched: true, Burn: burnAt(30, c.hub)}}
			ob := Outbox{Sessions: map[string]HookSession{"here-1": {Last: t0.Add(-5 * time.Minute), Slot: "~/.elsewhere"}},
				Tokens: []TokenUse{{At: t0.Add(-10 * time.Minute), Session: "here-1", Units: pts(c.local)}}}
			v, _ := decideA(t, Input{Candidates: []Candidate{a}, Outbox: ob})
			lc := v.LeaseCharges[0]
			if lc.BurnedFrom != c.from || !nearB(v.Meter5hPct, c.meter) || !nearB(v.FiveHourPct, 20+c.meter+(8-c.meter)) {
				t.Errorf("from %s meter %v h %v, want %s %v %v", lc.BurnedFrom, v.Meter5hPct, v.FiveHourPct, c.from, c.meter, c.total)
			}
		})
	}
}

// This machine's lease whose lane runs elsewhere: the hub's own_leases measure it (launched,
// burned, ended), so it is neither dropped as unlaunched nor charged in full.
func TestOwnLeaseMeasuredByHub(t *testing.T) {
	c5 := 8.0
	ledger := []leases.Lease{{Line: leases.Line{Op: leases.OpLease, ID: "L9", At: t0.Add(-30 * time.Minute), Key: "acct:acct-a/org",
		SessionID: "S9", C5: &c5, CF: &c5}}}
	for _, c := range []struct {
		name   string
		ended  bool
		h      float64
		state  string
		leases int
	}{
		{"running", false, 20 + 3 + 5, LeaseActive, 1},
		{"ended there", true, 20 + 3, LeaseEnded, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := burnAccount(20 * time.Minute)
			a.OwnLeases = []HubLease{{ID: "L9", At: t0.Add(-30 * time.Minute), HasSession: true, SessionHash: sessionHash("S9"),
				Launched: true, Ended: c.ended, Seen: true, Burn: burnAt(20, 3)}}
			v, _ := decideA(t, Input{Candidates: []Candidate{a}, Leases: ledger})
			if !nearB(v.FiveHourPct, c.h) || v.Leases != c.leases || len(v.LeaseCharges) != 1 {
				t.Fatalf("h %v leases %d charges %+v, want h %v", v.FiveHourPct, v.Leases, v.LeaseCharges, c.h)
			}
			lc := v.LeaseCharges[0]
			if lc.Machine != "this" || lc.SessionID != "S9" || lc.BurnedFrom != burnHub || lc.State != c.state || !lc.Launched {
				t.Errorf("lease charge %+v", lc)
			}
		})
	}
	// Without the hub's measure the same lease has no launched line and is dropped.
	v, _ := decideA(t, Input{Candidates: []Candidate{burnAccount(20 * time.Minute)}, Leases: ledger})
	if len(v.LeaseCharges) != 0 || !nearB(v.FiveHourPct, 20) {
		t.Errorf("unmeasured: %+v", v)
	}
}

// Another machine's lease that nothing measures (an older hub, or the kept copy with the hub
// down) settles at the first reading 10 minutes after its pick: that reading holds its use.
// Before such a reading it charges in full (the conservative side), and its weekly charge
// follows keep_w.
func TestRemoteLeaseSettles(t *testing.T) {
	for _, c := range []struct {
		name  string
		ago   time.Duration // reading age; the pick was 40 minutes ago
		h     float64
		state string
	}{
		{"reading after settle", 20 * time.Minute, 20, LeaseSettled},
		{"reading at settle", 30 * time.Minute, 20, LeaseSettled},
		{"reading before settle", 35 * time.Minute, 28, LeaseActive},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := burnAccount(c.ago)
			a.RemoteCopy = true
			a.RemoteLeases = []HubLease{{ID: "L1", At: t0.Add(-40 * time.Minute), Kind: "aggressive", C5: 8, CF: 8, HasSession: true,
				Launched: true}}
			v, explain := decideA(t, Input{Candidates: []Candidate{a}})
			if !nearB(v.FiveHourPct, c.h) || len(v.LeaseCharges) != 1 {
				t.Fatalf("h %v charges %+v, want %v", v.FiveHourPct, v.LeaseCharges, c.h)
			}
			lc := v.LeaseCharges[0]
			if lc.State != c.state || lc.BurnedFrom != burnNone || !lc.Copy || lc.WeeklyKeptPct != 0 {
				t.Errorf("lease charge %+v", lc)
			}
			if !strings.Contains(explain, "(other machine, kept copy): "+c.state) || !strings.Contains(explain, "burned none") {
				t.Errorf("explain:\n%s", explain)
			}
		})
	}
	// A local session found by its hash measures it: it does not settle.
	a := burnAccount(20 * time.Minute)
	a.RemoteLeases = []HubLease{{ID: "L1", At: t0.Add(-40 * time.Minute), Kind: "aggressive", C5: 8, CF: 8, HasSession: true,
		SessionHash: sessionHash("here-2"), Launched: true}}
	ob := Outbox{Sessions: map[string]HookSession{"here-2": {Last: t0.Add(-time.Minute)}},
		Tokens: []TokenUse{{At: t0.Add(-5 * time.Minute), Session: "here-2", Units: pts(1)}}}
	v, _ := decideA(t, Input{Candidates: []Candidate{a}, Outbox: ob})
	if lc := v.LeaseCharges[0]; lc.State != LeaseActive || lc.BurnedFrom != burnLocal || !nearB(v.FiveHourPct, 20+1+7) {
		t.Errorf("local session: h %v %+v", v.FiveHourPct, lc)
	}
}

// A session ended per the hub, or per this machine's hook events, ends its lease; its tokens
// after the reading stay in the meter.
func TestLeaseEnded(t *testing.T) {
	a := burnAccount(20 * time.Minute)
	a.RemoteLeases = []HubLease{{ID: "L1", At: t0.Add(-40 * time.Minute), Kind: "aggressive", C5: 8, CF: 8, HasSession: true,
		SessionHash: sessionHash("r-3"), Launched: true, Ended: true, Seen: true, Burn: burnAt(30, 2)}}
	v, _ := decideA(t, Input{Candidates: []Candidate{a}})
	if v.Leases != 0 || !nearB(v.FiveHourPct, 22) || v.LeaseCharges[0].State != LeaseEnded {
		t.Errorf("ended per the hub: %+v", v)
	}
	c5 := 8.0
	ledger := []leases.Lease{{Line: leases.Line{Op: leases.OpLease, ID: "L2", At: t0.Add(-30 * time.Minute), Key: "acct:acct-a/org",
		SessionID: "S2", C5: &c5, CF: &c5}, Launched: t0.Add(-29 * time.Minute)}}
	ob := Outbox{Sessions: map[string]HookSession{"S2": {Last: t0.Add(-2 * time.Minute), Ended: true}},
		Tokens: []TokenUse{{At: t0.Add(-10 * time.Minute), Session: "S2", Units: pts(1)}}}
	v, _ = decideA(t, Input{Candidates: []Candidate{burnAccount(20 * time.Minute)}, Leases: ledger, Outbox: ob})
	if v.Leases != 0 || !nearB(v.FiveHourPct, 21) || v.LeaseCharges[0].State != LeaseEnded {
		t.Errorf("ended per this machine's hook events: %+v", v)
	}
}
