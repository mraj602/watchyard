package picker

// Owner-style stories: the worker's real pattern is many small, short agents (well under 4-5% of
// the week or the 5 hours each) plus some long ones. Each story is checked against every
// invariant (I1-I10) and its own expectation.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var smallStats = LaneStats{Cost: 2, Mean: 1.2, Len: 10 * time.Minute, N: 60, NLen: 60}

// tiny adds n small measured lanes on key, ageMin apart from 1 minute, burning ptsPerH each.
func tiny(key string, n int, ptsPerH float64) []LaneLease {
	var out []LaneLease
	for i := 0; i < n; i++ {
		out = append(out, seenLease(key, 6+float64(i%8), ptsPerH))
	}
	return out
}

func ended(key string, n int, ageMin float64) []LaneLease {
	var out []LaneLease
	for i := 0; i < n; i++ {
		out = append(out, endedLease(key, ageMin))
	}
	return out
}

func TestDrainOwnerStories(t *testing.T) {
	type story struct {
		name  string
		build func() LaneInput
		want  func(r LaneResult) string // "" when as expected
	}
	all := func(key string) func(r LaneResult) string {
		return func(r LaneResult) string {
			if countOf(r, key) != len(r.Picks) || r.Waiting != 0 {
				return fmt.Sprintf("want every lane on %s: %v waiting %d", key, picksOf(r), r.Waiting)
			}
			return ""
		}
	}
	noProt := func(r LaneResult) string {
		if protPicks(r) != 0 {
			return fmt.Sprintf("protected used: %v", picksOf(r))
		}
		return ""
	}
	both := func(fs ...func(LaneResult) string) func(LaneResult) string {
		return func(r LaneResult) string {
			for _, f := range fs {
				if s := f(r); s != "" {
					return s
				}
			}
			return ""
		}
	}
	atLeast := func(key string, n int) func(LaneResult) string {
		return func(r LaneResult) string {
			if countOf(r, key) < n {
				return fmt.Sprintf("want at least %d on %s: %v", n, key, picksOf(r))
			}
			return ""
		}
	}
	none := func(key string) func(LaneResult) string {
		return func(r LaneResult) string {
			if countOf(r, key) != 0 {
				return fmt.Sprintf("want none on %s: %v", key, picksOf(r))
			}
			return ""
		}
	}
	world := func(n int, mod func(map[string]LaneSample)) LaneInput {
		s := dworld()
		mod(s)
		in := laneIn(s, n)
		in.Stats = smallStats
		return in
	}
	stories := []story{
		{"11-23: claude-2 86% at 19:03, 9 h left, 32% 5h, three small lanes just ended: all six to claude-2", func() LaneInput {
			in := world(6, func(s map[string]LaneSample) { s[dX] = dsmp(86, 32, 9, 3.4); s[dP] = dsmp(76, 0, 29, 3) })
			in.Leases = ended(dX, 3, 17)
			return in
		}, both(all(dX), noProt)},
		{"11-23 with fallback statistics", func() LaneInput {
			in := world(6, func(s map[string]LaneSample) { s[dX] = dsmp(86, 32, 9, 3.4) })
			in.Stats = LaneStats{}
			in.Leases = ended(dX, 3, 17)
			return in
		}, both(all(dX), noProt)},
		{"11-23 at 18:46: claude-2 82%, batch of 3", func() LaneInput {
			return world(3, func(s map[string]LaneSample) { s[dX] = dsmp(82, 20, 9.2, 3.7) })
		}, all(dX)},
		{"twenty tiny lanes running, room left: still takes lanes", func() LaneInput {
			in := world(6, func(s map[string]LaneSample) { s[dX] = dsmp(85, 40, 8, 3) })
			in.Leases = tiny(dX, 20, 6)
			return in
		}, all(dX)},
		{"forty tiny lanes running burn the 5h room: X stops, nothing protected", func() LaneInput {
			in := world(6, func(s map[string]LaneSample) { s[dX] = dsmp(85, 80, 8, 3) })
			in.Leases = tiny(dX, 40, 9)
			return in
		}, both(none(dX), noProt)},
		{"tiny lanes, 2% weekly left: a handful, not ten", func() LaneInput {
			return world(10, func(s map[string]LaneSample) { s[dX] = dsmp(96, 10, 3, 3) })
		}, func(r LaneResult) string {
			if n := countOf(r, dX); n == 0 || n > 6 || protPicks(r) != 0 {
				return fmt.Sprintf("picks %v", picksOf(r))
			}
			return ""
		}},
		{"fifty small lanes in a burst over an hour: no burst cap on the draining account", func() LaneInput {
			in := world(10, func(s map[string]LaneSample) { s[dX] = dsmp(70, 10, 20, 3) })
			in.Leases = ended(dX, 50, 30)
			return in
		}, all(dX)},
		{"one long lane (3 h, 6 pts/h) and small ones: room left for more", func() LaneInput {
			in := world(4, func(s map[string]LaneSample) { s[dX] = dsmp(88, 30, 8, 3) })
			in.Leases = append(tiny(dX, 5, 6), seenLease(dX, 180, 6))
			return in
		}, all(dX)},
		{"three long lanes eat the week's room: X full, the others aggressive", func() LaneInput {
			in := world(3, func(s map[string]LaneSample) { s[dX] = dsmp(95, 30, 8, 3) })
			in.Leases = []LaneLease{seenLease(dX, 200, 10), seenLease(dX, 200, 10), seenLease(dX, 200, 10)}
			return in
		}, both(none(dX), noProt)},
		{"two accounts resetting tonight: both get lanes", func() LaneInput {
			return world(6, func(s map[string]LaneSample) { s[dX] = dsmp(90, 10, 6, 3); s[dY] = dsmp(80, 10, 7, 3) })
		}, both(atLeast(dX, 1), atLeast(dY, 1), noProt)},
		{"three accounts resetting today, batch of 3: one each", func() LaneInput {
			return world(3, func(s map[string]LaneSample) {
				s[dX], s[dY], s[dZ] = dsmp(90, 10, 6, 3), dsmp(80, 10, 7, 3), dsmp(70, 10, 20, 3)
			})
		}, both(atLeast(dX, 1), atLeast(dY, 1), atLeast(dZ, 1))},
		{"night: X resets at 04:00 with 9% left and no demand; one lane comes", func() LaneInput {
			return world(1, func(s map[string]LaneSample) { s[dX] = dsmp(89, 0, 8, 0) })
		}, all(dX)},
		{"X's 5h window resets in 5 minutes at 96%: drains now", func() LaneInput {
			return world(3, func(s map[string]LaneSample) { s[dX] = dsmp(80, 96, 6, 5.0/60) })
		}, all(dX)},
		{"X's 5h at 96% for two more hours: others take the lanes, not protected", func() LaneInput {
			return world(3, func(s map[string]LaneSample) { s[dX] = dsmp(80, 96, 6, 2) })
		}, both(none(dX), noProt)},
		{"all aggressive burst-capped by small lanes: wait, never protected", func() LaneInput {
			in := world(2, func(s map[string]LaneSample) {})
			for _, a := range aggrNames {
				in.Leases = append(in.Leases, ended(a, 6, 20)...)
			}
			return in
		}, func(r LaneResult) string {
			if len(r.Picks) != 0 || r.Waiting != 2 {
				return fmt.Sprintf("picks %v waiting %d", picksOf(r), r.Waiting)
			}
			return ""
		}},
		{"all aggressive at 98%: protected may take one", func() LaneInput {
			return world(1, func(s map[string]LaneSample) {
				for _, a := range aggrNames {
					s[a] = dsmp(98.4, 10, 100, 3)
				}
			})
		}, func(r LaneResult) string {
			if len(r.Picks) != 1 || !isProt(r.Picks[0].Key) {
				return fmt.Sprintf("picks %v", picksOf(r))
			}
			return ""
		}},
		{"protected claude-5 at 40% resetting in 3 h (even full speed wastes it): drains after X", func() LaneInput {
			return world(6, func(s map[string]LaneSample) { s[dX] = dsmp(96.5, 10, 2, 3); s[dP] = dsmp(40, 10, 3, 3) })
		}, func(r LaneResult) string {
			if r.Picks[0].Key != dX || countOf(r, dP) == 0 {
				return fmt.Sprintf("picks %v", picksOf(r))
			}
			return ""
		}},
		{"protected claude-5 at 77%, 28 h left: kept", func() LaneInput {
			return world(6, func(s map[string]LaneSample) { s[dP] = dsmp(77, 10, 28, 3) })
		}, none(dP)},
		{"an account used only by claude -p, stale: one drain lane", func() LaneInput {
			in := world(4, func(s map[string]LaneSample) { x := dsmp(85, 10, 8, 3); x.TS = hrs(-2); s[dX] = x })
			in.Accounts[1].Live = true
			return in
		}, func(r LaneResult) string {
			if countOf(r, dX) != 1 {
				return fmt.Sprintf("picks %v", picksOf(r))
			}
			return ""
		}},
		{"hub down while draining: about half the lanes", func() LaneInput {
			in := world(10, func(s map[string]LaneSample) { s[dX] = dsmp(94, 10, 6, 3) })
			in.HubDown = true
			return in
		}, func(r LaneResult) string {
			full := world(10, func(s map[string]LaneSample) { s[dX] = dsmp(94, 10, 6, 3) })
			fr, _ := RankLanes(full)
			c := rowOf(fr, dX).Drain.Capacity
			if n := countOf(r, dX); n > (c+1)/2 || n == 0 {
				return fmt.Sprintf("%d lanes with the hub down, capacity %d", n, c)
			}
			return ""
		}},
		{"Opus lanes: fewer per room", func() LaneInput {
			in := world(10, func(s map[string]LaneSample) { s[dX] = dsmp(94, 10, 6, 3) })
			in.Stats = LaneStats{Cost: 16, Mean: 12, Len: 40 * time.Minute, Model: "opus"}
			return in
		}, func(r LaneResult) string {
			if n := countOf(r, dX); n > 3 || n == 0 {
				return fmt.Sprintf("%d Opus lanes on 3%% room", n)
			}
			return ""
		}},
		{"a 3-hour agent 40 minutes before the reset: a spill lane, at most two", func() LaneInput {
			in := world(4, func(s map[string]LaneSample) { s[dX] = dsmp(80, 10, 40.0/60, 3) })
			in.Expect = 3 * time.Hour
			return in
		}, func(r LaneResult) string {
			if countOf(r, dX) != 2 {
				return fmt.Sprintf("picks %v", picksOf(r))
			}
			return ""
		}},
		{"owner reset X to 0 mid-week, 50 h left: drain", func() LaneInput {
			return world(6, func(s map[string]LaneSample) { s[dX] = dsmp(0, 0, 50, 0) })
		}, all(dX)},
		{"X 99% weekly: nothing on X", func() LaneInput {
			return world(3, func(s map[string]LaneSample) { s[dX] = dsmp(99, 0, 5, 3) })
		}, both(none(dX), noProt)},
		{"a quiet week: everyone at 20% with days left, turns among aggressive", func() LaneInput {
			return world(4, func(s map[string]LaneSample) {})
		}, func(r LaneResult) string {
			seen := map[string]bool{}
			for _, k := range picksOf(r) {
				seen[k] = true
			}
			if len(seen) != 4 || protPicks(r) != 0 {
				return fmt.Sprintf("picks %v", picksOf(r))
			}
			return ""
		}},
		{"an unknown aggressive account ranks before protected", func() LaneInput {
			return world(1, func(s map[string]LaneSample) {
				for _, a := range aggrNames {
					s[a] = dsmp(98.5, 10, 100, 3)
				}
				x := dsmp(0, 0, 100, 3)
				x.Weekly = nil
				s[dW] = x
			})
		}, all(dW)},
		{"--urgent with X draining and the others burst-capped: X first, no urgent needed", func() LaneInput {
			in := world(3, func(s map[string]LaneSample) { s[dX] = dsmp(90, 10, 6, 3) })
			in.Urgent = true
			for _, a := range []string{dY, dZ, dW} {
				in.Leases = append(in.Leases, ended(a, 6, 20)...)
			}
			return in
		}, all(dX)},
		{"--urgent when every aggressive is only burst-capped: one past its cap, never protected", func() LaneInput {
			in := world(1, func(s map[string]LaneSample) {})
			in.Urgent = true
			for _, a := range aggrNames {
				in.Leases = append(in.Leases, ended(a, 6, 20)...)
			}
			return in
		}, func(r LaneResult) string {
			if len(r.Picks) != 1 || isProt(r.Picks[0].Key) || !r.Picks[0].Forced {
				return fmt.Sprintf("picks %+v", r.Picks)
			}
			return ""
		}},
		{"a trickle: every 15 minutes one small lane, X takes each", func() LaneInput {
			in := world(1, func(s map[string]LaneSample) { s[dX] = dsmp(88, 20, 8, 3) })
			in.Leases = append(ended(dX, 8, 40), tiny(dX, 2, 6)...)
			return in
		}, all(dX)},
		{"X resets in 1 h with 30% left (unavoidable waste): X takes the whole batch", func() LaneInput {
			return world(10, func(s map[string]LaneSample) { s[dX] = dsmp(68, 10, 1, 3) })
		}, all(dX)},
		{"X resets in 1 h with 30% left, its 5h at 70%: up to the 5h room, then others", func() LaneInput {
			return world(10, func(s map[string]LaneSample) { s[dX] = dsmp(68, 70, 1, 3) })
		}, both(atLeast(dX, 2), noProt)},
		{"X small room behind a big Y: both get lanes in proportion", func() LaneInput {
			return world(8, func(s map[string]LaneSample) { s[dX] = dsmp(94, 10, 5, 3); s[dY] = dsmp(60, 10, 10, 3) })
		}, both(atLeast(dX, 1), atLeast(dY, 4))},
		{"lanes of yesterday (normal mode, still running slowly) do not block today's drain", func() LaneInput {
			in := world(4, func(s map[string]LaneSample) { s[dX] = dsmp(85, 30, 10, 3) })
			in.Leases = []LaneLease{seenLease(dX, 280, 1), seenLease(dX, 280, 1)}
			return in
		}, all(dX)},
	}
	if len(stories) < 30 {
		t.Fatalf("%d stories", len(stories))
	}
	for i, s := range stories {
		in := s.build()
		r := rank(t, in)
		var probs []string
		if msg := s.want(r); msg != "" {
			probs = append(probs, msg)
		}
		probs = append(probs, checkInvariants(t, in)...)
		if len(probs) > 0 {
			t.Errorf("story %d %q:\n  %s", i+1, s.name, strings.Join(probs, "\n  "))
		}
	}
	t.Logf("%d stories", len(stories))
}
