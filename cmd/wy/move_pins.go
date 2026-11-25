package main

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/mover"
	peerspkg "github.com/mraj602/watchyard/internal/peers"
	"github.com/mraj602/watchyard/internal/picker"
)

// pinSet are the owner's pins (setup.md 8.6, Pinned sessions): the ones Claude Code keeps
// itself, the short ids in each config folder's jobs/pins.json (read only, mtime-cached). They
// are the only pins: a pin counts only for the folder whose list has it.
type pinSet struct {
	native map[string][]string // config folder -> Claude Code's pinned short ids
}

// loadPins reads the Claude Code pins of every folder in dirs; warn gets a pins file that
// cannot be read or parsed.
func loadPins(dirs []string, warn func(string)) pinSet {
	ps := pinSet{native: map[string][]string{}}
	for _, d := range dirs {
		if ids := mover.NativePins(d, warn); len(ids) > 0 {
			ps.native[d] = ids
		}
	}
	return ps
}

// pinned says whether Claude Code pins session l to its folder.
func (p pinSet) pinned(l mover.Launch) bool {
	return mover.NativePinned(p.native[l.Slot], l.SessionID)
}

// ignoredPins is the note for wy's old pins file (<state>/move/pins.json, from the retired
// `wy move --pin-account`): it is left in place and not read ("" when it lists nothing).
func ignoredPins(st mover.State) string {
	if n := len(st.Pins()); n > 0 {
		return fmt.Sprintf("%d session(s) in %s (the retired wy move --pin-account) are not pinned: pins come from Claude Code only (jobs/pins.json)",
			n, filepath.Join(st.Dir, "pins.json"))
	}
	return ""
}

// livePins are this machine's live pinned sessions by account key (setup.md 8.6, Pinned
// sessions): pinned in Claude Code (a session only outside account.smart_switch_scope holds
// none), each counted once, running, and not idle for mover.PinIdle.
func livePins(launches []mover.Launch, keyOfDir map[string]string, pins pinSet, now time.Time) map[string][]string {
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, l := range launches {
		key := keyOfDir[l.Slot]
		if key == "" || seen[l.SessionID] || !pins.pinned(l) {
			continue
		}
		if l.Status != "busy" && !l.StatusSince.IsZero() && now.Sub(l.StatusSince) >= mover.PinIdle {
			continue // idle for 6 hours: its reserve is released
		}
		seen[l.SessionID] = true
		out[key] = append(out[key], l.SessionID)
	}
	for _, ids := range out {
		sort.Strings(ids)
	}
	return out
}

// pinReserves are the picker's pin reserves from the live pins and the account.pin_reserve_*
// settings, with the pins' measured burn (units per hour over the burn window) that the picker
// scales them by.
func pinReserves(p mover.Params, live map[string][]string, tokens []picker.TokenUse, now time.Time) map[string]picker.PinReserve {
	out := map[string]picker.PinReserve{}
	var burn map[string]burnStat
	if len(live) > 0 && p.BurnWindow > 0 {
		burn = burnStats(tokens, now, p.BurnWindow)
	}
	for k, ids := range live {
		r5, r7 := p.PinReserve(len(ids))
		units := 0.0
		for _, id := range ids {
			units += burn[id].Units / p.BurnWindow.Hours()
		}
		out[k] = picker.PinReserve{Pins: len(ids), R5: r5, R7: r7, Units: units}
	}
	return out
}

// nextSwitch is what `wy account next` needs of Smart Account Switch: the pin reserves
// (setup.md 8.6, Pinned sessions) and, while the switch is on, its view for the lanes picker
// (moverView). It reads this machine's sessions (the registry and the process table; no tmux
// or command lines needed), the pins file and, only when a session runs, the peers tags.
func nextSwitch(e nextEnv, cfg config.Config, slots []picker.Slot, cands []picker.Candidate, tokens []picker.TokenUse, now time.Time) (map[string]picker.PinReserve, *picker.MoverView) {
	p := moveParams(cfg)
	if p.PinReserve5 <= 0 && p.PinReserve7 <= 0 && !cfg.Account.SmartSwitch {
		return nil, nil
	}
	var islots []identity.Slot
	keyOfDir := map[string]string{}
	for _, s := range slots {
		islots = append(islots, identity.Slot{Dir: s.Dir, Name: s.Folder, Default: s.Default})
		if s.LoggedIn && s.Account != "" {
			keyOfDir[s.Dir] = "acct:" + s.Account + "/" + s.Org
		}
	}
	st := mover.NewState(e.dirs.State.Path)
	sys := mover.Sys{FS: identity.OSFS{}, Proc: peerspkg.NewProc(), State: st}
	launches := sys.Sessions(islots)
	view := moverView(cfg, launches, keyOfDir, tokens, cands, now)
	known := false
	for _, l := range launches {
		known = known || keyOfDir[l.Slot] != ""
	}
	if !known || (p.PinReserve5 <= 0 && p.PinReserve7 <= 0) {
		return nil, view
	}
	var dirs []string
	for _, s := range slots {
		dirs = append(dirs, s.Dir)
	}
	pins := loadPins(dirs, func(msg string) { fmt.Fprintln(e.stderr, "wy: "+msg) })
	return pinReserves(p, livePins(launches, keyOfDir, pins, now), tokens, now), view
}

