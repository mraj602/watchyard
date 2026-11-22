package picker

// The burned units of leased sessions (setup.md 8.5, effective state 3): each session's use
// comes from one source, this machine's token rows of it or the hub's per-minute burn,
// whichever is larger, and the token meter counts the session from that same source. A
// session's tokens are then in the reading (before it) or in the meter (after it), once, and
// its lease charges only the part of its expected cost not burned yet.

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"time"
)

// Where a lease's burned units came from (LaneLease.From).
const (
	burnLocal = "local"
	burnHub   = "hub"
	burnNone  = "none"
)

// sessionHash is api.yaml UsageLease.session_hash: the SHA-256 of a session id, hex.
func sessionHash(sid string) string {
	sum := sha256.Sum256([]byte(sid))
	return hex.EncodeToString(sum[:])
}

type burnSpan struct{ from, to time.Time }

// burnSources measures leased sessions and remembers which ones the meter must count from
// the hub's burn.
type burnSources struct {
	perSession map[string][]unitAt // this machine's token rows by session
	byHash     map[string]string   // session hash -> a session this machine has
	// hubUnits are the hub burns the meter counts, by account key.
	hubUnits map[string][]unitAt
	drop     map[string][]burnSpan // this machine's rows the hub burn replaces, by session
	counted  map[string]bool       // session hashes already in hubUnits
}

func newBurnSources(perSession map[string][]unitAt, sessions map[string]HookSession) *burnSources {
	b := &burnSources{perSession: perSession, byHash: map[string]string{}, hubUnits: map[string][]unitAt{},
		drop: map[string][]burnSpan{}, counted: map[string]bool{}}
	for sid := range perSession {
		b.byHash[sessionHash(sid)] = sid
	}
	for sid := range sessions {
		b.byHash[sessionHash(sid)] = sid
	}
	return b
}

// local is the session of this machine with that hash ("" when none).
func (b *burnSources) local(hash string) string {
	if hash == "" {
		return ""
	}
	return b.byHash[hash]
}

// burned is one lease's measure: units since the pick and their source; seen when a source
// has a token of the session since the pick; measured when some source can see the session
// at all (a local session, or a hub burn), even with no token yet.
type burned struct {
	units    float64
	from     string
	seen     bool
	measured bool
}

func unitsOK(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) || x < 0 {
		return 0
	}
	return x
}

// measure is the burned units of the lease of key picked at t0, whose session is sid on this
// machine ("" when unknown here) and which the hub measured (hub with Burn; nil: no hub burn).
// When the hub's burn is the larger, the meter counts the session from it from now on.
func (b *burnSources) measure(key string, t0 time.Time, sid string, hub *HubLease) burned {
	out := burned{from: burnNone}
	local := 0.0
	if sid != "" {
		out.measured = true
		for _, u := range b.perSession[sid] {
			if !u.at.Before(t0) {
				local += u.units
				out.seen = true
			}
		}
	}
	if hub != nil && hub.Burn != nil {
		out.measured = true
		total := 0.0
		for _, m := range *hub.Burn {
			total += unitsOK(m[1])
		}
		if total > local {
			h := hub.SessionHash
			if h == "" && sid != "" {
				h = sessionHash(sid)
			}
			if h == "" || !b.counted[h] {
				b.counted[h] = h != ""
				for _, m := range *hub.Burn {
					after := unitsOK(m[0])
					b.hubUnits[key] = append(b.hubUnits[key], unitAt{t0.Add(time.Duration(after * float64(time.Second))), unitsOK(m[1])})
				}
				if sid != "" {
					b.drop[sid] = append(b.drop[sid], burnSpan{t0, t0.Add(leaseLife)})
				}
			}
			out.units, out.from, out.seen = total, burnHub, true
			return out
		}
	}
	if out.seen {
		out.units, out.from = local, burnLocal
	}
	return out
}

// fromHub reports whether this machine's token row of sid at at is counted from a hub burn
// instead.
func (b *burnSources) fromHub(sid string, at time.Time) bool {
	for _, s := range b.drop[sid] {
		if !at.Before(s.from) && at.Before(s.to) {
			return true
		}
	}
	return false
}
