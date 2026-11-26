package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/picker"
)

// wy account drain <label> [--until <time> | off] (setup.md 8.5, Owner drain): the owner
// drains an account now, not only in the day before its weekly reset. Stored as
// next.accounts.<label>.drain_until on this machine and, with a hub, as the seat's
// drain_until for every machine.

func drainUsage(w io.Writer) {
	fmt.Fprint(w, `usage: wy account drain <label> [--until <time>] [--local]   drain it until then (default: the end of today)
       wy account drain <label> off [--local]                  stop draining it
<time> is 15:04 (today), 2006-01-02 15:04 or 2006-01-02T15:04:05+05:30.
With a hub it is set there too, for every machine (--local: this machine only); this
machine's own setting wins here.
While on, new lanes go to it first, up to its drain aims, the switch moves nothing off it
before a hit within 60 min and pulls busy burners in while its 5h projection has room. It
ends by itself at <time>, or once its weekly level reaches the drain aim.
`)
}

func runAccountDrain(args []string, stdout, stderr io.Writer) error {
	e, err := accountEnv(stdout, stderr)
	if err != nil {
		return err
	}
	return accountDrain(e, args)
}

// runAccountMode is wy account mode <label> aggressive|protected|off|none (setup.md 8.5,
// Kind): this machine's next.accounts.<label>.mode (none deletes it: the hub's mode applies).
// The mode for every machine is the hub's, set on the board or in the phone app.
func runAccountMode(args []string, stdout, stderr io.Writer) error {
	e, err := accountEnv(stdout, stderr)
	if err != nil {
		return err
	}
	return accountMode(e, args)
}

func accountEnv(stdout, stderr io.Writer) (nextEnv, error) {
	pe := paths.FromOS()
	d, err := paths.Resolve(pe)
	if err != nil {
		return nextEnv{}, err
	}
	home := pe.Home
	if home == "" {
		return nextEnv{}, errors.New("cannot find the home folder")
	}
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	return nextEnv{home: home, dirs: d, getenv: os.Getenv, now: time.Now, stdout: stdout, stderr: stderr}, nil
}

func modeUsage(w io.Writer) {
	fmt.Fprint(w, `usage: wy account mode <label> aggressive|protected|off|none
This machine's mode of the account (next.accounts.<label>.mode); none: the hub's applies.
aggressive: used up first. protected: kept for you, used last. off: never picked and nothing
is moved into it; sessions already there stay. Drain: wy account drain <label>.
`)
}

func accountMode(e nextEnv, args []string) error {
	if len(args) != 2 {
		modeUsage(e.stderr)
		return errUsage
	}
	mode := args[1]
	switch mode {
	case config.NextModeAggressive, config.NextModeProtected, config.NextModeOff, "none":
	default:
		modeUsage(e.stderr)
		return errUsage
	}
	label, err := drainLabel(e, args[0])
	if err != nil {
		return err
	}
	f, err := config.Load(e.dirs.Config.Path)
	if err != nil {
		return err
	}
	key := "next.accounts." + label + ".mode"
	if mode == "none" {
		err = f.Delete(key)
	} else {
		err = f.Set(key, []string{mode})
	}
	if err == nil {
		err = f.Save()
	}
	if err != nil {
		return err
	}
	switch mode {
	case "none":
		fmt.Fprintf(e.stdout, "%s: no mode set on this machine; the hub's applies\n", label)
	case config.NextModeOff:
		fmt.Fprintf(e.stdout, "%s: off on this machine: never picked and nothing is moved into it; sessions already there stay\n", label)
	default:
		fmt.Fprintf(e.stdout, "%s: %s on this machine\n", label, mode)
	}
	return nil
}

// drainLabel resolves an account label or folder to its label on this machine.
func drainLabel(e nextEnv, arg string) (string, error) {
	slots, err := nextSlots(e, moveEnv{nextEnv: e}.config())
	if err != nil {
		return "", err
	}
	for _, s := range slots {
		if s.Label == arg || s.Folder == arg || s.Folder == "~/"+strings.TrimPrefix(arg, "~/") {
			return s.Label, nil
		}
	}
	return "", fmt.Errorf("wy account: no account folder labelled %q on this machine", arg)
}