// pinAlerts keeps the pass's new pin alerts (one per session and window until it resets) with
// the command that moves the session by hand, and prints them (the timer's log).
func (w *moveWorld) pinAlerts(out io.Writer, st mover.State, r mover.Result, now time.Time) {
	for _, al := range r.Alerts {
		a := mover.PinAlert{At: now, Session: al.Session, Name: w.byID[al.Session].Name, Account: w.accLabel(al.Account), Window: al.Window,
			HitAt: al.HitAt, ResetAt: al.ResetAt, Rate: al.Rate, Command: w.pinCommand(al.Session)}
		if isNew, err := st.AddAlert(a, now); err == nil && isNew {
			fmt.Fprintln(out, "alert: "+a.Text())
		}
	}
}

// pinCommand is the one line that moves pinned session id by hand to the account the switch
// would pick for it ("" when none has room).
func (w *moveWorld) pinCommand(id string) string {
	in := w.input
	in.Sessions = append([]mover.Session(nil), w.input.Sessions...)
	for i := range in.Sessions {
		if in.Sessions[i].ID == id {
			in.Sessions[i].Pinned = false
		}
	}
	to, _ := mover.Destination(in, id)
	if to == "" {
		return ""
	}
	cmd := fmt.Sprintf("wy move %s %s", short(id), w.targets[to].Label)
	if l, ok := w.launch(id); ok && w.pinSet.pinned(l) {
		cmd += " --force" // pinned in Claude Code: a by-hand move needs it
	}
	return cmd
}

// pinRow is one pinned session for --list and --plan: where its pin comes from and whether it
// holds its account's reserve now.
type pinRow struct {
	Session string `json:"session"`
	Name    string `json:"name,omitempty"`
	Account string `json:"account"`
	// From is where the pin comes from: "claude" (Claude Code's jobs/pins.json), the only source.
	From string `json:"from"`
	Live bool   `json:"live"`
}

// pinRows are the owner's pinned sessions among launches, by account then id.
func pinRows(launches []mover.Launch, labelOfDir, keyOfDir map[string]string, ps pinSet, live map[string][]string) []pinRow {
	held := map[string]bool{}
	for _, ids := range live {
		for _, id := range ids {
			held[id] = true
		}
	}
	rows := []pinRow{}
	seen := map[string]bool{}
	for _, l := range launches {
		if !ps.pinned(l) || seen[l.SessionID] {
			continue
		}
		seen[l.SessionID] = true
		acc := labelOfDir[l.Slot]
		if acc == "" {
			acc = "~/" + filepath.Base(l.Slot)
		}
		rows = append(rows, pinRow{Session: l.SessionID, Name: l.Name, Account: acc, From: "claude", Live: held[l.SessionID] && keyOfDir[l.Slot] != ""})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Account != rows[j].Account {
			return rows[i].Account < rows[j].Account
		}
		return rows[i].Session < rows[j].Session
	})
	return rows
}

// printPinRows prints rows, one line each ("pinned: 2abbe6d1 planner on claude-3 (in Claude
// Code; holds a reserve)").
func printPinRows(out io.Writer, rows []pinRow) {
	for _, r := range rows {
		state := "holds a reserve"
		if !r.Live {
			state = "idle 6 h or more: no reserve"
		}
		name := ""
		if r.Name != "" {
			name = " " + r.Name
		}
		fmt.Fprintf(out, "pinned: %s%s on %s (in Claude Code; %s)\n", short(r.Session), name, r.Account, state)
	}
}

// listPins reads this machine's sessions and pins for `wy move --list` (no picker, no hub).
func (e moveEnv) listPins(cfg config.Config) ([]pinRow, []string) {
	var warns []string
	slots, err := nextSlots(e.nextEnv, cfg)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var islots []identity.Slot
	var dirs []string
	labelOfDir, keyOfDir := map[string]string{}, map[string]string{}
	for _, s := range slots {
		islots = append(islots, identity.Slot{Dir: s.Dir, Name: s.Folder, Default: s.Default})
		dirs = append(dirs, s.Dir)
		labelOfDir[s.Dir] = s.Label
		if s.LoggedIn && s.Account != "" {
			keyOfDir[s.Dir] = "acct:" + s.Account + "/" + s.Org
		}
	}
	sys := e.sys
	if sys == nil {
		sys = &mover.Sys{FS: identity.OSFS{}, Proc: peerspkg.NewProc(), State: mover.NewState(e.dirs.State.Path)}
	}
	launches := sys.Sessions(islots)
	ps := loadPins(dirs, func(m string) { warns = append(warns, m) })
	if x := ignoredPins(mover.NewState(e.dirs.State.Path)); x != "" {
		warns = append(warns, x)
	}
	return pinRows(launches, labelOfDir, keyOfDir, ps, livePins(launches, keyOfDir, ps, e.now())), warns
}
