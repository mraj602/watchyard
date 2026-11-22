package picker

// The calibration the lanes strategy judges each account on (setup.md 8.5, Calibration): the
// hub's factors when under 7 days old, else the copy kept from the last hub read, else a fit
// of this machine's own data; and, always worked out here, the meter margins.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/calib"
)

// Calibration sources (`--json` lanes.calibration.source).
const (
	CalibHub    = "hub"
	CalibCached = "cached"
	CalibLocal  = "local"
)

// CalibFresh is how long the hub's factors (or the kept copy of them) are used.
const CalibFresh = 7 * 24 * time.Hour

// marginSpan is how far back the meter margins replay the readings.
const marginSpan = 26 * time.Hour

// CalibFile is <state>/next/calibration.json: the hub's factors of each account as last read,
// by account key (Candidate.Key).
type CalibFile struct {
	V     int                      `json:"v"`
	Seats map[string]calib.Factors `json:"seats"`
}

// LoadCalibration reads the kept factors; a missing or unreadable file is empty.
func LoadCalibration(path string) map[string]calib.Factors {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f CalibFile
	if json.Unmarshal(b, &f) != nil || f.V != 1 {
		return nil
	}
	return f.Seats
}

// SaveCalibration keeps the hub's factors of the candidates that have them, with the kept
// factors of other accounts that are still under CalibFresh old. Nothing is written when no
// candidate has hub factors.
func SaveCalibration(path string, old map[string]calib.Factors, cands []Candidate, now time.Time) error {
	seats := map[string]calib.Factors{}
	for k, f := range old {
		if now.Sub(f.At) < CalibFresh {
			seats[k] = f
		}
	}
	changed := false
	for _, c := range cands {
		if c.Calib != nil && c.Account != "" {
			seats[c.Key()] = *c.Calib
			changed = true
		}
	}
	if !changed {
		return nil
	}
	b, err := json.Marshal(CalibFile{V: 1, Seats: seats})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'), 0o600)
}

// CalibView is LaneView.Calibration (`--json`).
type CalibView struct {
	Source                string       `json:"source"`
	At                    time.Time    `json:"at"`
	Since                 *time.Time   `json:"since"`
	ResetReason           *string      `json:"reset_reason"`
	F                     calib.Factor `json:"f"`
	Ratio                 calib.Factor `json:"ratio"`
	LanePrice             calib.Factor `json:"lane_price"`
	LaneCharge            float64      `json:"lane_charge"`
	LaneUnits             *float64     `json:"lane_units"`
	UnexplainedPctPerHour *float64     `json:"unexplained_pct_per_hour"`
	UnexplainedHours      float64      `json:"unexplained_hours"`
	Corrector5h           float64      `json:"corrector_5h"`
	Corrector7d           float64      `json:"corrector_7d"`
	Margin5hPct           float64      `json:"margin_5h_pct"`
	Margin7dPct           float64      `json:"margin_7d_pct"`
	UnexplainedFrom       *string      `json:"unexplained_from"`
}

func newCalibView(src string, f calib.Factors) *CalibView {
	v := &CalibView{Source: src, At: f.At.UTC(), F: f.F, Ratio: f.Ratio, LanePrice: f.LanePrice,
		LaneCharge: f.LaneCharge, LaneUnits: f.LaneUnits, UnexplainedPctPerHour: f.UnexplainedPctPerHour,
		UnexplainedHours: f.UnexplainedHours}
	if f.Since != nil {
		t := f.Since.UTC()
		v.Since = &t
	}
	if f.Reset != nil {
		r := f.Reset.Reason
		v.ResetReason = &r
	}
	return v
}

// usable reports whether factors can be used: under CalibFresh old, not from the future, and
// with values in range (a damaged copy falls through to the next source).
func usable(f *calib.Factors, now time.Time) bool {
	if f == nil || f.At.IsZero() || now.Sub(f.At) >= CalibFresh || f.At.After(now.Add(readingAhead)) {
		return false
	}
	ok := func(x float64) bool { return x > 0 && !math.IsInf(x, 0) && !math.IsNaN(x) }
	return ok(f.F.Value) && ok(f.Ratio.Value) && ok(f.LaneCharge)
}