func accountDrain(e nextEnv, args []string) error {
	fs := flag.NewFlagSet("account drain", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() { drainUsage(e.stderr) }
	until := fs.String("until", "", "15:04, 2006-01-02 15:04, RFC 3339, or off")
	local := fs.Bool("local", false, "this machine only: do not set it on the hub")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return usageErr(err)
	}
	if len(pos) == 2 && pos[1] == "off" && *until == "" {
		*until, pos = "off", pos[:1]
	}
	if len(pos) != 1 {
		drainUsage(e.stderr)
		return errUsage
	}
	now := e.now()
	cfg := moveEnv{nextEnv: e}.config()
	slots, err := nextSlots(e, cfg)
	if err != nil {
		return err
	}
	label := ""
	for _, s := range slots {
		if s.Label == pos[0] || s.Folder == pos[0] || s.Folder == "~/"+strings.TrimPrefix(pos[0], "~/") {
			label = s.Label
			break
		}
	}
	if label == "" {
		return fmt.Errorf("wy account drain: no account folder labelled %q on this machine", pos[0])
	}
	f, err := config.Load(e.dirs.Config.Path)
	if err != nil {
		return err
	}
	key := "next.accounts." + label + ".drain_until"
	if *until == "off" {
		if err := f.Delete(key); err != nil {
			return err
		}
		if err := f.Save(); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "%s: owner drain off%s\n", label, hubDrainNote(e, cfg, slots, label, nil, *local, now))
		return nil
	}
	t, err := parseDrainUntil(*until, now)
	if err != nil {
		return fmt.Errorf("wy account drain: %w", err)
	}
	if !t.After(now) {
		return fmt.Errorf("wy account drain: %s is not in the future", t.Format("2006-01-02 15:04"))
	}
	if err := f.Set(key, []string{t.Format(time.RFC3339)}); err != nil {
		return err
	}
	if err := f.Save(); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "%s: draining (owner, until %s): new lanes go to it first and the switch pulls busy burners in, up to its drain aims%s\n",
		label, drainUntilText(t, now), hubDrainNote(e, cfg, slots, label, &t, *local, now))
	return nil
}

// hubDrainNote sets the drain on the hub too (PUT /v1/accounts/{seat}/drain), so every
// machine drains the account, and says how that went; "" without a hub or with --local.
// This machine's own setting is written first and wins here either way.
func hubDrainNote(e nextEnv, cfg config.Config, slots []picker.Slot, label string, until *time.Time, local bool, now time.Time) string {
	if local || cfg.Hub.URL == "" {
		return ""
	}
	if err := hubDrain(e, cfg, slots, label, until, now); err != nil {
		return "\n  on this machine only: the hub was not set (" + err.Error() + ")"
	}
	return "\n  set on the hub too: every machine drains it"
}

func hubDrain(e nextEnv, cfg config.Config, slots []picker.Slot, label string, until *time.Time, now time.Time) error {
	m, err := identity.LoadOrCreateMachine(e.dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return err
	}
	hd, err := nextHub(e, cfg, slots, m.ID, now)
	if err != nil || hd == nil {
		if err == nil {
			err = errors.New("no hub")
		}
		return err
	}
	seat := ""
	for _, c := range picker.Candidates(slots, picker.Sources{Machine: m.ID, Hub: hd}) {
		if c.Label == label {
			seat = c.SeatID
		}
	}
	if seat == "" {
		return errors.New("the hub has no account matching " + label + " on this machine")
	}
	tok, err := client.ReadToken(e.dirs.State.Path)
	if err != nil {
		return errors.New("no machine token; run wy connect")
	}
	tr := e.transport
	if tr == nil {
		if tr, err = client.DialTransport(cfg.Hub.Dial); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), nextHubTimeout)
	defer cancel()
	return picker.HubClient{URL: cfg.Hub.URL, Token: tok, HTTP: &http.Client{Transport: tr, Timeout: nextHubTimeout}}.PutDrain(ctx, seat, until)
}

// parseDrainUntil reads --until: "" is the end of today (23:59 local), 15:04 is today,
// 2006-01-02 15:04 local, or RFC 3339.
func parseDrainUntil(v string, now time.Time) (time.Time, error) {
	loc := now.Location()
	switch {
	case v == "":
		y, m, d := now.Date()
		return time.Date(y, m, d, 23, 59, 0, 0, loc), nil
	case len(v) == 5 && v[2] == ':':
		t, err := time.ParseInLocation("15:04", v, loc)
		if err != nil {
			return time.Time{}, fmt.Errorf("--until %q: want 15:04", v)
		}
		y, m, d := now.Date()
		return time.Date(y, m, d, t.Hour(), t.Minute(), 0, 0, loc), nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", v, loc); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--until %q: want 15:04, 2006-01-02 15:04 or 2006-01-02T15:04:05+05:30", v)
}

// drainUntilText is the until time as the plan shows it: 23:59 today, else Mon 07:30.
func drainUntilText(t, now time.Time) string {
	t = t.In(now.Location())
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	return t.Format("Mon 15:04")
}
