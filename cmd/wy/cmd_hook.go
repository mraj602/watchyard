package main

import (
	"fmt"
	"io"
	"os"

	"github.com/mraj602/watchyard/internal/hook"
	"github.com/mraj602/watchyard/internal/statusline"
)

const hookUsage = `usage: wy hook [<Event>]            record one Claude Code hook event (JSON on stdin)
       wy hook --title <Event>      print the tab title hook JSON, or nothing (terminal.titles)
       wy hook --peers-context <Event>
                                    on SessionStart, print the peers context hook JSON, or nothing
       wy hook --step <Event>       record a tool call's start or end (Smart Account Switch)

Run by Claude Code's hooks; it always exits 0 and never blocks or decides anything (the
peers context only tells a new session that other sessions are reachable).
`

// runHook is `wy hook [event]`, `wy hook --title <Event>` and `wy hook --peers-context
// <Event>`. It never returns an error and writes to stdout only the title or peers-context
// hook's JSON (or the usage for --help): a hook must not block or decide anything. --help, -h and help print usage and touch no state.
func runHook(args []string, stdout, _ io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Fprint(stdout, hookUsage)
			return nil
		case hook.TitleFlag:
			hook.RunTitle(args[1:], os.Stdin, stdout, os.Getenv)
			return nil
		case hook.PeersContextFlag:
			hook.RunPeersContext(args[1:], os.Stdin, stdout, os.Getenv)
			return nil
		case hook.StepFlag:
			hook.RunStep(args[1:], os.Stdin, os.Getenv)
			return nil
		}
	}
	hook.Run(args, os.Stdin, os.Getenv)
	return nil
}

// runStatusline is `wy statusline [--wrap cmd]`.
func runStatusline(args []string, stdout, stderr io.Writer) error {
	return statusline.Run(args, os.Stdin, stdout, stderr, os.Getenv)
}
