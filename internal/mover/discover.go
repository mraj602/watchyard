package mover

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/peers"
)

// Sys is how the mover sees this machine's sessions: Claude Code's session registry in every
// config folder, the process table (no environment: keys.md 8), and tmux's panes. Tests
// fill it with fakes.
type Sys struct {
	FS   identity.FS
	Proc peers.Proc
	// Cmdline is a process's argv (nil: unreadable).
	Cmdline func(pid int) []string
	// Panes maps each tmux pane's own process to the pane id (%N); nil or empty: no tmux.
	Panes func() map[int]string
	State State
}

// Sessions lists the live sessions of the slots with their launch specs. An entry whose
// process is gone or is another process now (its start time differs) is left out.
func (y Sys) Sessions(slots []identity.Slot) []Launch {
	var panes map[int]string
	if y.Panes != nil {
		panes = y.Panes()
	}
	var out []Launch
	seen := map[string]bool{}
	for _, slot := range slots {
		for _, e := range peers.ReadRegistry(y.FS, slot) {
			if e.PID <= 0 || seen[e.SessionID] {
				continue
			}
			live, err := y.Proc.StartTime(e.PID)
			if err != nil || !peers.ProcStartMatches(e.ProcStartString(), live) {
				continue
			}
			d := DetectInput{PID: e.PID, ProcStart: live, SessionID: e.SessionID, Slot: slot.Dir, CWD: e.CWD, Name: e.Name,
				RegKind: e.Kind, Entrypoint: e.Entrypoint, Bridge: e.Bridge != "", Status: e.Status}
			if e.StatusUpdatedAt > 0 {
				d.StatusSince = time.UnixMilli(e.StatusUpdatedAt)
			}
			if y.Cmdline != nil {
				d.Cmdline = y.Cmdline(e.PID)
			}
			d.Pane = registryPane(e.Tmux)
			anc := peers.Ancestors(y.Proc, e.PID)
			for i, a := range anc {
				if p, ok := panes[a.PID]; ok {
					if d.Pane == "" {
						d.Pane = p
					}
					d.PaneRoot = i == 0
					break
				}
			}
			// A wrapper within a few levels (claude-launch scripts may sit between them).
			for i := 1; i < len(anc) && i <= 4; i++ {
				if _, ok := y.State.Run(anc[i].PID); ok {
					d.Wrapper = anc[i].PID
					break
				}
			}
			seen[e.SessionID] = true
			out = append(out, Detect(d))
		}
	}
	return out
}

// registryPane reads the pane id from the registry's tmux field when Claude Code wrote one:
// a string "%N", or an object with a pane id.
func registryPane(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.HasPrefix(s, "%") {
			return s
		}
		return ""
	}
	var o map[string]any
	if json.Unmarshal(raw, &o) == nil {
		for _, k := range []string{"pane", "paneId", "pane_id", "TMUX_PANE"} {
			if v, ok := o[k].(string); ok && strings.HasPrefix(v, "%") {
				return v
			}
		}
	}
	return ""
}
