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
	// DrainUntil: the owner drains it until then (zero: off; setup.md 8.5, Owner drain);
	// Protected is then false (an aggressive drain account meanwhile).
	DrainUntil time.Time
	// Ign5h: drain_ignore_5h (with DrainUntil on): its 5-hour window is ignored.
	Ign5h bool
	// Off: the owner keeps it out (setup.md 8.5, Kind): the switch moves nothing in or out.
	Off bool
	// Quiet: the hub was read and nothing ran on it since its reading (no live session on any
	// machine, no running lane, no tokens since): its levels can only have held or fallen, so the
	// switch may use an old reading of it (setup.md 8.6, Stale data).
	Quiet bool
	// PinReserve5 and PinReserve7: the points kept free for its live pinned sessions, scaled to
	// the time left in each window (LaneAccount.PinReserve5/7; 0: no pins).
	PinReserve5, PinReserve7 float64
	// SetCap5 and SetCap7: its own cap_5h and cap_7d when set (LaneAccount.SetCap5/7).
	SetCap5, SetCap7 float64
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
	for _, a := range p.ownerDrainEnds(in.Accounts, in.Mover, now) {
		c := cleanLaneSample(a.Sample, now)
		v := laneLive(a, c, now, p, in.Leases)
		pa := p.account(a)
		la := LiveAccount{Key: a.Key, Protected: a.Protected, FiveHour: v.hM, Weekly: v.wM, FiveReset: c.fiveRst,
			Ratio: pa.ratio(), F5: pa.F5, Stale: v.stale || len(v.unknown) > 0, DrainUntil: a.DrainUntil, Ign5h: a.Ign5h, Off: a.Off,
			PinReserve5: a.PinReserve5, PinReserve7: a.PinReserve7, SetCap5: a.SetCap5, SetCap7: a.SetCap7}
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
		la.Quiet = a.HubRead && !a.Live && !c.noTS && !running(in.Leases, a.Key)
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

// running reports whether a lane of the account runs (launched, not ended).
func running(ls []LaneLease, key string) bool {
	for _, L := range ls {
		if L.Key == key && L.Launched && !L.Ended {
			return true
		}
	}
	return false
}
