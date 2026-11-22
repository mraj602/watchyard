package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/agent"
	"github.com/mraj602/watchyard/internal/keys"
	peerspkg "github.com/mraj602/watchyard/internal/peers"
)

// The trusted sender (peers.md 4.1a): `wy peers trust doorbell [--account] [--program]`,
// `wy peers trust doorbell --off` and `wy peers trust`. The CLI runs the signing guard's
// environment and ancestry steps as a courtesy and always asks y/N on the terminal (there is no
// --yes); the agent decides, from the peer credentials and /proc (peers.CheckApprover), then
// resolves the install again and writes the record.

const peersTrustUsage = `usage: wy peers trust doorbell [--account <account>] [--program <path>]
       wy peers trust doorbell --off
       wy peers trust
`

// trustDeps is everything the trust commands touch, injectable for tests.
type trustDeps struct {
	goos string
	// guard runs keys.md 8 steps 1 and 2 (environment names, ancestry); tty step 3 and the
	// question.
	guard    func() error
	tty      func() (keysTTY, error)
	lookPath func(string) (string, error)
	// call reaches the agent (agent.ErrNoAgent when it does not run).
	call func(pe peerEnv, req agent.Request) (agent.Reply, error)
}

var peersTrustDeps = func() trustDeps {
	return trustDeps{
		goos: runtime.GOOS,
		guard: func() error {
			g := keys.DefaultGuard()
			g.OpenTTY = func() error { return nil } // the terminal is asked for separately
			return g.Check()
		},
		tty:      openKeysTTY,
		lookPath: exec.LookPath,
		call: func(pe peerEnv, req agent.Request) (agent.Reply, error) {
			return agent.Call(agentSocket(pe.Env), req, 30*time.Second)
		},
	}
}

func peersTrust(args []string, stdout, stderr io.Writer) error {
	d := peersTrustDeps()
	fs := flag.NewFlagSet("peers trust", flag.ContinueOnError)
	fs.SetOutput(stderr)
	off := fs.Bool("off", false, "revoke the trusted sender on this machine")
	account := fs.String("account", "", "the account it sends as: a label, a number from the list, or a uuid")
	program := fs.String("program", "", "the doorbell console script or its venv (default: doorbell on PATH)")
	pos, err := parsePeersFlags(fs, args)
	if err != nil {
		return err
	}
	if d.goos != "linux" {
		fmt.Fprintln(stdout, "wy peers trust: not supported on this OS yet")
		return peersExit(peerspkg.ExitUnsupported, nil)
	}
	pe, err := peersEnv()
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		if *off || *account != "" || *program != "" {
			fmt.Fprint(stderr, peersTrustUsage)
			return errUsage
		}
		return trustShow(d, pe, stdout)
	}
	if len(pos) != 1 || pos[0] != peerspkg.TrustDoorbell {
		fmt.Fprintf(stderr, "wy peers trust: only %s can be a trusted sender\n\n%s", peerspkg.TrustDoorbell, peersTrustUsage)
		return errUsage
	}
	if *off && (*account != "" || *program != "") {
		fmt.Fprint(stderr, peersTrustUsage)
		return errUsage
	}
	if !pe.Enabled {
		return peersExit(peerspkg.ExitPolicy, errors.New("peers are not enabled on this machine (wy peers enable)"))
	}
	if peerspkg.TrustGuardOff {
		fmt.Fprintln(stderr, "wy: "+peerspkg.TrustTestWarning)
	} else if err := d.guard(); err != nil {
		return peersExit(peerspkg.ExitGuard, trustGuardErr(err))
	}
	if *off {
		return trustOff(d, pe, stdout)
	}
	// The test build asks nothing (its guard is off, and it may run without a terminal).
	return trustSet(d, pe, *program, *account, peerspkg.TrustGuardOff, stdout)
}

// trustGuardErr words a signing-guard refusal for this command.
func trustGuardErr(err error) error {
	var ge *keys.GuardError
	if errors.As(err, &ge) && ge.Reason == "" {
		return fmt.Errorf("refusing: wy peers trust runs inside a Claude Code session (pid %d, %s). Approving or revoking the trusted sender needs you at a terminal that Claude Code did not start", ge.PID, ge.Name)
	}
	return err
}

func trustShow(d trustDeps, pe peerEnv, stdout io.Writer) error {
	rec, err := pe.Env.LoadTrust()
	if rep, cerr := d.call(pe, agent.Request{Op: agent.OpTrust, TrustOp: agent.TrustShow}); cerr == nil && rep.Error == nil {
		rec, err = rep.Trust, nil
	}
	if err != nil {
		return err
	}
	if rec == nil {
		fmt.Fprintln(stdout, "Doorbell is not approved on this machine. To let it wake sessions by tag: wy peers trust doorbell")
		return nil
	}
	printTrust(stdout, *rec)
	return nil
}

