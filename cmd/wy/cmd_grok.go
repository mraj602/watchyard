package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mraj602/watchyard/internal/grokcli"
	"github.com/mraj602/watchyard/internal/grokpeer"
	"github.com/mraj602/watchyard/internal/paths"
)

const grokUsage = `usage: wy grok run [--dir D] [--model M] [--effort E] [--max-turns N] [--yolo]
                   [--resume ID] [--name N] [--timeout 30m] [--json] <prompt | ->
       wy grok status
       wy grok list | forget <id> | adopt <id> [--dir D] [--name N] [--yolo]

Runs xAI's Grok Build CLI headlessly and prints its answer, so a Claude session (or an
orchestrator) can hand work to Grok. "-" reads the prompt from stdin. See docs/grok.md.
`

// Seams for tests.
var (
	grokHome   = func() string { return paths.FromOS().Home }
	grokLocate = grokcli.Locate
)

func runGrok(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, grokUsage)
		return errUsage
	}
	switch args[0] {
	case "run":
		return runGrokRun(args[1:], stdout, stderr)
	case "status":
		return runGrokStatus(stdout, stderr)
	case "drain", "list", "forget", "adopt":
		return runGrokPeer(args[0], args[1:], stdout, stderr)
	case "-h", "--help":
		fmt.Fprint(stdout, grokUsage)
		return nil
	}
	fmt.Fprintf(stderr, "wy grok: unknown subcommand %q\n%s", args[0], grokUsage)
	return errUsage
}

func runGrokStatus(stdout, stderr io.Writer) error {
	bin, err := grokLocate(grokHome())
	if err != nil {
		return &exitError{code: 3, err: err}
	}
	st, err := grokcli.Check(context.Background(), bin, nil)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	fmt.Fprintf(stdout, "grok   %s\npath   %s\nsigned in  %v\n", st.Version, st.Path, st.Authenticated)
	if !st.Authenticated {
		fmt.Fprintln(stdout, "next: run `grok login --device-auth` on this machine (an interactive sign-in only the account owner can do)")
		return &exitError{code: 4}
	}
	return nil
}

func runGrokRun(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("grok run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o grokcli.Options
	var asJSON bool
	fs.StringVar(&o.Dir, "dir", "", "working directory")
	fs.StringVar(&o.Model, "model", "", "model id")
	fs.StringVar(&o.Effort, "effort", "", "reasoning effort")
	fs.IntVar(&o.MaxTurns, "max-turns", 0, "max agentic turns")
	fs.BoolVar(&o.Yolo, "yolo", false, "auto-approve all tool calls")
	fs.StringVar(&o.SessionID, "resume", "", "resume a Grok session id")
	fs.DurationVar(&o.Timeout, "timeout", 0, "overall timeout")
	fs.BoolVar(&asJSON, "json", false, "print the result as JSON")
	var name string
	fs.StringVar(&name, "name", "", "name of the session in `wy peers list`")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() < 1 {
		fmt.Fprint(stderr, grokUsage)
		return errUsage
	}
	o.Prompt = strings.Join(fs.Args(), " ")
	if o.Prompt == "-" {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
		if err != nil {
			return err
		}
		o.Prompt = string(b)
	}
	bin, err := grokLocate(grokHome())
	if err != nil {
		return &exitError{code: 3, err: err}
	}
	host := grokHost()
	if o.SessionID != "" {
		// A resumed session is busy while this turn runs; messages sent meanwhile queue and
		// are delivered when it ends.
		if l, lerr := host.Hold(o.SessionID); lerr == nil {
			defer func() {
				l.Unlock()
				_ = host.Drain(context.Background(), o.SessionID)
			}()
		} else {
			return &exitError{code: 1, err: fmt.Errorf("session %s is busy (another run or a delivered message holds it); retry when it ends", o.SessionID)}
		}
	}
	res, err := grokcli.Run(context.Background(), bin, nil, o)
	if err != nil {
		code := 1
		if err == grokcli.ErrNotAuthenticated {
			code = 4
		}
		return &exitError{code: code, err: err}
	}
	if res.SessionID != "" {
		dir := o.Dir
		if dir == "" {
			dir, _ = os.Getwd()
		}
		if _, rerr := host.Register(res.SessionID, dir, name, o, res); rerr != nil {
			fmt.Fprintf(stderr, "wy grok: session not registered as a peer: %v\n", rerr)
		}
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(res)
	}
	fmt.Fprintln(stdout, strings.TrimRight(res.Text, "\n"))
	if res.SessionID != "" {
		fmt.Fprintf(stderr, "grok session %s (%d turns, %d tokens)\n", res.SessionID, res.NumTurns, res.Tokens)
	}
	return nil
}

// grokHost is the Grok peers host of this machine; a variable so tests use a temp state dir.
var grokHost = func() *grokpeer.Host {
	dirs, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return &grokpeer.Host{State: os.TempDir()}
	}
	return &grokpeer.Host{State: dirs.State.Path}
}

// runGrokPeer is drain (internal, started detached by a delivery), list, forget and adopt.
func runGrokPeer(sub string, args []string, stdout, stderr io.Writer) error {
	h := grokHost()
	switch sub {
	case "list":
		for _, r := range h.Records() {
			st := "idle"
			if h.Busy(r.ID) {
				st = "busy"
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%d queued\t%s\n", r.ID, st, r.Name, h.Pending(r.ID), r.Dir)
		}
		return nil
	case "forget", "drain":
		if len(args) != 1 {
			fmt.Fprint(stderr, grokUsage)
			return errUsage
		}
		if sub == "forget" {
			return h.Forget(args[0])
		}
		return h.Drain(context.Background(), args[0])
	}
	// adopt: make an existing Grok session (e.g. started by hand) reachable as a peer.
	fs := flag.NewFlagSet("grok adopt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o grokcli.Options
	var dir, name string
	fs.StringVar(&dir, "dir", "", "working directory of the session")
	fs.StringVar(&name, "name", "", "name in `wy peers list`")
	fs.BoolVar(&o.Yolo, "yolo", false, "deliveries run with auto-approve")
	if err := fs.Parse(reorderFlags(args)); err != nil || fs.NArg() != 1 {
		fmt.Fprint(stderr, grokUsage)
		return errUsage
	}
	r, err := h.Register(fs.Arg(0), dir, name, o, nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "adopted grok session %s as %s\n", r.ID, r.Name)
	return nil
}

// reorderFlags moves the positional arguments after the flags so `adopt <id> --name x` works.
func reorderFlags(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && a != "--yolo" && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}
