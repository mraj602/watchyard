package picker

import (
	"math"
	"time"
)

// LiveAccount is one account as Smart Account Switch reads it (setup.md 8.6, Inputs): its
// measured levels now (the reading, the token meter since it with its margins and the
// unexplained use; no lease charges), when its windows reset and its calibration.
type LiveAccount struct {
	Key       string
	Protected bool
	// FiveHour and Weekly: percent now; FiveReset zero when the 5-hour window has not started.
	FiveHour, Weekly       float64
	FiveReset, WeeklyReset time.Time
	// Ratio: 5-hour points per weekly point; F5: 5-hour points per weighted token unit.
	Ratio, F5 float64
	// Stale: its reading is too old or unknown to forecast.
	Stale bool
	// WeeklyRead is the weekly reading Weekly starts from (at ReadingAt); WeeklyMeter what the
	// token meter with its margin and the unexplained use add to it, weekly points. Shown only
	// (passes.jsonl): the picker's `--json` lanes weekly_measured_pct is the same Weekly.
	WeeklyRead, WeeklyMeter float64
	ReadingAt               time.Time
	// FiveRead is the 5-hour reading FiveHour starts from (the same reading, ReadingAt),
	// FiveMeter what the meter, its margin and the unexplained use add (FiveHour = FiveRead +
	// FiveMeter), and FiveLeases the lease charges the picker adds on top (its 5-hour level is
	// FiveHour + FiveLeases: lanes too young for the meter to have seen their cost). Shown only.
	FiveRead, FiveMeter, FiveLeases float64
}

// LiveLane is one running lane (a lease whose session runs): its burn over the last
// BurnWindow in 5-hour points per hour.
type LiveLane struct {
	Key, SID string
	T0       time.Time
	Rate     float64
	Measured bool
	Expect   time.Duration
	// ExpectEnd: its lease's expected end (zero: unknown): the switch projects its burn to it
	// while it runs inside it.
	ExpectEnd time.Time
}

// Live reads the accounts and the running lanes from the lanes input.
func (in LaneInput) Live() ([]LiveAccount, []LiveLane) {
	p, now := in.Params, in.Now
	var accs []LiveAccount
	f5 := map[string]float64{}
	for _, a := range in.Accounts {
		c := cleanLaneSample(a.Sample, now)
		v := laneLive(a, c, now, p, in.Leases)
		pa := p.account(a)
		la := LiveAccount{Key: a.Key, Protected: a.Protected, FiveHour: v.hM, Weekly: v.wM, FiveReset: c.fiveRst,
			Ratio: pa.ratio(), F5: pa.F5, Stale: v.stale || len(v.unknown) > 0}
		if !la.FiveReset.After(now) {
			la.FiveReset, la.FiveHour = time.Time{}, 0
		}
		la.WeeklyReset = now.Add(time.Duration(v.hrs * float64(time.Hour)))
		w0, h0, _ := c.effective(now)
		la.WeeklyRead, la.WeeklyMeter = w0, v.wM-w0
		if !a.Meter.KeepH {
			h0 = 0 // as laneLive: the meter alone
		}
		if !la.FiveReset.IsZero() {
			la.FiveRead, la.FiveMeter, la.FiveLeases = h0, v.hM-h0, math.Max(0, v.h-v.hM)
		}
		if !c.noTS {
			la.ReadingAt = c.ts
		}
		accs = append(accs, la)
		f5[a.Key] = pa.F5
	}
	var lanes []LiveLane
	bw := p.BurnWindow.Hours()
	for _, L := range in.Leases {
		if L.SID == "" || L.Ended || !L.Launched || L.Remote {
			continue
		}
		r := 0.0
		if bw > 0 {
			r = math.Max(0, L.Recent) * f5[L.Key] / bw
		}
		lanes = append(lanes, LiveLane{Key: L.Key, SID: L.SID, T0: L.T0, Rate: r, Measured: L.RecentOK, Expect: L.Expect, ExpectEnd: L.ExpectEnd})
	}
	return accs, lanes
}