func printTrust(w io.Writer, rec peerspkg.TrustRecord) {
	fmt.Fprintf(w, "Doorbell is approved on %s (since %s).\n", rec.MachineName, rec.ApprovedAt.Local().Format("2006-01-02 15:04"))
	if rec.TestBuild {
		fmt.Fprintln(w, "  approved by a test build (its approval guard was off); it is honoured. Approve it again with a release build to replace it.")
	}
	fmt.Fprintf(w, "  program:     %s\n  interpreter: %s\n  venv:        %s\n  sends as:    %s (%s)\n",
		rec.Program, rec.Interpreter, rec.Venv, rec.AccountLabel, rec.Account)
	fmt.Fprintln(w, "It may send only to tags (wy peers send tag:<tag> --stdin). Revoke: wy peers trust doorbell --off, or Settings → Peers on the board.")
}

func trustOff(d trustDeps, pe peerEnv, stdout io.Writer) error {
	rep, err := d.call(pe, agent.Request{Op: agent.OpTrust, TrustOp: agent.TrustOff})
	switch {
	case errors.Is(err, agent.ErrNoAgent):
		// Narrowing needs nobody: remove the record here; the agent tells the hub when it runs.
		had, err := pe.Env.RevokeDoorbell("cli")
		if err != nil {
			return err
		}
		printOff(stdout, had)
		fmt.Fprintln(stdout, "The wy agent is not running; the hub learns of it when the agent starts.")
		return nil
	case err != nil:
		return fmt.Errorf("wy agent: %w", err)
	}
	if rf := rep.Refusal(); rf != nil {
		return peersExit(peerspkg.ExitCode(rf.Code), errors.New(rf.Detail))
	}
	printOff(stdout, rep.OK)
	return nil
}

func printOff(w io.Writer, had bool) {
	if had {
		fmt.Fprintln(w, "Doorbell is no longer a trusted sender on this machine.")
		return
	}
	fmt.Fprintln(w, "Doorbell was not approved on this machine; nothing changed.")
}

// trustSet approves Doorbell; noAsk (the trusttest build only) skips the terminal and the
// questions.
func trustSet(d trustDeps, pe peerEnv, program, account string, noAsk bool, stdout io.Writer) error {
	if program == "" {
		p, err := d.lookPath(peerspkg.DoorbellScript)
		if err != nil {
			return peersExit(peerspkg.ExitUsage, errors.New("doorbell is not on PATH; give its console script or venv with --program"))
		}
		program = p
	}
	in, err := peerspkg.ResolveDoorbellInstall(program)
	if err != nil {
		return peersExit(peerspkg.ExitUsage, fmt.Errorf("this is not a Doorbell install: %w", err))
	}
	accts := pe.Env.ManagedAccounts()
	if len(accts) == 0 {
		return peersExit(peerspkg.ExitPolicy, errors.New("no folder Watchyard manages on this machine is logged in to an account; Doorbell needs one to send as"))
	}
	var tty keysTTY
	if !noAsk {
		t, err := d.tty()
		if err != nil {
			return peersExit(peerspkg.ExitGuard, errors.New("wy peers trust needs you at a terminal"))
		}
		defer t.Close()
		tty = t
	}
	chosen, err := pickTrustAccount(accts, account, tty)
	if err != nil {
		return peersExit(peerspkg.ExitUsage, err)
	}
	plan := fmt.Sprintf("Doorbell on %s:\n  program:     %s\n  interpreter: %s\n  venv:        %s\n  sends as:    %s (%s)\n"+
		"It will be able to wake sessions by sending to tags (never to a session by name), as that account; the matrix and the limits apply.\n",
		pe.Env.Machine.Name, in.Program, in.Interpreter, in.Venv, chosen.Label, chosen.Account)
	if tty != nil {
		tty.Print(plan)
		ok, err := confirmed(tty, false, fmt.Sprintf("Let Doorbell at this path send to tags as %s?", chosen.Label))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(stdout, "Not approved; nothing changed.")
			return nil
		}
	} else {
		fmt.Fprint(stdout, plan)
	}
	rep, err := d.call(pe, agent.Request{Op: agent.OpTrust, TrustOp: agent.TrustSet, Program: in.Program, Account: chosen.Account})
	switch {
	case errors.Is(err, agent.ErrNoAgent):
		return errors.New("the wy agent is not running; start it with wy agent enable, then approve again (Doorbell's sends go through it)")
	case err != nil:
		return fmt.Errorf("wy agent: %w", err)
	}
	if rep.Warning != "" {
		fmt.Fprintln(stdout, "wy: "+rep.Warning)
	}
	if rf := rep.Refusal(); rf != nil {
		return peersExit(peerspkg.ExitCode(rf.Code), errors.New(rf.Detail))
	}
	if rep.Trust == nil {
		return errors.New("the wy agent did not confirm the approval; restart it (wy agent enable)")
	}
	printTrust(stdout, *rep.Trust)
	return nil
}

