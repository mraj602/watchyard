package picker

import (
	"strings"
	"time"
)

// Slot is one config folder on this machine and its login, as `wy doctor` reads it (the
// state file's oauthAccount through the identity cache; claude is not run).
type Slot struct {
	Folder   string
	Dir      string
	Default  bool
	Label    string
	LoggedIn bool
	Account  string
	Org      string
	Email    string
}

// Sources is the usage data to join to the slots.
type Sources struct {
	Machine string   // this machine's id (hub places and signed_out name it)
	Hub     *HubData // nil: no hub data
	Local   []LocalSample
}

// Candidates joins each slot to its hub seat and usage. A slot is matched to a hub seat by
// its login, never by label (labels differ between machines): the seat with the same
// account and org uuids; else the seat that lists this folder on this machine as a place;
// else the only seat with the same email. Readings the status line wrote on this machine
// that are newer than the hub's are used instead of the hub's.
func Candidates(slots []Slot, src Sources) []Candidate {
	out := make([]Candidate, 0, len(slots))
	for _, s := range slots {
		c := Candidate{Folder: s.Folder, Dir: s.Dir, Default: s.Default, Label: s.Label, LoggedIn: s.LoggedIn,
			Account: s.Account, Org: s.Org, Email: s.Email, Source: "none"}
		if src.Hub != nil {
			if seat, by := matchSeat(s, src.Machine, src.Hub.Seats); seat != nil {
				c.SeatID, c.MatchedBy, c.LiveSessions, c.Source = seat.ID, by, seat.LiveSessions, "hub"
				if seat.Label != nil {
					c.HubLabel = *seat.Label
				}
				if seat.Mode != nil && ValidSeatMode(*seat.Mode) {
					c.HubMode = *seat.Mode
				}
				if seat.DrainUntil != nil {
					c.HubDrainUntil = *seat.DrainUntil
				}
				if c.Email == "" && seat.Email != nil {
					c.Email = *seat.Email
				}
				if c.Account == "" {
					c.Account, c.Org = seat.Account, seat.Org
				}
				c.Windows = hubWindows(*seat, src.Hub.Samples[seat.ID])
				c.RemoteLeases = src.Hub.Leases[seat.ID]
				c.OwnLeases = src.Hub.OwnLeases[seat.ID]
				if f, ok := src.Hub.Calib[seat.ID]; ok {
					c.Calib = &f
				}
			}
			c.SignedOut = signedOut(s.Folder, src.Machine, c.SeatID, src.Hub.Seats)
		}
		overlayLocal(&c, src.Local)
		out = append(out, c)
	}
	return out
}

func matchSeat(s Slot, machine string, seats []HubSeat) (*HubSeat, string) {
	if s.Account != "" {
		for i := range seats {
			if seats[i].Account == s.Account && (s.Org == "" || seats[i].Org == s.Org) {
				return &seats[i], "account"
			}
		}
	}
	if machine != "" {
		for i := range seats {
			for _, p := range seats[i].Places {
				if p.Machine == machine && p.Slot != nil && *p.Slot == s.Folder {
					return &seats[i], "place"
				}
			}
		}
	}
	if s.Email != "" {
		var hit *HubSeat
		for i := range seats {
			if seats[i].Email != nil && strings.EqualFold(*seats[i].Email, s.Email) && (s.Org == "" || seats[i].Org == s.Org) {
				if hit != nil {
					return nil, "" // two seats with that email: do not guess
				}
				hit = &seats[i]
			}
		}
		if hit != nil {
			return hit, "email"
		}
	}
	return nil, ""
}

// signedOut is the time the hub saw this folder on this machine signed out, unless the
// matched seat lists the folder as a place again.
func signedOut(folder, machine, seatID string, seats []HubSeat) *time.Time {
	if machine == "" {
		return nil
	}
	var since *time.Time
	for _, se := range seats {
		if se.ID == seatID {
			for _, p := range se.Places {
				if p.Machine == machine && p.Slot != nil && *p.Slot == folder {
					return nil
				}
			}
		}
		for _, so := range se.SignedOut {
			if so.Machine == machine && so.Slot == folder && (since == nil || so.Since.After(*since)) {
				t := so.Since
				since = &t
			}
		}
	}
	return since
}

