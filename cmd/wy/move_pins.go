package main

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/mover"
	peerspkg "github.com/mraj602/watchyard/internal/peers"
	"github.com/mraj602/watchyard/internal/picker"
)

// livePins are this machine's live pinned sessions by account key (setup.md 8.6, Pinned
// sessions): pinned by the owner (`wy move --pin-account`, the peers tag no-move; a session
// only outside account.smart_switch_scope holds none), running, and not idle for
// mover.PinIdle.
func livePins(launches []mover.Launch, keyOfDir map[string]string, pins map[string]bool, tags map[string][]string, now time.Time) map[string][]string {
	out := map[string][]string{}
	for _, l := range launches {
		key := keyOfDir[l.Slot]
		if key == "" || !(pins[l.SessionID] || peerspkg.HasTag(tags[l.SessionID], peerspkg.NoMoveTag)) {
			continue
		}
		if l.Status != "busy" && !l.StatusSince.IsZero() && now.Sub(l.StatusSince) >= mover.PinIdle {
			continue // idle for 6 hours: its reserve is released
		}
		out[key] = append(out[key], l.SessionID)
	}
	for _, ids := range out {
		sort.Strings(ids)
	}
	return out
}

// pinReserves are the picker's pin reserves from the live pins and the account.pin_reserve_*
// settings.
func pinReserves(p mover.Params, live map[string][]string) map[string]picker.PinReserve {
	out := map[string]picker.PinReserve{}
	for k, ids := range live {
		r5, r7 := p.PinReserve(len(ids))
		out[k] = picker.PinReserve{Pins: len(ids), R5: r5, R7: r7}
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
	tags, _ := peersTags()
	return pinReserves(p, livePins(launches, keyOfDir, st.Pins(), tags, now)), view
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
	return fmt.Sprintf("wy move %s %s", short(id), w.targets[to].Label)
}
