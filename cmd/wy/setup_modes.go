package main

// The picker mode question of `wy setup` and `wy account add` (setup.md 8.5, Kind: Choosing in
// wy setup and wy account add): interactive runs ask once per account that has no mode yet (no
// local next.accounts key, no hub setting, no slots.modes proposal); the answer goes to
// slots.modes.<folder> with a hub, next.accounts.<email or label>.mode without.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/picker"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/plan"
)

// modeHubBudget bounds the read of the hub's modes: setup still asks when the hub is slow.
const modeHubBudget = 3 * time.Second

// modeSetup is what the mode question needs to know about the machine.
type modeSetup struct {
	cfg config.Config
	// hub: answers go to slots.modes (a hub is connected, or this run connects one).
	hub bool
	// hubModes are the seats' hub modes by folder, read from the hub ("" none).
	hubModes map[string]string
	// seats are the matched hub seat id and label by folder (for next.accounts keys).
	seats map[string]picker.HubSeat
}

// loadModeSetup reads config.toml and, with a hub connected (hub.url and a machine token),
// the seats' modes from GET /v1/usage within modeHubBudget. joining says this run connects a
// hub (--hub here or join), so the answers are proposals for it. A hub that cannot be read
// is no reason not to ask.
func (e *setupEnv) loadModeSetup(ctx context.Context, st model.State, joining bool) modeSetup {
	ms := modeSetup{cfg: config.Defaults(), hubModes: map[string]string{}, seats: map[string]picker.HubSeat{}}
	if f, err := config.Load(e.dirs.Config.Path); err == nil {
		if c, err := f.Typed(); err == nil {
			ms.cfg = c
		}
	}
	tok, terr := client.ReadToken(e.dirs.State.Path)
	connected := ms.cfg.Hub.URL != "" && terr == nil && tok != ""
	ms.hub = connected || joining
	if !connected {
		return ms
	}
	url, _, err := client.CheckURL(ms.cfg.Hub.URL, ms.cfg.Hub.InsecureLAN)
	if err != nil {
		return ms
	}
	hc := e.http
	if hc == nil {
		tr, err := client.DialTransport(ms.cfg.Hub.Dial)
		if err != nil {
			return ms
		}
		hc = &http.Client{Transport: tr, Timeout: modeHubBudget}
	}
	cctx, cancel := context.WithTimeout(ctx, modeHubBudget)
	defer cancel()
	now := time.Now()
	d, err := picker.HubClient{URL: url, Token: tok, HTTP: hc}.FetchUsage(cctx, now)
	if err != nil {
		return ms
	}
	machine := ""
	if m, err := identity.LoadOrCreateMachine(e.dirs.State.Path, identity.MachineOptions{}); err == nil {
		machine = m.ID
	}
	store := &identity.Store{StateDir: e.dirs.State.Path, FS: identity.OSFS{}}
	var slots []picker.Slot
	for _, fo := range st.Folders {
		s := picker.Slot{Folder: fo.Folder, Dir: fo.Dir, Default: fo.Default, Label: fo.Label, LoggedIn: fo.LoggedIn, Email: fo.Email}
		if fo.LoggedIn && fo.Dir != "" && fo.StateFile != "" {
			if l, err := store.Login(identity.Slot{Name: fo.Folder, Dir: fo.Dir, StateFile: fo.StateFile, Default: fo.Default}); err == nil && l.HasSeat {
				s.Account, s.Org = l.Seat.Account, l.Seat.Org
			}
		}
		slots = append(slots, s)
	}
	for _, c := range picker.Candidates(slots, picker.Sources{Machine: machine, Hub: &d}) {
		if c.SeatID == "" {
			continue
		}
		ms.hubModes[c.Folder] = c.HubMode
		ms.seats[c.Folder] = picker.HubSeat{ID: c.SeatID, Label: &c.HubLabel}
	}
	return ms
}

// askSetupModes asks the mode question of wy setup for the folders of st and c (the labels
// chosen so far) and returns the config keys to set; nil when nothing is to be asked.
func (e *setupEnv) askSetupModes(ctx context.Context, st model.State, c model.Choices) map[string]string {
	accs, err := plan.Accounts(st, c)
	if err != nil {
		return nil // plan.Plan reports it
	}
	joining := c.Hub.Mode == model.HubHere || c.Hub.Mode == model.HubJoin
	ms := e.loadModeSetup(ctx, st, joining)
	asks := ms.unchosen(accs)
	if len(asks) == 0 {
		return nil
	}
	return ms.askModes(e.stdin, e.stdout, asks)
}

