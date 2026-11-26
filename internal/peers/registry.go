package peers

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/mraj602/watchyard/internal/identity"
)

// RegistryEntry is one slot's `<config>/sessions/<pid>.json`, the file Claude Code's own
// /peers reads (peers.md 3.1). Only these fields are decoded.
type RegistryEntry struct {
	PID        int             `json:"pid"`
	SessionID  string          `json:"sessionId"`
	CWD        string          `json:"cwd"`
	ProcStart  json.RawMessage `json:"procStart"`
	Socket     string          `json:"messagingSocketPath"`
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	Kind       string          `json:"kind"`
	PeerProtoc json.RawMessage `json:"peerProtocol"`
	// For Smart Account Switch (setup.md 8.6, Mechanics): how the session was started, whether
	// Remote Control is on (a bridge session), its background job, its tmux pane when Claude
	// Code records it, and when its status last changed (Unix milliseconds).
	Entrypoint      string          `json:"entrypoint"`
	Bridge          string          `json:"bridgeSessionId"`
	JobID           string          `json:"jobId"`
	Tmux            json.RawMessage `json:"tmux"`
	StatusUpdatedAt int64           `json:"statusUpdatedAt"`
	// StartedAt: when the process started (Unix milliseconds).
	StartedAt int64 `json:"startedAt"`

	// Slot is the config folder the entry was read from (not in the file).
	Slot identity.Slot `json:"-"`
}

var registryName = regexp.MustCompile(`^(\d+)\.json$`)

// ProcStartString is procStart as text, whether the file holds a number or a string.
func (e RegistryEntry) ProcStartString() string {
	if len(e.ProcStart) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(e.ProcStart, &s) == nil {
		return s
	}
	return string(e.ProcStart)
}

// ProcStartMatches compares the registry's procStart with the live process's start time.
// The registry's format is not documented (peers.md 3.1; lab E1 setup). This
// accepts a missing procStart and any value that is not a plain decimal number (nothing to
// compare); a decimal value must equal the stat start time. A pid that was reused has a
// different start time, which is what this exists to catch; the socket connect is the other
// guard.
func ProcStartMatches(registry, live string) bool {
	if registry == "" || live == "" {
		return true
	}
	if _, err := strconv.ParseUint(registry, 10, 64); err != nil {
		return true
	}
	return registry == live
}

// ReadRegistry lists the session registry of one slot. It opens only files named
// `<digits>.json` in `<slot>/sessions/`; `*.key` and everything else there is never opened
// (identity.md 4), which the FS enforces too. A torn or unreadable file is skipped.
func ReadRegistry(fsys identity.FS, slot identity.Slot) []RegistryEntry {
	dir := filepath.Join(slot.Dir, "sessions")
	ents, err := fsys.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []RegistryEntry
	for _, d := range ents {
		m := registryName.FindStringSubmatch(d.Name())
		if m == nil || d.IsDir() {
			continue
		}
		e, err := readRegistryFile(fsys, filepath.Join(dir, d.Name()))
		if err != nil {
			continue
		}
		if pid, _ := strconv.Atoi(m[1]); e.PID == 0 {
			e.PID = pid
		}
		e.Slot = slot
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// ReadRegistryPID reads one entry, `<slot>/sessions/<pid>.json`.
func ReadRegistryPID(fsys identity.FS, slot identity.Slot, pid int) (RegistryEntry, error) {
	e, err := readRegistryFile(fsys, filepath.Join(slot.Dir, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return RegistryEntry{}, err
	}
	if e.PID == 0 {
		e.PID = pid
	}
	e.Slot = slot
	return e, nil
}

func readRegistryFile(fsys identity.FS, path string) (RegistryEntry, error) {
	f, err := fsys.Open(path)
	if err != nil {
		return RegistryEntry{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return RegistryEntry{}, err
	}
	var e RegistryEntry
	if err := json.Unmarshal(b, &e); err != nil {
		return RegistryEntry{}, fmt.Errorf("%s: %w", path, err)
	}
	if e.SessionID == "" {
		return RegistryEntry{}, fmt.Errorf("%s: no sessionId", path)
	}
	return e, nil
}

// SocketOf is the entry's inbox socket: its messagingSocketPath, else the default
// <runtime>/cc-socks/<pid>.sock (peers.md 8.1).
func (e RegistryEntry) SocketOf(runtime string) string {
	if e.Socket != "" {
		return e.Socket
	}
	return DefaultSocketPath(runtime, e.PID)
}