func hubWindows(seat HubSeat, samples []HubSample) []Window {
	var ws []Window
	for _, hw := range seat.Windows {
		if !limitKey(hw.Key) {
			continue
		}
		w := Window{Key: hw.Key, Idle: hw.State == "idle", Source: "hub"}
		if hw.UsedPct != nil {
			w.UsedPct = *hw.UsedPct
		}
		if hw.ResetsAt != nil {
			w.ResetsAt = *hw.ResetsAt
		}
		if hw.UpdatedAt != nil {
			w.UpdatedAt = *hw.UpdatedAt
		}
		for _, s := range samples {
			if s.Key == hw.Key && s.UsedPct != nil {
				x := Sample{At: s.TS, Pct: *s.UsedPct, Source: s.Source}
				if s.ResetsAt != nil {
					x.ResetsAt = *s.ResetsAt
				}
				w.Samples = append(w.Samples, x)
			}
		}
		ws = append(ws, w)
	}
	return ws
}

// overlayLocal uses this machine's own status-line readings of the candidate's seat (or of
// its folder when the seat is unknown) where they are newer than the hub's.
func overlayLocal(c *Candidate, local []LocalSample) {
	mine := func(s LocalSample) bool { return localOf(*c, s) }
	used := false
	for _, k := range []string{"five_hour", "seven_day"} {
		var newest *LocalSample
		var hist []Sample
		for i := range local {
			s := local[i]
			if !mine(s) {
				continue
			}
			pct, reset := pick(s, k)
			if pct == nil {
				continue
			}
			x := Sample{At: s.At, Pct: *pct, Source: "local"}
			if reset != nil {
				x.ResetsAt = *reset
			}
			hist = append(hist, x)
			if newest == nil || s.At.After(newest.At) {
				newest = &local[i]
			}
		}
		if newest == nil {
			continue
		}
		idx := -1
		for i := range c.Windows {
			if c.Windows[i].Key == k {
				idx = i
			}
		}
		if idx < 0 {
			c.Windows = append(c.Windows, Window{Key: k, Source: "local"})
			idx = len(c.Windows) - 1
		}
		w := &c.Windows[idx]
		var hubNewest time.Time
		for _, s := range w.Samples {
			if s.At.After(hubNewest) {
				hubNewest = s.At
			}
		}
		if w.UpdatedAt.After(hubNewest) {
			hubNewest = w.UpdatedAt
		}
		for _, h := range hist {
			if h.At.After(hubNewest) {
				w.Samples = append(w.Samples, h)
			}
		}
		if newest.At.After(w.UpdatedAt) {
			pct, reset := pick(*newest, k)
			w.UsedPct, w.UpdatedAt, w.Idle, w.Source = *pct, newest.At, false, "local"
			w.ResetsAt = time.Time{}
			if reset != nil {
				w.ResetsAt = *reset
			}
			used = true
		}
	}
	if used {
		if c.Source == "hub" {
			c.Source = "hub+local"
		} else {
			c.Source = "local"
		}
	}
}

// localOf reports whether a status-line reading of this machine is of the candidate's seat
// (or of its folder when either seat is unknown).
func localOf(c Candidate, s LocalSample) bool {
	if c.Account != "" && s.Account != "" {
		return s.Account == c.Account && (c.Org == "" || s.Org == "" || s.Org == c.Org)
	}
	return s.Slot == c.Folder
}

func pick(s LocalSample, key string) (*float64, *time.Time) {
	if key == "five_hour" {
		return s.Five, s.FiveReset
	}
	return s.Seven, s.SevenReset
}

// WantSeat returns a filter for HubClient.Fetch: the seats some slot matches, so history is
// fetched only for accounts this machine can start.
func WantSeat(slots []Slot, machine string) func(HubSeat) bool {
	return func(seat HubSeat) bool {
		for _, s := range slots {
			if m, _ := matchSeat(s, machine, []HubSeat{seat}); m != nil {
				return true
			}
		}
		return false
	}
}
