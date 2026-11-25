package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/paths"
)

// wy account drain <label> [--until <time> | off] (setup.md 8.5, Owner drain): the owner
// drains an account now, not only in the day before its weekly reset. Stored as
// next.accounts.<label>.drain_until on this machine.

func drainUsage(w io.Writer) {
	fmt.Fprint(w, `usage: wy account drain <label> [--until <time>]   drain it until then (default: the end of today)
       wy account drain <label> off                  stop draining it
<time> is 15:04 (today), 2006-01-02 15:04 or 2006-01-02T15:04:05+05:30.
While on, new lanes go to it first, up to its drain aims, the switch moves nothing off it
before a hit within 60 min and pulls busy burners in while its 5h projection has room. It
ends by itself at <time>, or once its weekly level reaches the drain aim.
`)
}

func runAccountDrain(args []string, stdout, stderr io.Writer) error {
	pe := paths.FromOS()
	d, err := paths.Resolve(pe)
	if err != nil {
		return err
	}
	home := pe.Home
	if home == "" {
		return errors.New("cannot find the home folder")
	}
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	return accountDrain(nextEnv{home: home, dirs: d, getenv: os.Getenv, now: time.Now, stdout: stdout, stderr: stderr}, args)
}

func accountDrain(e nextEnv, args []string) error {
	fs := flag.NewFlagSet("account drain", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() { drainUsage(e.stderr) }
	until := fs.String("until", "", "15:04, 2006-01-02 15:04, RFC 3339, or off")
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
		fmt.Fprintf(e.stdout, "%s: owner drain off\n", label)
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
	fmt.Fprintf(e.stdout, "%s: draining (owner, until %s): new lanes go to it first and the switch pulls busy burners in, up to its drain aims\n",
		label, drainUntilText(t, now))
	return nil
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
