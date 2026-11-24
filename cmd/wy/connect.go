package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/machineinfo"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/timer"
)

// connectDeps is everything `wy connect` and `wy disconnect` take from the machine, so
// tests can use a fake home, a fake service manager and a fake clock.
type connectDeps struct {
	timer  timer.Env
	stdin  io.Reader
	dirs   paths.Dirs
	http   *http.Client // nil: default
	budget time.Duration
}

func realConnectDeps() (connectDeps, error) {
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return connectDeps{}, err
	}
	home, _ := os.UserHomeDir()
	uname := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		uname = u.Username
	}
	wy, err := os.Executable()
	if err == nil {
		if r, rerr := filepath.EvalSymlinks(wy); rerr == nil {
			wy = r
		}
	} else {
		wy = "wy"
	}
	return connectDeps{
		timer: timer.Env{
			GOOS: runtime.GOOS, Home: home, UID: os.Getuid(), User: uname, WyPath: wy,
			StateDir: d.State.Path, Getenv: os.Getenv, Runner: timer.ExecRunner{},
		},
		stdin: os.Stdin,
		dirs:  d,
	}, nil
}

func runConnect(args []string, stdout, stderr io.Writer) error {
	deps, err := realConnectDeps()
	if err != nil {
		return err
	}
	deps.timer.Out = stdout
	return connect(context.Background(), deps, args, stdout, stderr)
}

func runDisconnect(args []string, stdout, stderr io.Writer) error {
	deps, err := realConnectDeps()
	if err != nil {
		return err
	}
	deps.timer.Out = stdout
	return disconnect(deps, args, stdout, stderr)
}

// tokenShape: a wy_ token.
var tokenShape = regexp.MustCompile(`^wy_[mdpbo]_[0-9A-Za-z]{43}$`)

