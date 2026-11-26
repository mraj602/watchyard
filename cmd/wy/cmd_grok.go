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
	"github.com/mraj602/watchyard/internal/paths"
)

const grokUsage = `usage: wy grok run [--dir D] [--model M] [--effort E] [--max-turns N] [--yolo]
                   [--resume ID] [--timeout 30m] [--json] <prompt | ->
       wy grok status

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
	res, err := grokcli.Run(context.Background(), bin, nil, o)
	if err != nil {
		code := 1
		if err == grokcli.ErrNotAuthenticated {
			code = 4
		}
		return &exitError{code: code, err: err}
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
