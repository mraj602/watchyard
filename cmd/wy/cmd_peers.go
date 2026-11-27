package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/mraj602/watchyard/internal/grokpeer"
	"io"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mraj602/watchyard/internal/agent"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/paths"
	peerspkg "github.com/mraj602/watchyard/internal/peers"
)

const peersUsage = `usage: wy peers list [--all] [--json] [--tag <tag>]
       wy peers send <to> <text> [--ttl 1h] [--wait 10] [--stdin]
       wy peers send tag:<tag> [--one] [--same-host] --stdin
       wy peers tag add|rm <tag>
       wy peers tags
       wy peers off | on
       wy peers trust [doorbell [--off | --account <a>] [--program <path>] [--yes]]
       wy peers enable | disable | status | audit
       wy peers matrix | allow | deny | same-account | max-turns | pause | resume

Message Claude Code sessions in other accounts through Watchyard (docs/spec/peers.md).
send, list and the tag commands work inside a Claude Code session (list --all also at a terminal).
`

// peersEnv builds the environment of one `wy peers` run. A variable so tests can give a
// temp HOME, runtime dir and fake /proc.
var peersEnv = func() (peerEnv, error) {
	pe := paths.FromOS()
	dirs, err := paths.Resolve(pe)
	if err != nil {
		return peerEnv{}, err
	}
	cfg := config.Defaults()
	if f, err := config.Load(dirs.Config.Path); err == nil {
		cfg, _ = f.Typed() // a broken key falls back to its default; hooks log the cause
	}
	m, err := identity.LoadOrCreateMachine(dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return peerEnv{}, err
	}
	home := pe.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return peerEnv{
		Env: peerspkg.Env{
			Home: home, State: dirs.State.Path, Runtime: os.Getenv("XDG_RUNTIME_DIR"), Machine: m,
			SlotExtra: cfg.Slots.Extra, SlotLabels: cfg.Slots.Labels,
			Proc: peerspkg.NewProc(), StartPID: os.Getppid(), FS: identity.OSFS{},
			PastSlots: movedSlots(dirs.State.Path), MarkerTags: scopeTags(cfg.Account.SmartSwitchScope),
			Hosts: []peerspkg.AgentHost{&grokpeer.Host{State: dirs.State.Path}},
		},
		Enabled: cfg.Peers.Enabled, NoHubWiden: !cfg.Peers.HubMayWiden,
	}, nil
}

// peerEnv is the peers environment plus the config switch that turns the commands on.
type peerEnv struct {
	peerspkg.Env
	Enabled bool
	// NoHubWiden is peers.hub_may_widen = false: this machine refuses a hub-signed matrix that
	// widens (keys.md 5.4). The zero value keeps the default, true.
	NoHubWiden bool
}

func peersExit(code int, err error) error { return &exitError{code: code, err: err} }

// runPeers is `wy peers`: list, send, off and on here, the matrix commands (enable, matrix,
// allow, deny, pause, status, audit, ...) in cmd_peers_matrix.go.
func runPeers(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, peersUsage)
		if len(args) == 0 {
			return errUsage
		}
		return nil
	}
	if runtime.GOOS == "windows" {
		return peersExit(peerspkg.ExitUnsupported, errors.New("peers are not supported on Windows (Claude Code's inbox sockets need a per-session token that Watchyard never reads)"))
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return peersList(rest, stdout, stderr)
	case "send":
		return peersSend(rest, os.Stdin, stdout, stderr)
	case "off", "on":
		return peersOffOn(sub == "off", rest, stdout, stderr)
	case "tag":
		if len(rest) == 0 {
			fmt.Fprint(stderr, peersTagUsage)
			return errUsage
		}
		return peersTagCmd(rest[0], rest[1:], stdout, stderr)
	case "tags":
		return peersTagCmd(peerspkg.TagList, rest, stdout, stderr)
	case "trust":
		return peersTrust(rest, stdout, stderr)
	}
	if handled, err := peersMatrixCmd(sub, rest, stdout, stderr); handled {
		return err
	}
	fmt.Fprintf(stderr, "wy peers: unknown subcommand %q\n\n%s", sub, peersUsage)
	return errUsage
}