// pairSecretShape is a pairing secret (16 bytes, base64url, 22 characters; pairing.md 1); a
// typed code is digits with optional separators.
var pairSecretShape = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// parseInterspersed parses flags that may come before or after positional arguments, so
// both `wy connect URL --token T` and `wy connect --token T URL` work.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usageErr(err)
		}
		if fs.NArg() == 0 {
			return pos, flagLike(pos)
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// flagLike refuses a positional argument that looks like a long flag ("--dry-run" after a
// "--"): a command must never act with a flag left unread.
func flagLike(pos []string) error {
	for _, a := range pos {
		if len(a) > 2 && strings.HasPrefix(a, "--") {
			return usageErr(fmt.Errorf("%q was not read as a flag (after --?): nothing done", a))
		}
	}
	return nil
}

// connect implements `wy connect <url> [--token T | --token-stdin] [--insecure-lan] [--no-timer]`.
func connect(ctx context.Context, deps connectDeps, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	token := fs.String("token", "", "device token made by `wy hub token create`")
	tokenStdin := fs.Bool("token-stdin", false, "read the token from the first line of stdin")
	insecure := fs.Bool("insecure-lan", false, "allow plain http to a non-Tailscale, non-loopback host")
	code := fs.String("code", "", "pairing code made by `wy hub pair --kind machine` (482-913)")
	noTimer := fs.Bool("no-timer", false, "do not install the OS timer")
	codeStdin := fs.Bool("code-stdin", false, "read a pairing code (digits) or its secret from the first line of stdin")
	keepToken := fs.Bool("keep-token", false, "keep this machine's token and only change hub.url (after a hub move)")
	dial := fs.String("dial", "", "reach the hub through a loopback port or unix:<socket> (an SSH forward); TLS still checks the URL's name")
	reach := fs.String("reach", "", "direct or forward (recorded as hub.reach)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: wy connect <hub url> (--token <token> | --token-stdin | --code <code> | --code-stdin | --keep-token)\n"+
			"                  [--dial 127.0.0.1:P|unix:PATH] [--reach direct|forward] [--insecure-lan] [--no-timer]")
	}
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	ways := 0
	for _, on := range []bool{*token != "", *tokenStdin, *code != "", *codeStdin, *keepToken} {
		if on {
			ways++
		}
	}
	if len(pos) != 1 || ways != 1 {
		if len(pos) == 1 && ways > 1 {
			fmt.Fprintln(stderr, "wy connect: use one of --token, --token-stdin, --code, --code-stdin and --keep-token")
		}
		fs.Usage()
		return errUsage
	}
	switch *reach {
	case "", config.ReachDirect, config.ReachForward:
	default:
		fmt.Fprintln(stderr, "wy connect: --reach must be direct or forward (pull is set up with wy setup --hub pull)")
		return errUsage
	}
	if *dial != "" {
		if err := config.CheckDial(*dial); err != nil {
			return exitWith(exitUsage, err)
		}
		hc, err := client.HTTPClient(*dial, 15*time.Second)
		if err != nil {
			return exitWith(exitUsage, err)
		}
		deps.http = hc
	}
	tok := strings.TrimSpace(*token)
	if *tokenStdin {
		line, rerr := bufio.NewReader(deps.stdin).ReadString('\n')
		if rerr != nil && line == "" {
			return fmt.Errorf("--token-stdin: no token on stdin")
		}
		tok = strings.TrimSpace(line)
	}
	if *codeStdin {
		line, rerr := bufio.NewReader(deps.stdin).ReadString('\n')
		if rerr != nil && line == "" {
			return fmt.Errorf("--code-stdin: no code on stdin")
		}
		*code = strings.TrimSpace(line)
		if *code == "" {
			return fmt.Errorf("--code-stdin: no code on stdin")
		}
	}
	if *keepToken {
		t, err := client.ReadToken(deps.dirs.State.Path)
		if err != nil {
			return fmt.Errorf("--keep-token: this machine has no token yet (%s); connect with a code or token first", filepath.Join(deps.dirs.State.Path, client.TokenFile))
		}
		tok = t
	}
	if *code == "" && (tok == "" || strings.ContainsAny(tok, " \t\r\n")) {
		return errors.New("the token is empty or contains whitespace")
	}
	if *code == "" && !tokenShape.MatchString(tok) {
		fmt.Fprintln(stderr, "wy connect: warning: that does not look like a Watchyard token (wy_<kind>_<43 characters>)")
	}

	url, warn, err := client.CheckURL(pos[0], *insecure)
	if err != nil {
		return err
	}
	if warn != "" {
		fmt.Fprintf(stderr, "wy connect: note: %s\n", warn)
	}

	cl := client.New(url, tok)
	cl.HTTP = deps.http
	if cl.HTTP == nil && *keepToken && *dial == "" {
		// A repoint on a forward machine must reach the hub the way the machine does: through
		// the hub.dial it already has (a forward's TLS still checks the URL's name).
		if cf, lerr := config.Load(deps.dirs.Config.Path); lerr == nil {
			if c, terr := cf.Typed(); terr == nil && c.Hub.Dial != "" {
				c.Hub.URL = url
				c.Hub.InsecureLAN = true // url passed CheckURL above, with --insecure-lan if it needed it
				if fc, ferr := client.ForConfig(c, tok); ferr == nil {
					cl.HTTP = fc.HTTP
				}
			}
		}
	}
	if cl.HTTP == nil {
		cl.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	h, err := cl.Health(ctx)
	if err != nil {
		return fmt.Errorf("cannot reach a hub at %s: %w", url, err)
	}
	fmt.Fprintf(stdout, "Hub %s is up (version %s, schema %d).\n", url, h.Version, h.Schema)

	st := deps.dirs.State.Path
	claimed := false
	if *code != "" {
		// Swap the pairing code for this machine's token (pairing.md 3). Nothing is written
		// until the hub has accepted the code.
		tok, err = claimMachineToken(ctx, deps, url, *code)
		if err != nil {
			return err
		}
		claimed = true
		cl.Token = tok
		fmt.Fprintln(stdout, "Pairing code accepted.")
	}

	// Prove the token with real slot_info records (connect_probe.go).
	if err := probeToken(ctx, deps, cl); err != nil {
		if claimed {
			// The code is spent; keep the token so the claim is not lost.
			tp := filepath.Join(st, client.TokenFile)
			if werr := os.MkdirAll(st, 0o700); werr == nil {
				if werr = atomicfile.Write(tp, []byte(tok+"\n"), 0o600); werr == nil {
					return fmt.Errorf("%w\n(the code was already used; the token was saved in %s, so run `wy connect %s --token-stdin < %s` to finish)",
						err, tp, url, tp)
				}
			}
		}
		return err
	}

	if !*keepToken {
		if err := os.MkdirAll(st, 0o700); err != nil {
			return err
		}
		if err := atomicfile.Write(filepath.Join(st, client.TokenFile), []byte(tok+"\n"), 0o600); err != nil {
			return fmt.Errorf("store token: %w", err)
		}
	}
	f, err := config.Load(deps.dirs.Config.Path)
	if err != nil {
		return err
	}
	if err := f.Set("hub.url", []string{url}); err != nil {
		return err
	}
	if *insecure {
		if err := f.Set("hub.insecure_lan", []string{"true"}); err != nil {
			return err
		}
	}
	// hub.dial and hub.reach are written only when they change, so a plain connect leaves a
	// config.toml exactly as before. --keep-token changes only hub.url (machines.md
	// 8.3) unless --dial or --reach is given: then that is saved too (a repoint of a forward
	// machine passes them), never silently dropped.
	if !*keepToken || *dial != "" || *reach != "" {
		cur := config.Defaults()
		if c, err := f.Typed(); err == nil {
			cur = c
		}
		if (!*keepToken || *dial != "") && *dial != cur.Hub.Dial && (*dial != "" || cur.Hub.Dial != "") {
			if err := f.Set("hub.dial", []string{*dial}); err != nil {
				return err
			}
		}
		want := *reach
		if want == "" {
			want = config.ReachDirect
			if *dial != "" {
				want = config.ReachForward
			}
		}
		if want != cur.Hub.Reach {
			if err := f.Set("hub.reach", []string{want}); err != nil {
				return err
			}
		}
	}
	if err := f.Save(); err != nil {
		return err
	}
	if *keepToken {
		fmt.Fprintf(stdout, "The hub accepted this machine's token. Saved hub.url in %s.\n", f.Path())
	} else {
		fmt.Fprintf(stdout, "Saved hub.url in %s and the token in %s (mode 0600).\n", f.Path(), filepath.Join(st, client.TokenFile))
	}

	cfg, err := f.Typed()
	if err != nil {
		return err
	}
	// What this hub reads (machines.md 7.2), before the first flush uses it; then this
	// machine's machine_info (6.3), so the first flush carries it.
	if _, err := client.RefreshHub(ctx, cl, st, time.Now()); err != nil {
		fmt.Fprintf(stderr, "wy connect: note: %s not updated: %v\n", filepath.Join(st, client.HubFile), err)
	}
	if _, err := machineinfo.Send(machineInfoOptions(deps.dirs, deps.timer.Home, cfg), true); err != nil {
		fmt.Fprintf(stderr, "wy connect: note: machine_info not queued: %v\n", err)
	}
	budget := deps.budget
	if budget <= 0 {
		budget = client.DefaultBudget
	}
	res := flushOnce(ctx, deps.dirs, cfg, cl, flushArgs{wait: true, timeout: budget}, stderr)
	reportFlush(stdout, res)
	if res.Err != nil {
		fmt.Fprintf(stderr, "wy connect: the first flush did not finish (%v); the timer will keep trying.\n", res.Err)
	}

	if *noTimer {
		fmt.Fprintln(stdout, "Timer not installed (--no-timer). Run `wy tick --quiet` every minute yourself.")
		return nil
	}
	tres, terr := timer.Install(deps.timer)
	for _, p := range tres.Files {
		fmt.Fprintf(stdout, "Wrote %s\n", p)
	}
	if terr != nil {
		fmt.Fprintf(stderr, "wy connect: timer not fully installed: %v\n", terr)
		return nil // connected; only the schedule needs the user's hand
	}
	fmt.Fprintln(stdout, "Timer installed: `wy tick --quiet` runs every minute and at boot.")
	return nil
}

// disconnect implements `wy disconnect`: removes the timer, the token and hub.url. The
// outbox is kept, so records wait for the next connect.
func disconnect(deps connectDeps, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("disconnect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "wy disconnect: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	removed, err := timer.Uninstall(deps.timer)
	if err != nil {
		fmt.Fprintf(stderr, "wy disconnect: timer: %v\n", err)
	}
	for _, p := range removed {
		fmt.Fprintf(stdout, "Removed %s\n", p)
	}
	tp := filepath.Join(deps.dirs.State.Path, client.TokenFile)
	if err := os.Remove(tp); err == nil {
		fmt.Fprintf(stdout, "Removed %s\n", tp)
	}
	f, err := config.Load(deps.dirs.Config.Path)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(f.Path()); statErr == nil {
		if err := f.Set("hub.url", []string{""}); err != nil {
			return err
		}
		if err := f.Save(); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Cleared hub.url. Queued records stay in the outbox.")
	}
	return nil
}

// claimMachineToken claims a pairing code as this machine (expect_kind machine) and returns
// the machine token. Refusals are explained in the user's terms.
func claimMachineToken(ctx context.Context, deps connectDeps, url, code string) (string, error) {
	m, err := identity.LoadOrCreateMachine(deps.dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return "", fmt.Errorf("this machine's id: %w", err)
	}
	hc := deps.http
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	req := client.ClaimRequest{Name: identity.ClampMachineName(m.Name), Machine: m.ID, ExpectKind: "machine"}
	var dk keys.DeviceKeys
	var sent keys.DeviceKeysNew
	if pairSecretShape.MatchString(code) {
		// The 128-bit secret wy machine add passes on stdin: a secret claim carries no clear
		// keys (keys.md 7.1, no downgrade of the QR path).
		req.Secret = code
	} else {
		req.Code = code
		// A typed code registers this machine's link keys (reg tofu, keys.md 7.1).
		if dk, _, err = keys.LoadOrCreateDeviceKeys(deps.dirs.State.Path, time.Now()); err != nil {
			return "", fmt.Errorf("this machine's link keys: %w", err)
		}
		if sent, err = dk.Public(); err != nil {
			return "", fmt.Errorf("this machine's link keys: %w", err)
		}
		req.Keys = &sent
	}
	rep, err := client.Claim(ctx, url, hc, req)
	if err == nil && req.Keys != nil && (rep.HubKey != nil || rep.Cert != nil) {
		err = keepClaimedKeys(ctx, deps, url, hc, rep, dk, sent)
		if err != nil {
			return "", err
		}
	}
	if err != nil {
		var se *client.StatusError
		if errors.As(err, &se) {
			switch se.Status {
			case http.StatusUnauthorized:
				return "", errors.New("that code is not valid: it works once, for 10 minutes. Make a new one with `wy hub pair --kind machine` on the hub")
			case http.StatusConflict:
				return "", fmt.Errorf("the hub refused the code: %s", se.Message)
			case http.StatusTooManyRequests:
				return "", fmt.Errorf("too many tries; wait %d seconds and try again", int(max(se.RetryAfter.Seconds(), 1)))
			}
		}
		return "", fmt.Errorf("could not claim the code: %w", err)
	}
	return rep.Token, nil
}

// keepClaimedKeys checks a claim reply (keys.md 7.3) and pins the hub key (tofu) and a
// confirmed owner key. A reply that does not hold up gives the new token back (best effort)
// and fails; so does a hub key other than the one this machine pinned before.
func keepClaimedKeys(ctx context.Context, deps connectDeps, url string, hc *http.Client, rep *client.ClaimReply, dk keys.DeviceKeys, sent keys.DeviceKeysNew) error {
	giveBack := func() { _ = (&client.Client{BaseURL: url, Token: rep.Token, HTTP: hc}).DeleteSelf(ctx) }
	ck, err := client.VerifyClaim(rep, sent, keys.RegTOFU, "")
	if err != nil {
		giveBack()
		return fmt.Errorf("%w; the new token was not kept (if the hub still lists this machine, revoke it on the board)", err)
	}
	if _, err := client.PinClaimed(deps.dirs.State.Path, ck, keys.PinTOFU, time.Now()); err != nil {
		if errors.Is(err, keys.ErrPinned) {
			giveBack()
			old, _ := keys.ReadPin(keys.Path(deps.dirs.State.Path, keys.HubPinFile))
			return fmt.Errorf("this hub's key %s is not the one this machine pinned (%s). If you moved or rebuilt the hub, check the new fingerprint with `wy hub keys show` on the hub and run `wy agent enable --repin-hub`",
				keys.GroupFP(ck.HubFP), keys.GroupFP(old.FP))
		}
		return err
	}
	dk.Device = rep.Device
	return keys.WriteDeviceKeys(deps.dirs.State.Path, dk)
}