// askAddMode is the mode question of wy account add for the new folder nt (setup.md 8.5,
// Choosing in wy account add): the config keys to set, nil when it is not asked or skipped.
func (e *setupEnv) askAddMode(ctx context.Context, st model.State, nt, label string) map[string]string {
	ms := e.loadModeSetup(ctx, st, false)
	asks := ms.unchosen([]plan.Account{{Folder: nt, Label: label, New: true}})
	if len(asks) == 0 {
		return nil
	}
	return ms.askModes(e.stdin, e.stdout, asks)
}

// modeAsk is one question: an account and the folders logged in to it.
type modeAsk struct {
	Label, Email string
	Folders      []string
}

// unchosen lists the accounts of accs that have no mode yet: logged-in folders (once per
// login) and folders new in this run.
func (ms modeSetup) unchosen(accs []plan.Account) []modeAsk {
	s := picker.SettingsFrom(ms.cfg.Next)
	s.Proposed = ms.cfg.Slots.Modes
	var out []modeAsk
	byEmail := map[string]int{}
	for _, a := range accs {
		if !a.LoggedIn && !a.New {
			continue
		}
		c := picker.Candidate{Folder: a.Folder, Label: a.Label, Email: a.Email, HubMode: ms.hubModes[a.Folder]}
		if seat, ok := ms.seats[a.Folder]; ok {
			c.SeatID = seat.ID
			if seat.Label != nil {
				c.HubLabel = *seat.Label
			}
		}
		if pol, _ := s.PolicyFor(c); pol.ModeFrom != picker.ModeFromNone {
			continue
		}
		if a.Email != "" {
			k := strings.ToLower(a.Email)
			if i, ok := byEmail[k]; ok {
				out[i].Folders = append(out[i].Folders, a.Folder)
				continue
			}
			byEmail[k] = len(out)
		}
		out = append(out, modeAsk{Label: a.Label, Email: a.Email, Folders: []string{a.Folder}})
	}
	return out
}

// keys are the config keys an answer sets: slots.modes.<folder> for each folder with a hub,
// else next.accounts.<email, else label>.mode.
func (ms modeSetup) keys(a modeAsk) []string {
	if ms.hub {
		out := make([]string, 0, len(a.Folders))
		for _, f := range a.Folders {
			out = append(out, "slots.modes."+f)
		}
		return out
	}
	key := a.Email
	if key == "" {
		key = a.Label
	}
	return []string{"next.accounts." + key + ".mode"}
}

// askModes asks each question on in/out and returns the config keys to set. Skip (the
// default) and the end of the input leave an account not chosen.
func (ms modeSetup) askModes(in io.Reader, out io.Writer, asks []modeAsk) map[string]string {
	set := map[string]string{}
	for _, a := range asks {
		who := a.Label
		if a.Email != "" {
			who += " (" + a.Email + ")"
		}
		mode, ok := askMode(in, out, who)
		if !ok {
			break
		}
		if mode == "" {
			continue
		}
		for _, k := range ms.keys(a) {
			set[k] = mode
		}
	}
	return set
}

// askMode asks one question: protected, aggressive or "" (skip); ok is false at the end of
// the input.
func askMode(in io.Reader, out io.Writer, who string) (string, bool) {
	fmt.Fprintf(out, "How should `wy account next` use %s?\n", who)
	fmt.Fprintf(out, "  p  protected: %s\n", picker.ModeWords.Protected)
	fmt.Fprintf(out, "  a  aggressive: %s\n", picker.ModeWords.Aggressive)
	fmt.Fprintf(out, "  s  skip: %s\n", picker.ModeWords.NotChosen)
	for range 3 {
		fmt.Fprint(out, "[p/a/S] ")
		line, err := readLine(in)
		if err != nil && line == "" {
			fmt.Fprintln(out)
			return "", false
		}
		switch strings.ToLower(line) {
		case "p", "protected":
			return config.NextModeProtected, true
		case "a", "aggressive":
			return config.NextModeAggressive, true
		case "", "s", "skip":
			return "", true
		}
		fmt.Fprintln(out, "Answer p, a or s.")
	}
	return "", true
}

// queueModes queues a slot_info with the mode for each logged-in folder whose proposal this
// run wrote, when the machine has a hub token (a folder that logs in later sends it with its
// next slot_info, on SessionStart).
func (e *setupEnv) queueModes(st model.State, modes map[string]string) {
	if tok, err := client.ReadToken(e.dirs.State.Path); err != nil || tok == "" {
		return
	}
	for _, fo := range st.Folders {
		m, ok := modes["slots.modes."+fo.Folder]
		if !ok || !fo.LoggedIn {
			continue
		}
		if err := sendSlotInfo(e, fo, "", "", m); err != nil {
			fmt.Fprintf(e.stderr, "wy setup: slot_info with the mode of %s not queued: %v\n", fo.Folder, err)
		}
	}
}
