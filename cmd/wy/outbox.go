package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/record"
)

// runOutbox is `wy outbox append|stat`.
func runOutbox(args []string, stdout, stderr io.Writer) error {
	return runOutboxIn(os.Stdin, args, stdout, stderr)
}

func runOutboxIn(stdin io.Reader, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: wy outbox append|stat|drain [flags]")
		return errUsage
	}
	switch args[0] {
	case "append":
		return outboxAppend(stdin, args[1:], stdout, stderr)
	case "stat":
		return outboxStat(args[1:], stdout, stderr)
	case "drain":
		return outboxDrain(stdin, args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "wy outbox: unknown subcommand %q (want append, stat or drain)\n", args[0])
	return errUsage
}

func outboxFlags(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("outbox "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "outbox folder (default: <state>/outbox)")
	return fs, dir
}

// outboxDir is --dir, else <state>/outbox from the paths package (paths.md).
func outboxDir(flagDir string) (string, error) {
	if flagDir != "" {
		return flagDir, nil
	}
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return "", err
	}
	return filepath.Join(d.State.Path, "outbox"), nil
}

// outboxAppend reads records (one JSON value per line, or any run of JSON values) from stdin
// and appends each, filling v, id and ts when a writer left them out. It prints one id per
// line. A bad record is reported and skipped; the exit status is 1 if any failed.
func outboxAppend(stdin io.Reader, args []string, stdout, stderr io.Writer) error {
	fs, dirFlag := outboxFlags("append", stderr)
	noFsync := fs.Bool("no-fsync", false, "skip fsync (record survives a crash of wy, maybe not a power cut)")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "wy outbox append: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	dir, err := outboxDir(*dirFlag)
	if err != nil {
		return err
	}
	ob := outbox.New(dir)
	ob.NoFsync = *noFsync

	dec := json.NewDecoder(stdin)
	failed, n := 0, 0
	for {
		var r record.Record
		err := dec.Decode(&r)
		if err == io.EOF {
			break
		}
		n++
		if err != nil {
			fmt.Fprintf(stderr, "wy outbox append: record %d: %v\n", n, err)
			return fmt.Errorf("%d of %d records failed (stopped at unparsable input)", failed+1, n)
		}
		if r.V == 0 {
			r.V = record.Version
		}
		now := time.Now()
		if r.TS == "" {
			r.TS = record.FormatTime(now)
		}
		if r.ID == "" {
			r.ID = record.NewID(now)
		}
		if err := r.Validate(); err != nil {
			fmt.Fprintf(stderr, "wy outbox append: record %d: %v\n", n, err)
			failed++
			continue
		}
		res, err := ob.Append(&r)
		if err != nil {
			fmt.Fprintf(stderr, "wy outbox append: record %d (%s): %v\n", n, r.ID, err)
			failed++
			continue
		}
		fmt.Fprintln(stdout, res.ID)
	}
	if n == 0 {
		return errors.New("no record on stdin")
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d records failed", failed, n)
	}
	return nil
}

func outboxStat(args []string, stdout, stderr io.Writer) error {
	fs, dirFlag := outboxFlags("stat", stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "wy outbox stat: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	dir, err := outboxDir(*dirFlag)
	if err != nil {
		return err
	}
	st, err := outbox.New(dir).Stat()
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Dir string `json:"dir"`
			outbox.Stats
		}{dir, st})
	}
	fmt.Fprintf(stdout, "folder:    %s\n", dir)
	fmt.Fprintf(stdout, "files:     %d day, %d spill\n", st.DayFiles, st.SpillFiles)
	fmt.Fprintf(stdout, "size:      %s (%s not yet flushed)\n", humanBytes(st.Bytes), humanBytes(st.UnflushedBytes))
	if st.OldestUnflushed.IsZero() {
		fmt.Fprintln(stdout, "oldest unflushed: none")
	} else {
		fmt.Fprintf(stdout, "oldest unflushed: %s (%s ago)\n", record.FormatTime(st.OldestUnflushed),
			time.Since(st.OldestUnflushed).Round(time.Second))
	}
	if st.Cursor.File != "" {
		fmt.Fprintf(stdout, "cursor:    %s @ %d\n", st.Cursor.File, st.Cursor.Offset)
	} else {
		fmt.Fprintln(stdout, "cursor:    start")
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// outboxDrain is `wy outbox drain --stdio | --peek [--max N]` (machines.md 3.5, outbox.md 8).
// --stdio speaks the drain protocol on stdin/stdout (stderr is for people); started by the
// hub's forced command it ignores SSH_ORIGINAL_COMMAND and accepts no flag but --stdio.
func outboxDrain(stdin io.Reader, args []string, stdout, stderr io.Writer) error {
	if _, forced := os.LookupEnv("SSH_ORIGINAL_COMMAND"); forced {
		if len(args) != 1 || args[0] != "--stdio" {
			fmt.Fprintln(stderr, "wy outbox drain: only --stdio is allowed here")
			return exitWith(3, nil)
		}
	}
	fs, dirFlag := outboxFlags("drain", stderr)
	stdio := fs.Bool("stdio", false, "speak the drain protocol on stdin and stdout")
	peek := fs.Bool("peek", false, "print the next records as JSONL and change nothing")
	max := fs.Int("max", 500, "with --peek: how many records")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 || *stdio == *peek {
		fmt.Fprintln(stderr, "usage: wy outbox drain --stdio | --peek [--max N]")
		return errUsage
	}
	dir, err := outboxDir(*dirFlag)
	if err != nil {
		return err
	}
	ob := outbox.New(dir)
	if *peek {
		// Read quarantines corrupt lines and deletes junk spill files: only the flush.lock
		// holder may do that (outbox.md 5).
		lock, ok, err := ob.TryFlushLock()
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("a flush or drain is running on this outbox (flush.lock is held); try again in a moment")
		}
		defer lock.Release()
		b, err := ob.Read(ob.ReadCursor(), *max, 0)
		if err != nil {
			return err
		}
		for _, it := range b.Items {
			if _, err := stdout.Write(append(append([]byte{}, it.Raw...), '\n')); err != nil {
				return err
			}
		}
		return nil
	}
	m, err := identity.LoadOrCreateMachine(filepath.Dir(dir), identity.MachineOptions{})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	dopts := client.DrainOptions{Outbox: ob, Machine: m.ID, Name: identity.ClampMachineName(m.Name),
		Logf: func(f string, a ...any) { fmt.Fprintf(stderr, "wy outbox drain: "+f+"\n", a...) }}
	if d, err := paths.Resolve(paths.FromOS()); err == nil {
		if f, err := config.Load(d.Config.Path); err == nil {
			if cfg, err := f.Typed(); err == nil {
				dopts.CapBytes, dopts.KeepDays = int64(cfg.Outbox.MaxMB)<<20, cfg.Outbox.KeepDays
				ob.NoFsync = !cfg.Outbox.Fsync
			}
		}
	}
	res := client.Drain(ctx, stdin, stdout, dopts)
	switch {
	case res.Err == nil:
		return nil
	case res.ProtocolErr:
		fmt.Fprintf(stderr, "wy outbox drain: %v\n", res.Err)
		return exitWith(3, nil)
	default:
		fmt.Fprintf(stderr, "wy outbox drain: %v\n", res.Err) // nack, bye, EOF, refuse: exit 0
		return nil
	}
}
