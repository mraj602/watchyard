package picker

import (
	"testing"
	"time"

	"github.com/mraj602/watchyard/internal/config"
)

// setup.md 8.5, Kind: an account that is off is never picked, not even with --urgent or
// when it is the only one with room; --explain says why; the switch sees it as off.
func TestLanesOff(t *testing.T) {
	off := func(in *LaneInput, key string) {
		for i := range in.Accounts {
			if in.Accounts[i].Key == key {
				in.Accounts[i].Off, in.Accounts[i].Protected = true, true
			}
		}
	}
	t.Run("never picked", func(t *testing.T) {
		in := laneIn(dworld(), 6)
		off(&in, dZ)
		r := rank(t, in)
		if countOf(r, dZ) != 0 {
			t.Fatalf("picks %v", picksOf(r))
		}
		if row := rowOf(r, dZ); !row.Skip || row.Reason != OffText || row.ETA != nil {
			t.Fatalf("row %+v", row)
		}
		la, _ := in.Live()
		for _, a := range la {
			if a.Key == dZ && !a.Off {
				t.Errorf("live %+v: want Off", a)
			}
		}
	})
	t.Run("the only one with room, --urgent", func(t *testing.T) {
		s := dworld()
		for k := range s {
			s[k] = dsmp(99, 99, 100, 3)
		}
		s[dZ] = dsmp(10, 5, 100, 3)
		in := laneIn(s, 1)
		in.Urgent = true
		off(&in, dZ)
		if r := rank(t, in); countOf(r, dZ) != 0 {
			t.Fatalf("picks %v", picksOf(r))
		}
	})
	t.Run("a drain on an account that is off does nothing", func(t *testing.T) {
		in := laneIn(dworld(), 3)
		for i := range in.Accounts {
			if in.Accounts[i].Key == dZ {
				in.Accounts[i].DrainUntil = lnow.Add(5 * time.Hour)
			}
		}
		off(&in, dZ)
		if r := rank(t, in); countOf(r, dZ) != 0 || rowOf(r, dZ).Drain.On {
			t.Fatalf("picks %v drain %+v", picksOf(r), rowOf(r, dZ).Drain)
		}
	})
}

// Modes and drains from the hub (setup.md 8.5, Kind and Owner drain): off and drain_until of
// the seat apply on every machine; this machine's next.accounts.<key> wins.
func TestPolicyHubOffAndDrain(t *testing.T) {
	hubUntil := lnow.Add(4 * time.Hour)
	localUntil := lnow.Add(2 * time.Hour)
	for _, c := range []struct {
		name      string
		hubMode   string
		hubDrain  time.Time
		local     config.NextAccount
		wantOff   bool
		wantDrain time.Time
	}{
		{"hub off", "off", time.Time{}, config.NextAccount{}, true, time.Time{}},
		{"local mode wins over hub off", "off", time.Time{}, config.NextAccount{Mode: "aggressive"}, false, time.Time{}},
		{"local off", "aggressive", time.Time{}, config.NextAccount{Mode: "off"}, true, time.Time{}},
		{"hub drain", "protected", hubUntil, config.NextAccount{}, false, hubUntil},
		{"local drain wins", "protected", hubUntil, config.NextAccount{DrainUntil: localUntil.Format(time.RFC3339)}, false, localUntil},
	} {
		s := Settings{Accounts: map[string]config.NextAccount{}}
		if c.local != (config.NextAccount{}) {
			s.Accounts["claude-4"] = c.local
		}
		p, _ := s.PolicyFor(Candidate{Label: "claude-4", Folder: "~/.claude-4", HubMode: c.hubMode, HubDrainUntil: c.hubDrain})
		if p.Off() != c.wantOff || !p.DrainUntil.Equal(c.wantDrain) {
			t.Errorf("%s: off %v drain %v", c.name, p.Off(), p.DrainUntil)
		}
	}
}

// setup.md 8.6, Stale data: an account is Quiet for the switch when the hub was read and
// nothing ran on it since its reading (no live session, no running lane).
func TestLiveQuiet(t *testing.T) {
	quiet := func(in LaneInput) bool {
		la, _ := in.Live()
		for _, a := range la {
			if a.Key == dZ {
				return a.Quiet
			}
		}
		t.Fatal("no account")
		return false
	}
	set := func(in *LaneInput, f func(*LaneAccount)) {
		for i := range in.Accounts {
			if in.Accounts[i].Key == dZ {
				f(&in.Accounts[i])
			}
		}
	}
	in := laneIn(dworld(), 1)
	set(&in, func(a *LaneAccount) { a.HubRead, a.Live = true, false })
	in.Leases = nil
	if !quiet(in) {
		t.Error("hub read, nothing ran: want quiet")
	}
	set(&in, func(a *LaneAccount) { a.Live = true })
	if quiet(in) {
		t.Error("a live session: not quiet")
	}
	set(&in, func(a *LaneAccount) { a.Live, a.HubRead = false, false })
	if quiet(in) {
		t.Error("the hub not read: not quiet (other machines unknown)")
	}
	set(&in, func(a *LaneAccount) { a.HubRead = true })
	in.Leases = []LaneLease{{Key: dZ, Launched: true, T0: lnow.Add(-time.Hour)}}
	if quiet(in) {
		t.Error("a running lane: not quiet")
	}
}
