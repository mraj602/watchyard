// Command wy is Watchyard's single binary: the client side (hook, statusline, scan,
// flush, setup, doctor) and the hub (wy hub serve). Unofficial; not affiliated with
// Anthropic.
//
// Subcommands are dispatched with the standard library only. Each subcommand owns a
// flag.FlagSet and lives in its own internal package; this file only routes.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/mraj602/watchyard/internal/version"
)

// command is one subcommand. run gets the arguments after the subcommand name.
type command struct {
	summary string
	run     func(args []string, stdout, stderr io.Writer) error
}

// exitNotImplemented is the exit status of a command this build does not have (`wy machine
// add` reads it from an older wy on a remote machine).
const exitNotImplemented = 3

// errUsage means the arguments were wrong; the usage text has already been printed.
var errUsage = errors.New("usage")

// usageErr marks a flag parse error (unknown flag, bad value; the flag package has printed
// it with the usage) as a usage error: exit 2, like every other argument error.
func usageErr(err error) error {
	if err == nil || errors.Is(err, errUsage) {
		return err
	}
	return fmt.Errorf("%w: %w", errUsage, err)
}

func commands() map[string]command {
	return map[string]command{
		"version":     {summary: "Print the version", run: runVersion},
		"help":        {summary: "Show help for wy or a subcommand", run: nil}, // handled in dispatch
		"machine":     {summary: "Print this machine's id and name", run: runMachine},
		"outbox":      {summary: "Append to or inspect the local outbox (append, stat)", run: runOutbox},
		"hook":        {summary: "Record one Claude Code hook event (stdin JSON)", run: runHook},
		"grok":        {summary: "Run xAI Grok Build headlessly (run, status)", run: runGrok},
		"agent":       {summary: "The peers agent service (enable, disable, status, run)", run: runAgent},
		"peers":       {summary: "Message sessions in other accounts (list, send, off, on)", run: runPeers},
		"statusline":  {summary: "Status line wrapper that also records limits", run: runStatusline},
		"scan":        {summary: "Read token rows from transcripts", run: runScan},
		"agents":      {summary: "Record the live session list of each folder", run: runAgents},
		"status":      {summary: "Show local sessions", run: runStatus},
		"analytics":   {summary: "Show the hub's usage reports (daily, models, projects, heatmap, sessions, limits, hits)", run: runAnalytics},
		"doctor":      {summary: "Check the installation and print fixes", run: runDoctor},
		"paths":       {summary: "Print the config, state and data folders", run: runPaths},
		"config":      {summary: "Read and change config.toml (get, set)", run: runConfig},
		"connect":     {summary: "Connect this machine to a hub (url, token, timer)", run: runConnect},
		"disconnect":  {summary: "Remove the hub link, token and timer", run: runDisconnect},
		"flush":       {summary: "Send queued records to the hub", run: runFlush},
		"tick":        {summary: "Timer entry point: flush, share sync, agents, scan", run: runTick},
		"hub":         {summary: "Run and manage the hub (serve, pair, owner-link, token, backup, prune)", run: runHub},
		"phone":       {summary: "Pair a phone with this hub (wy phone pair; run on the hub)", run: runPhone},
		"setup":       {summary: "Set up accounts, sharing, hub and phone (--undo reverses a run)", run: runSetup},
		"account":     {summary: "Add, log in, label, share and remove accounts; pick the next one to use", run: runAccount},
		"uninstall":   {summary: "Remove the plugin, status line, timer and launchers", run: runUninstall},
		"update":      {summary: "Update wy from a release (--machines updates every added machine)", run: runUpdate},
		"direct-push": {summary: "Send session pushes straight to ntfy when the hub is unreachable", run: runDirectPush},
		"desktop":     {summary: "Hub actions for the desktop app as JSON (info, token, owner-link, hub)", run: runDesktop},
		"board":       {summary: "Session board in this terminal (--once, --json, --follow --ndjson; connect, disconnect)", run: runBoard},
		"term":        {summary: "Terminal pieces: pane, titles, Warp tabs and plugin (setup, titles, warp, off, status, focus)", run: runTerm},
		"open":        {summary: "List accounts, or open one in a new Warp tab or this terminal", run: runOpen},
		"ssh":         {summary: "ssh that lets the server's sessions be focused in this tab", run: runSSH},
		"keys":        {summary: "Owner key, certificates, revocations and pins (init, status, certify, revoke, ...)", run: runKeys},
	}
}

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

// dispatch runs the subcommand named by args[0] and returns the process exit code.
func dispatch(args []string, stdout, stderr io.Writer) int {
	cmds := commands()
	if len(args) == 0 {
		usage(stderr, cmds)
		return 2
	}
	name, rest := args[0], args[1:]
	switch name {
	case "-h", "--help", "help":
		if name == "help" && len(rest) > 0 {
			c, ok := cmds[rest[0]]
			if !ok {
				fmt.Fprintf(stderr, "wy: unknown command %q\n", rest[0])
				return 2
			}
			fmt.Fprintf(stdout, "wy %s: %s\n", rest[0], c.summary)
			return 0
		}
		usage(stdout, cmds)
		return 0
	case "-v", "--version":
		name = "version"
	}
	c, ok := cmds[name]
	if !ok {
		fmt.Fprintf(stderr, "wy: unknown command %q\n\n", name)
		usage(stderr, cmds)
		return 2
	}
	if err := c.run(rest, stdout, stderr); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.err != nil && !errors.Is(ee.err, errUsage) {
				fmt.Fprintf(stderr, "wy %s: %v\n", name, ee.err)
			}
			return ee.code
		}
		if errors.Is(err, flag.ErrHelp) {
			return 0 // -h: the flag package has printed the usage
		}
		if errors.Is(err, errUsage) {
			return 2
		}
		fmt.Fprintf(stderr, "wy %s: %v\n", name, err)
		return 1
	}
	return 0
}

func usage(w io.Writer, cmds map[string]command) {
	fmt.Fprintln(w, "wy: Watchyard, for several Claude accounts on several machines.")
	fmt.Fprintln(w, "Unofficial; not affiliated with Anthropic.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: wy <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	names := make([]string, 0, len(cmds))
	for n := range cmds {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := cmds[n]
		fmt.Fprintf(w, "  %-11s %s\n", n, c.summary)
	}
}

func runVersion(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	short := fs.Bool("short", false, "print only the version number")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "wy version: unexpected argument %q\n", strings.Join(fs.Args(), " "))
		return errUsage
	}
	if *short {
		fmt.Fprintln(stdout, version.Version)
		return nil
	}
	fmt.Fprintln(stdout, version.String())
	return nil
}
