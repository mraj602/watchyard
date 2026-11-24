package mover

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Claude Code keeps its own pins per config folder: <dir>/jobs/pins.json, a JSON array of
// short session ids (the first 8 characters). A session listed there is pinned to that
// account as much as one pinned with `wy move --pin-account` (setup.md 8.6, Pinned
// sessions). Watchyard only reads the file; it never writes it.

// NativePinsFile is the path of the Claude Code pins of config folder dir.
func NativePinsFile(dir string) string { return filepath.Join(dir, "jobs", "pins.json") }

// ParseNativePins reads the ids of a Claude Code pins file: a JSON array of strings (empty
// and repeated ones dropped).
func ParseNativePins(b []byte) ([]string, error) {
	var raw []string
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range raw {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// NativePinned says whether session id is in pins: an entry equal to the id or a prefix of it.
func NativePinned(pins []string, id string) bool {
	for _, p := range pins {
		if p != "" && strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

type nativeEntry struct {
	mod  time.Time
	size int64
	ids  []string
	err  error
}

var nativeCache = struct {
	sync.Mutex
	files map[string]nativeEntry
}{files: map[string]nativeEntry{}}

// NativePins are the Claude Code pins of config folder dir, read again only when the file's
// modification time or size changed. A missing file is no pins; one that cannot be read or
// parsed is no pins too, and warn (when not nil) is told why (the switch's log keeps each
// warning once: State.Changed).
func NativePins(dir string, warn func(string)) []string {
	path := NativePinsFile(dir)
	fi, err := os.Stat(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			nativeWarn("Claude Code pins not read ("+err.Error()+"): taken as none", warn)
		}
		return nil
	}
	nativeCache.Lock()
	e, ok := nativeCache.files[path]
	nativeCache.Unlock()
	if !ok || !e.mod.Equal(fi.ModTime()) || e.size != fi.Size() {
		e = nativeEntry{mod: fi.ModTime(), size: fi.Size()}
		b, rerr := os.ReadFile(path)
		if rerr == nil {
			e.ids, rerr = ParseNativePins(b)
		}
		e.err = rerr
		nativeCache.Lock()
		nativeCache.files[path] = e
		nativeCache.Unlock()
	}
	if e.err != nil {
		nativeWarn("Claude Code pins in "+path+" not understood ("+e.err.Error()+"): taken as none", warn)
		return nil
	}
	return e.ids
}

func nativeWarn(msg string, warn func(string)) {
	if warn != nil {
		warn(msg)
	}
}