func peersList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("peers list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "show every session, with what the matrix allows")
	asJSON := fs.Bool("json", false, "print JSON")
	tag := fs.String("tag", "", "only sessions that hold this tag")
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if *tag != "" {
		if rf := peerspkg.CheckTag(*tag); rf != nil {
			fmt.Fprintf(stderr, "wy peers list: %s\n", rf.Detail)
			return errUsage
		}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "wy peers list: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	pe, err := peersEnv()
	if err != nil {
		return err
	}
	if !pe.Enabled {
		return peersExit(peerspkg.ExitPolicy, errors.New("peers are not enabled on this machine (wy peers enable)"))
	}
	entries, asked, rf := peersListEntries(pe, *all)
	if rf != nil {
		if rf.Code == peerspkg.CodeNotASession {
			return peersExit(peerspkg.ExitGuard, errors.New(rf.Detail))
		}
		return peersExit(peerspkg.ExitCode(rf.Code), rf)
	}
	e := pe.Env
	if *tag != "" {
		entries = filterTag(entries, *tag)
	}
	if *asJSON {
		if entries == nil {
			entries = []peerspkg.ListEntry{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(entries)
	}
	if len(entries) == 0 && *tag != "" {
		fmt.Fprintf(stdout, "No session you may message holds tag %s.\n", *tag)
		return nil
	}
	if len(entries) == 0 {
		if asked && !*all {
			if e.MatrixConfigured() {
				fmt.Fprintln(stdout, "No session you may message right now (the matrix allows none, or no other session is running).")
			} else {
				fmt.Fprintln(stdout, "No session you may message: the matrix is not configured on this machine, so nothing is allowed yet.")
			}
		} else {
			fmt.Fprintln(stdout, "No other live session on this machine.")
		}
		return nil
	}
	return printPeerList(stdout, entries)
}

// printPeerList is the table of `wy peers list`; a blocked session (another machine that
// cannot be reached now) says why, and a closing line says what the owner does about it.
func printPeerList(stdout io.Writer, entries []peerspkg.ListEntry) error {
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	tagged := false
	for _, le := range entries {
		tagged = tagged || len(le.Tags) > 0
	}
	if tagged {
		fmt.Fprintln(tw, "HANDLE\tACCOUNT\tPROJECT\tSTATE\tCAN\tTAGS")
	} else {
		fmt.Fprintln(tw, "HANDLE\tACCOUNT\tPROJECT\tSTATE\tCAN")
	}
	certs := false
	for _, le := range entries {
		can := le.Can
		switch {
		case le.Blocked != "" && can != "none":
			can += " (blocked: " + le.Blocked + ")"
			certs = certs || peerspkg.FixedByCerts(le.Blocked)
		case le.MayAsk && can != "none":
			can += " (may ask for approval)"
		}
		// Local entries carry the full folder, remote ones its base name: show the base name for both.
		if tagged {
			tags := strings.Join(le.Tags, ",")
			if tags == "" {
				tags = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", le.Handle, le.AccountLabel, baseName(le.Project), le.State, can, tags)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", le.Handle, le.AccountLabel, baseName(le.Project), le.State, can)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if certs {
		fmt.Fprintf(stdout, "\nBlocked sessions on other machines cannot be messaged yet: %s.\n", peerspkg.CertFix)
	}
	return nil
}

// peersSend is `wy peers send <to> <text> [--ttl] [--wait] [--stdin]`. Flags may come before
// or after the positionals, since Claude writes it either way.
func peersSend(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("peers send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ttl := fs.Duration("ttl", time.Hour, "how long a queued message may wait (1m-24h; used for other machines)")
	wait := fs.Int("wait", 10, "seconds to wait for a final status (0-60; same-machine sends are final at once)")
	useStdin := fs.Bool("stdin", false, "read the text from standard input")
	one := fs.Bool("one", false, "tag address only: deliver to exactly one holder (idle or done first)")
	sameHost := fs.Bool("same-host", false, "tag address only: only holders on this machine under this OS user")
	pos, err := parsePeersFlags(fs, args)
	if err != nil {
		return err
	}
	if *ttl < time.Minute || *ttl > 24*time.Hour {
		fmt.Fprintln(stderr, "wy peers send: --ttl must be between 1m and 24h")
		return errUsage
	}
	if *wait < 0 || *wait > 60 {
		fmt.Fprintln(stderr, "wy peers send: --wait must be between 0 and 60")
		return errUsage
	}
	if len(pos) == 0 || (*useStdin && len(pos) != 1) || (!*useStdin && len(pos) < 2) {
		fmt.Fprint(stderr, "usage: wy peers send <to> <text> [--ttl 1h] [--wait 10]\n       wy peers send <to> --stdin\n")
		return errUsage
	}
	to := pos[0]
	tag, isTag, bad := peerspkg.ParseTagAddress(to)
	if bad != nil {
		fmt.Fprintf(stderr, "wy peers send: %s\n", bad.Detail)
		return errUsage
	}
	if !isTag && (*one || *sameHost) {
		fmt.Fprintln(stderr, "wy peers send: --one and --same-host go with a tag address (tag:<tag>)")
		return errUsage
	}
	var text string
	if *useStdin {
		// Read one byte past the cap: text cut at the cap could end inside a UTF-8 character
		// and be refused as not_text, when what is wrong is its size.
		limit := 2 * int64(peerspkg.MaxTextBytes)
		b, err := io.ReadAll(io.LimitReader(stdin, limit+1))
		if err != nil {
			return fmt.Errorf("read the text from stdin: %w", err)
		}
		if int64(len(b)) > limit {
			fmt.Fprintln(stdout, peerspkg.Describe(peerspkg.Refuse(peerspkg.CodeTooBig, "the text is over %d bytes; the limit is %d", limit, peerspkg.MaxTextBytes)))
			return peersExit(peerspkg.ExitLimits, nil)
		}
		text = string(b)
	} else {
		text = strings.Join(pos[1:], " ")
	}

	pe, err := peersEnv()
	if err != nil {
		return err
	}
	if !pe.Enabled {
		fmt.Fprintln(stdout, "not delivered: peers are not enabled on this machine. The owner enables them with: wy peers enable. Do not retry.")
		return peersExit(peerspkg.ExitPolicy, nil)
	}
	caller, rf := pe.Env.Whoami()
	if rf != nil {
		if rf.Code == peerspkg.CodeNotASession {
			// Outside any session: only the approved Doorbell may send (peers.md 4.1a), which
			// the agent decides from this process's ancestry.
			return peersSendService(pe, rf, to, text, *useStdin, *one, *sameHost, *ttl, *wait, stdout, stderr)
		}
		return peersExit(peerspkg.ExitCode(rf.Code), errors.New(rf.Detail))
	}
	if isTag {
		return peersSendTag(pe, caller, tag, text, *one, *sameHost, *ttl, *wait, stdout)
	}
	res, rf, err := peersDeliver(pe, caller, to, text, *ttl, *wait)
	if err != nil {
		return err
	}
	if rf != nil {
		fmt.Fprintln(stdout, peerspkg.Describe(rf))
		return peersExit(peerspkg.ExitCode(rf.Code), nil)
	}
	fmt.Fprintln(stdout, res.Line())
	if res.Status == "queued" {
		return peersExit(peerspkg.ExitQueued, nil)
	}
	return nil
}

// peersOffOn is `wy peers off|on`: this machine only, works with no hub (peers.md 11). Like
// the agent's off and on ops it runs at a terminal only (peers.md 2.3): a session must not
// switch the machine's messaging back on.
func peersOffOn(off bool, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "wy peers: unexpected argument %q\n", args[0])
		return errUsage
	}
	pe, err := peersEnv()
	if err != nil {
		return err
	}
	if _, rf := pe.Env.Whoami(); rf == nil || rf.Code != peerspkg.CodeNotASession {
		return peersExit(peerspkg.ExitGuard, fmt.Errorf("wy peers %s runs at a terminal, not inside a session", map[bool]string{true: "off", false: "on"}[off]))
	}
	if err := pe.Env.SetOff(off); err != nil {
		return err
	}
	if off {
		fmt.Fprintln(stdout, "peers are off on this machine: nothing is sent or delivered. wy peers on turns them back on.")
	} else {
		fmt.Fprintln(stdout, "peers are on again on this machine.")
	}
	return nil
}

// peersAgent calls the agent when it runs; agent.ErrNoAgent means it does not, and the caller
// takes the direct path (peers.md 8.5, without the agent). Any other error is final:
// the request may have been handled, so it is never repeated.
func peersAgent(pe peerEnv, req agent.Request) (agent.Reply, error) {
	if c, rf := pe.Env.Whoami(); rf == nil {
		req.ClaudePID, req.ConfigDir = c.ClaudePID, c.Slot.Name
	}
	// A send to another machine waits up to 10 s for the hub and --wait for the result.
	return agent.Call(agentSocket(pe.Env), req, 30*time.Second+time.Duration(req.WaitS)*time.Second)
}

func peersListEntries(pe peerEnv, all bool) ([]peerspkg.ListEntry, bool, *peerspkg.Refusal) {
	rep, err := peersAgent(pe, agent.Request{Op: agent.OpList, All: all})
	switch {
	case err == nil:
		if rf := rep.Refusal(); rf != nil {
			return nil, false, rf
		}
		return rep.Entries, rep.Asked, nil
	case !errors.Is(err, agent.ErrNoAgent):
		return nil, false, peerspkg.Refuse(peerspkg.CodeFailed, "%v", err)
	}
	return pe.Env.List(all)
}

// peersDeliver sends through the agent when it runs, else writes the inbox itself.
func peersDeliver(pe peerEnv, caller peerspkg.Caller, to, text string, ttl time.Duration, wait int) (peerspkg.SendResult, *peerspkg.Refusal, error) {
	if wait == 0 {
		wait = -1 // --wait 0: do not wait (0 on the wire is the default)
	}
	rep, err := peersAgent(pe, agent.Request{Op: agent.OpSend, To: to, Text: text, TTLs: int(ttl / time.Second), WaitS: wait})
	switch {
	case err == nil:
		if rf := rep.Refusal(); rf != nil {
			return peerspkg.SendResult{}, rf, nil
		}
		res := peerspkg.SendResult{Msg: rep.Msg, Thread: rep.Thread, Target: peerspkg.Session{Handle: rep.To}, HeldLikely: rep.HeldLikely, Via: rep.Via}
		if rep.Status == "queued" {
			res.Status, res.Offline, res.Unconfirmed = "queued", rep.Detail == "offline", rep.Detail == "unconfirmed"
			res.Expires, _ = time.Parse(time.RFC3339, rep.Expires)
		}
		return res, nil, nil
	case !errors.Is(err, agent.ErrNoAgent):
		return peerspkg.SendResult{}, nil, fmt.Errorf("wy agent: %w", err)
	}
	res, rf := pe.Env.Send(caller, to, text)
	return res, rf, nil
}
