package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mraj602/watchyard/internal/ccjobs"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/doctor"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/status"
)

// runStatus is `wy status [--json] [--all]`.
func runStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	all := fs.Bool("all", false, "include ended and gone sessions")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "wy status: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	pe := paths.FromOS()
	d, err := paths.Resolve(pe)
	if err != nil {
		return err
	}
	f, err := config.Load(d.Config.Path)
	if err != nil {
		return err
	}
	cfg, err := f.Typed()
	if err != nil {
		return err
	}
	now := time.Now()
	rows, err := status.Collect(status.Options{
		OutboxDir: filepath.Join(d.State.Path, "outbox"), Home: pe.Home, Labels: cfg.Slots.Labels, All: *all, Now: now,
		Keepalive: statusKeepalive(pe.Home),
	})
	if err != nil {
		return err
	}
	if *asJSON {
		if rows == nil {
			rows = []status.Row{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"sessions": rows})
	}
	return status.Render(stdout, rows, now)
}

// statusKeepalive reads Claude Code's daemon files of each slot once (states.md 5.2,
// best-effort: a slot whose files are missing knows nothing).
func statusKeepalive(home string) func(slot, sessionID string) status.Keepalive {
	if home == "" {
		return nil
	}
	loc := identity.Locator{Home: home, FS: identity.OSFS{}}
	cache := map[string]ccjobs.Account{}
	return func(slot, sessionID string) status.Keepalive {
		if slot == "" {
			return status.Keepalive{}
		}
		a, ok := cache[slot]
		if !ok {
			a = ccjobs.Load(loc.SlotFor(slot).Dir)
			cache[slot] = a
		}
		short := a.Job(sessionID)
		if short == "" {
			return status.Keepalive{}
		}
		k := status.Keepalive{Background: true, Pinned: a.Pins[short]}
		if r, ok := a.Retired[short]; ok {
			k.RetiredAt = r.At
		}
		return k
	}
}

// runDoctor is `wy doctor [--json] [--rebaseline]`. It exits non-zero only when a check
// fails (warnings are for you to read).
func runDoctor(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	rebase := fs.Bool("rebaseline", false, "re-record which settings.json files are symlinks")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "wy doctor: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	pe := paths.FromOS()
	d, err := paths.Resolve(pe)
	if err != nil {
		return err
	}
	if pe.Home == "" {
		return fmt.Errorf("cannot find the home folder")
	}
	rep := doctor.Run(context.Background(), doctor.Options{
		Home: pe.Home, Dirs: d, Getenv: os.Getenv, Environ: os.Environ(), Rebaseline: *rebase,
	})
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		rep.WriteText(stdout)
	}
	if rep.Failed() {
		return fmt.Errorf("%d check(s) failed", rep.Fail)
	}
	return nil
}
