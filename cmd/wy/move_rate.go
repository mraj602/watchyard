package main

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/mover"
	"github.com/mraj602/watchyard/internal/picker"
)

// Readings are whole percents: a one-point rise between two readings minutes apart reads as
// 10-60 points/h (live run, 1.3.3-pre3). The readings' rate is a slope over at least
// readingSpan, from readings at least readingGap apart, all of the newest reading's window.
const (
	readingSpan     = 15 * time.Minute
	readingLookback = 30 * time.Minute
	readingGap      = 5 * time.Minute
)

// readingSlope is the least-squares slope of account key's 5-hour readings, points per hour:
// the readings of the last max(window, readingLookback) of the newest reading's window (a
// reset in between starts again), thinned to one per readingGap, spanning at least
// max(window, readingSpan). ok is false when there are not enough.
func readingSlope(cands []picker.Candidate, keyOfDir map[string]string, key string, now time.Time, window time.Duration) (float64, bool) {
	span := max(window, readingSpan)
	look := max(span, readingLookback) + time.Minute
	for _, c := range cands {
		if keyOfDir[c.Dir] != key {
			continue
		}
		for _, wd := range c.Windows {
			if wd.Key != "five_hour" {
				continue
			}
			ss := append([]picker.Sample(nil), wd.Samples...)
			sort.Slice(ss, func(i, j int) bool { return ss[i].At.After(ss[j].At) })
			var pts []picker.Sample
			for _, x := range ss {
				if now.Sub(x.At) > look || x.At.After(now.Add(time.Minute)) {
					continue
				}
				if len(pts) > 0 && !x.ResetsAt.Equal(pts[0].ResetsAt) {
					break // an older window: across a reset
				}
				if len(pts) > 0 && pts[len(pts)-1].At.Sub(x.At) < readingGap {
					continue
				}
				pts = append(pts, x)
			}
			if len(pts) < 2 || pts[0].At.Sub(pts[len(pts)-1].At) < span {
				return 0, false
			}
			var mx, my float64
			for _, x := range pts {
				mx += now.Sub(x.At).Hours()
				my += x.Pct
			}
			mx, my = mx/float64(len(pts)), my/float64(len(pts))
			var sxy, sxx float64
			for _, x := range pts {
				dx := -now.Sub(x.At).Hours() + mx // time forward
				sxy += dx * (x.Pct - my)
				sxx += dx * dx
			}
			if sxx == 0 {
				return 0, false
			}
			return math.Max(0, sxy/sxx), true
		}
	}
	return 0, false
}

// remoteRate is the burn of account key's sessions on other machines that the hub measured,
// points per hour (remoteUnits × f).
func remoteRate(cands []picker.Candidate, keyOfDir map[string]string, key string, now time.Time, window time.Duration, f float64) float64 {
	if f <= 0 {
		return 0
	}
	return remoteUnits(cands, keyOfDir, key, now, window) * f
}

// remoteUnits is the burn of account key's sessions on other machines that the hub measured
// (their leases' per-minute units in the burn window), units per hour; 0 when none or when
// only the kept copy of an older hub read is there.
func remoteUnits(cands []picker.Candidate, keyOfDir map[string]string, key string, now time.Time, window time.Duration) float64 {
	if window <= 0 {
		return 0
	}
	units := 0.0
	seen := map[string]bool{}
	for _, c := range cands {
		if keyOfDir[c.Dir] != key || c.RemoteCopy {
			continue
		}
		for _, l := range c.RemoteLeases {
			if l.Burn == nil || l.Ended || seen[l.ID] {
				continue
			}
			seen[l.ID] = true
			for _, m := range *l.Burn {
				at := l.At.Add(time.Duration(m[0] * float64(time.Second)))
				if now.Sub(at) <= window && !at.After(now.Add(time.Minute)) && m[1] > 0 && !math.IsInf(m[1], 0) {
					units += m[1]
				}
			}
		}
	}
	return units / window.Hours()
}

// moverView is Smart Account Switch's view for the lanes picker while the switch is on
// (setup.md 8.5, The switch's projection): its caps and each account's measured burn now,
// units per hour: this machine's running sessions' token rows in the burn window and other
// machines' sessions the hub measured. nil while the switch is off.
func moverView(cfg config.Config, launches []mover.Launch, keyOfDir map[string]string, tokens []picker.TokenUse, cands []picker.Candidate, now time.Time) *picker.MoverView {
	if !cfg.Account.SmartSwitch {
		return nil
	}
	p := moveParams(cfg)
	v := &picker.MoverView{Cap5: p.Cap5, Cap7: p.Cap7, Cap5Protected: p.Cap5Protected, Cap7Protected: p.Cap7Protected,
		WeekSpanH: p.WeekSpan.Hours(), Units: map[string]float64{},
		ExpiringH: p.ExpiringRoom.Hours(), DestHorizonH: p.DestHorizon.Hours(), DestFloorH: mover.DestFloor.Hours()}
	keyOf := map[string]string{}
	for _, l := range launches {
		if k := keyOfDir[l.Slot]; k != "" {
			keyOf[l.SessionID] = k
		}
	}
	for sid, b := range burnStats(tokens, now, p.BurnWindow) {
		if k := keyOf[sid]; k != "" {
			v.Units[k] += b.Units / p.BurnWindow.Hours()
		}
	}
	keys := map[string]bool{}
	for _, k := range keyOfDir {
		keys[k] = true
	}
	for k := range keys {
		v.Units[k] += remoteUnits(cands, keyOfDir, k, now, p.BurnWindow)
	}
	return v
}

// topModel is the model a session called most in the burn window, else its --model flag.
func topModel(calls map[string]int, argv []string) string {
	best, n := "", 0
	for m, c := range calls {
		if c > n || (c == n && m < best) {
			best, n = m, c
		}
	}
	if best != "" && best != "?" {
		return best
	}
	for i, a := range argv {
		if a == "--model" && i+1 < len(argv) {
			return argv[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--model="); ok {
			return v
		}
	}
	return ""
}