// localLanes are this machine's ledger leases as calibration lanes: those whose session
// started and that are at least 5 hours old, with their session's units in the 5 hours after
// the pick (setup.md 8.5, lane price).
func localLanes(ls []LaneLease, perSession map[string][]unitAt, now time.Time) []calib.Lane {
	var out []calib.Lane
	for _, l := range ls {
		if l.SID == "" || !l.Launched || now.Sub(l.T0) < calib.LaneSpan {
			continue
		}
		u := 0.0
		for _, x := range perSession[l.SID] {
			if !x.at.Before(l.T0) && x.at.Before(l.T0.Add(calib.LaneSpan)) {
				u += x.units
			}
		}
		out = append(out, calib.Lane{At: l.T0.UnixMilli(), Units: u})
	}
	return out
}

// calibReadings joins an account's five_hour and seven_day readings from since up to now:
// its samples (hub and this machine) and this machine's outbox readings of its seat, so a local
// fit sees the outbox's 7 days even where the hub's history is shorter. The samples of one
// time and source are one reading.
func calibReadings(c Candidate, local []LocalSample, since, now time.Time) []calib.Reading {
	type key struct {
		at  int64
		src string
	}
	idx := map[key]int{}
	var out []calib.Reading
	put := func(key5 bool, at time.Time, src string, pct float64, reset time.Time) {
		if at.Before(since) || at.After(now) {
			return
		}
		kk := key{at.UnixNano(), src}
		i, ok := idx[kk]
		if !ok {
			i = len(out)
			idx[kk] = i
			out = append(out, calib.Reading{At: at, Source: src})
		}
		v := pct
		if key5 {
			out[i].Five, out[i].FiveReset = &v, reset
		} else {
			out[i].Seven, out[i].SevenReset = &v, reset
		}
	}
	for _, k := range []string{"five_hour", "seven_day"} {
		if w := windowOf(c, k); w != nil {
			for _, s := range w.Samples {
				put(k == "five_hour", s.At, s.Source, s.Pct, s.ResetsAt)
			}
		}
	}
	for _, s := range local {
		if !localOf(c, s) {
			continue
		}
		for _, k := range []string{"five_hour", "seven_day"} {
			if pct, reset := pick(s, k); pct != nil {
				var r time.Time
				if reset != nil {
					r = *reset
				}
				put(k == "five_hour", s.At, "local", *pct, r)
			}
		}
	}
	calib.SortReadings(out)
	return out
}

func calibMeter(us []unitAt) *calib.Meter {
	cu := make([]calib.Use, len(us))
	for i, u := range us {
		cu[i] = calib.Use{At: u.at, Units: u.units}
	}
	return calib.NewMeter(cu)
}

// localFit calibrates an account from this machine's data, by the hub's rules.
func localFit(c Candidate, local []LocalSample, us []unitAt, pool calib.Pool, now time.Time) calib.Factors {
	s := calib.NewState()
	m := calibMeter(us)
	s.AddReadings(calibReadings(c, local, now.Add(-calib.PairWindow), now), m)
	s.Prune(now)
	return s.Factors(now, pool)
}

// accountCalib is the calibration of one account: its factors, their source, and the meter
// margin factors.
func accountCalib(c Candidate, cached map[string]calib.Factors, local []LocalSample, us []unitAt, pool calib.Pool, now time.Time) (calib.Factors, string, float64, float64) {
	var f calib.Factors
	var src string
	switch {
	case usable(c.Calib, now):
		f, src = *c.Calib, CalibHub
	default:
		if k, ok := cached[c.Key()]; ok && c.Account != "" && usable(&k, now) {
			f, src = k, CalibCached
		} else {
			f, src = localFit(c, local, us, pool, now), CalibLocal
		}
	}
	k5, k7 := calib.Margins(calibReadings(c, local, now.Add(-marginSpan), now), calibMeter(us), f.F.Value, f.Ratio.Value)
	return f, src, k5, k7
}
