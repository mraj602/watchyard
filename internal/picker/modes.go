package picker

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"

	"github.com/mraj602/watchyard/internal/atomicfile"
)

// ModesFile is <state>/next/modes.json: each seat's hub mode as last read, by account key
// (Candidate.Key, "acct:<account>/<org>"); "" is a seat with no mode. It is a setting, so it
// is used with no age limit when the hub cannot be read (setup.md 8.5, Kind).
type ModesFile struct {
	V     int               `json:"v"`
	Seats map[string]string `json:"seats"`
}

// LoadHubModes reads the kept hub modes; a missing or unreadable file is empty.
func LoadHubModes(path string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f ModesFile
	if json.Unmarshal(b, &f) != nil || f.V != 1 {
		return nil
	}
	return f.Seats
}

// SaveHubModes keeps the hub modes of the candidates matched to a hub seat over the kept
// ones; nothing is written when that changes nothing.
func SaveHubModes(path string, old map[string]string, cands []Candidate) error {
	seats := maps.Clone(old)
	if seats == nil {
		seats = map[string]string{}
	}
	for _, c := range cands {
		if c.SeatID != "" && c.Account != "" && !c.HubModeCached {
			seats[c.Key()] = c.HubMode
		}
	}
	if maps.Equal(seats, old) && old != nil {
		return nil
	}
	b, err := json.Marshal(ModesFile{V: 1, Seats: seats})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'), 0o600)
}

// UseHubModes gives candidates the kept hub mode of their account when the hub was not read.
func UseHubModes(cands []Candidate, kept map[string]string) {
	for i := range cands {
		c := &cands[i]
		if c.HubMode != "" || c.Account == "" {
			continue
		}
		if m := kept[c.Key()]; ValidHubMode(m) {
			c.HubMode, c.HubModeCached = m, true
		}
	}
}
