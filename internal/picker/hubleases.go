package picker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
)

// HubLeasesFresh is how long the kept copy of other machines' leases is used.
const HubLeasesFresh = 7 * 24 * time.Hour

// leaseLife is the lease life of setup.md 8.5 (LaneParams.LeaseTTL by default): the kept copy
// holds the leases younger than this, and older ones the hub never saw a token of.
const leaseLife = 5 * time.Hour

// HubLeasesFile is <state>/next/hub_leases.json: the other machines' leases of the last hub
// read that can still charge, by account key (Candidate.Key), without their burn (setup.md
// 8.5, Leases and the ledger).
type HubLeasesFile struct {
	V     int                   `json:"v"`
	At    time.Time             `json:"at"`
	Seats map[string][]HubLease `json:"seats"`
}

// LoadHubLeases reads the kept copy; a missing, unreadable or older than HubLeasesFresh file
// is empty.
func LoadHubLeases(path string, now time.Time) *HubLeasesFile {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f HubLeasesFile
	if json.Unmarshal(b, &f) != nil || f.V != 1 || f.At.IsZero() || now.Sub(f.At) >= HubLeasesFresh {
		return nil
	}
	return &f
}

// SaveHubLeases keeps the other machines' leases of the hub read in cands (the candidates
// matched to a hub seat whose login is known) that can still charge: younger than the lease
// life, or never seen. It replaces the previous copy; nothing is written when no candidate
// has a hub seat.
func SaveHubLeases(path string, cands []Candidate, now time.Time) error {
	f := HubLeasesFile{V: 1, At: now.UTC(), Seats: map[string][]HubLease{}}
	matched := false
	for _, c := range cands {
		if c.SeatID == "" || c.Account == "" || c.RemoteCopy {
			continue
		}
		matched = true
		keep := []HubLease{}
		for _, l := range c.RemoteLeases {
			if now.Sub(l.At) < leaseLife || !l.Seen {
				l.Burn, l.BurnedUnits = nil, 0
				keep = append(keep, l)
			}
		}
		f.Seats[c.Key()] = keep
	}
	if !matched {
		return nil
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'), 0o600)
}

// UseHubLeases gives the candidates the kept copy's leases of their account when the hub was
// not read (the candidates have no hub seat).
func UseHubLeases(cands []Candidate, f *HubLeasesFile) {
	if f == nil {
		return
	}
	for i := range cands {
		c := &cands[i]
		if c.SeatID != "" || c.Account == "" {
			continue
		}
		if ls, ok := f.Seats[c.Key()]; ok {
			c.RemoteLeases, c.RemoteCopy = append([]HubLease(nil), ls...), true
			for j := range c.RemoteLeases {
				c.RemoteLeases[j].Burn = nil
			}
		}
	}
}
