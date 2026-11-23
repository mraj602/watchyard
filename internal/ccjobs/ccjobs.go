// Package ccjobs reads, read-only and best-effort, the files Claude Code's per-account daemon
// keeps about background sessions (peers.md 3.5): `<config>/jobs/<short>/state.json`,
// `<config>/jobs/pins.json` and `<config>/daemon.log`. They are Claude Code internals,
// undocumented except the pin feature itself, and may change. Nothing here writes, and a
// file that is absent, unreadable or malformed only leaves its part of the answer empty.
//
// Depends on: nothing in Watchyard (leaf).
package ccjobs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ShortLen is the length of the short id Claude Code names a background job by: the first
// eight characters of its session id.
const ShortLen = 8

// Limits on what is read, so a huge or hostile folder cannot stall a command.
const (
	maxJobs     = 5000    // job folders looked at
	maxStateLen = 1 << 20 // bytes of one state.json
	maxPinsLen  = 1 << 20 // bytes of pins.json
	maxLogTail  = 8 << 20 // bytes read from the end of daemon.log
)

// Retirement is the newest `bg retire` line the daemon logged for one job.
type Retirement struct {
	At     time.Time
	Reason string // as logged: settled, stale-spare, idle-prompt, ...
}

// Account is what one config folder's daemon files say. The zero value means "nothing known".
type Account struct {
	// Jobs maps a session id to the short id of the background job that runs it.
	Jobs map[string]string
	// Pins holds the short ids in pins.json.
	Pins map[string]bool
	// Retired holds, per short id, the newest idle retirement in daemon.log.
	Retired map[string]Retirement
}

// Load reads configDir's job states, pins and daemon log. It never fails: what cannot be read
// is left out.
func Load(configDir string) Account {
	return Account{Jobs: readJobs(configDir), Pins: ReadPins(configDir), Retired: readRetired(configDir)}
}

// Short is the first eight characters of a session id, or "" when it is shorter.
func Short(sessionID string) string {
	if len(sessionID) < ShortLen {
		return ""
	}
	return sessionID[:ShortLen]
}

// Job is the short id of the background job that runs sessionID, or "" when the session is
// not a Claude Code background session of this folder.
func (a Account) Job(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	return a.Jobs[sessionID]
}

// JobOf is Load(configDir).Job(sessionID) without reading the daemon log: it looks at
// jobs/<short>/state.json first and scans the other job folders only when that one is not
// the session's.
func JobOf(configDir, sessionID string) string {
	s := Short(sessionID)
	if s == "" || configDir == "" {
		return ""
	}
	if id, ok := readStateSession(filepath.Join(configDir, "jobs", s, "state.json")); ok && id == sessionID {
		return s
	}
	return readJobs(configDir)[sessionID]
}

// ReadPins reads `<config>/jobs/pins.json`, a JSON array of short ids. Anything else is no pins.
func ReadPins(configDir string) map[string]bool {
	out := map[string]bool{}
	if configDir == "" {
		return out
	}
	b, err := readCapped(filepath.Join(configDir, "jobs", "pins.json"), maxPinsLen)
	if err != nil {
		return out
	}
	var ids []string
	if json.Unmarshal(b, &ids) != nil {
		return out
	}
	for _, id := range ids {
		if id != "" {
			out[id] = true
		}
	}
	return out
}

func readJobs(configDir string) map[string]string {
	out := map[string]string{}
	if configDir == "" {
		return out
	}
	d, err := os.Open(filepath.Join(configDir, "jobs"))
	if err != nil {
		return out
	}
	defer d.Close()
	ents, _ := d.ReadDir(maxJobs)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		id, ok := readStateSession(filepath.Join(configDir, "jobs", e.Name(), "state.json"))
		if ok && id != "" {
			out[id] = e.Name()
		}
	}
	return out
}

// readStateSession returns the sessionId of a job's state.json; ok is false when the file
// cannot be read or parsed.
func readStateSession(path string) (string, bool) {
	b, err := readCapped(path, maxStateLen)
	if err != nil {
		return "", false
	}
	var st struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(b, &st) != nil {
		return "", false
	}
	return st.SessionID, true
}

// retireLine is `[<RFC 3339 ts>] [bg] bg retire <short>: <reason>, idle <age>`.
var retireLine = regexp.MustCompile(`^\[([^\]]+)\] \[bg\] bg retire ([0-9A-Za-z_-]{1,64}): ([a-z-]+),\s*idle\b`)

func readRetired(configDir string) map[string]Retirement {
	out := map[string]Retirement{}
	if configDir == "" {
		return out
	}
	f, err := os.Open(filepath.Join(configDir, "daemon.log"))
	if err != nil {
		return out
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > maxLogTail {
		if _, err := f.Seek(fi.Size()-maxLogTail, io.SeekStart); err != nil {
			return out
		}
	}
	ParseRetired(f, out)
	return out
}

// ParseRetired adds the idle retirements found in a daemon log to into (newest per short id).
func ParseRetired(r io.Reader, into map[string]Retirement) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte("bg retire ")) {
			continue
		}
		m := retireLine.FindSubmatch(line)
		if m == nil {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(m[1])))
		if err != nil {
			continue
		}
		short := string(m[2])
		if cur, ok := into[short]; ok && !at.After(cur.At) {
			continue
		}
		into[short] = Retirement{At: at, Reason: string(m[3])}
	}
}

func readCapped(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, n))
}