// pickTrustAccount is --account (a label, its number in the list, or a uuid), else the
// question with the first account as the default; without a terminal the first.
func pickTrustAccount(accts []peerspkg.Party, want string, tty keysTTY) (peerspkg.Party, error) {
	match := func(s string) (peerspkg.Party, bool) {
		s = strings.TrimSpace(s)
		for _, a := range accts {
			if strings.EqualFold(a.Label, s) || a.Account == s {
				return a, true
			}
		}
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(accts) {
			return accts[n-1], true
		}
		return peerspkg.Party{}, false
	}
	if want != "" {
		if a, ok := match(want); ok {
			return a, nil
		}
		return peerspkg.Party{}, fmt.Errorf("no account %q is logged in to a folder Watchyard manages here", want)
	}
	if tty == nil || len(accts) == 1 {
		return accts[0], nil
	}
	var b strings.Builder
	b.WriteString("Accounts on this machine:\n")
	for i, a := range accts {
		fmt.Fprintf(&b, "  %d %s (%s)\n", i+1, a.Label, a.Account)
	}
	tty.Print(b.String())
	for range 3 {
		ans, err := tty.Line("Send as which account? [1] ")
		if err != nil {
			return peerspkg.Party{}, err
		}
		if ans == "" {
			return accts[0], nil
		}
		if a, ok := match(ans); ok {
			return a, nil
		}
		tty.Print("Type a number from the list, or press Enter for 1.\n")
	}
	return peerspkg.Party{}, errors.New("no account chosen")
}

// peersSendService is the trusted sender's path of `wy peers send` (peers.md 4.1a): this
// process is in no session (notASession is 4.1's refusal), so the agent is asked whether it is
// the approved Doorbell. Anything else keeps 4.1's refusal, with the agent's reason.
func peersSendService(pe peerEnv, notASession *peerspkg.Refusal, to, text string, useStdin, one, sameHost bool,
	ttl time.Duration, wait int, stdout, stderr io.Writer) error {
	if runtime.GOOS != "linux" {
		return peersExit(peerspkg.ExitGuard, errors.New(notASession.Detail))
	}
	if wait == 0 {
		wait = -1
	}
	rep, err := agent.Call(agentSocket(pe.Env), agent.Request{Op: agent.OpServiceSend, To: to, Text: text, One: one, SameHost: sameHost,
		Stdin: useStdin, TTLs: int(ttl / time.Second), WaitS: wait}, 30*time.Second+time.Duration(max(wait, 0))*time.Second)
	switch {
	case errors.Is(err, agent.ErrNoAgent):
		return peersExit(peerspkg.ExitGuard, errors.New(notASession.Detail))
	case err != nil:
		return fmt.Errorf("wy agent: %w", err)
	}
	if rf := rep.Refusal(); rf != nil {
		switch rf.Code {
		case peerspkg.CodeNotASession:
			// 4.1's own reason first, then why the agent did not take it as Doorbell.
			return peersExit(peerspkg.ExitGuard, errors.New(notASession.Detail+"; "+
				strings.TrimPrefix(rf.Detail, "not inside a Claude Code session, and ")))
		case peerspkg.CodeUsage:
			fmt.Fprintln(stderr, "wy peers send: "+rf.Detail)
			return errUsage
		case peerspkg.CodeNoHolder:
			fmt.Fprintln(stdout, "not delivered: "+rf.Detail+".")
			return peersExit(peerspkg.ExitCode(rf.Code), nil)
		}
		fmt.Fprintln(stdout, peerspkg.Describe(rf))
		return peersExit(peerspkg.ExitCode(rf.Code), nil)
	}
	for _, l := range rep.Lines {
		if rf := l.Refusal(); rf != nil {
			fmt.Fprintf(stdout, "%s: %s\n", l.To, peerspkg.Describe(rf))
			continue
		}
		res := peerspkg.SendResult{Msg: l.Msg, Thread: l.Thread, Target: peerspkg.Session{Handle: l.To}, HeldLikely: l.HeldLikely}
		if l.Status == "queued" {
			res.Status, res.Offline, res.Unconfirmed = "queued", l.Detail == "offline", l.Detail == "unconfirmed"
			res.Expires, _ = time.Parse(time.RFC3339, l.Expires)
		}
		fmt.Fprintln(stdout, res.Line())
	}
	if rep.Exit != 0 {
		return peersExit(rep.Exit, nil)
	}
	return nil
}
