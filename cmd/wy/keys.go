package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	hubcli "github.com/mraj602/watchyard/internal/hub/cli"
	"github.com/mraj602/watchyard/internal/hub/hublock"
	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/paths"
)

// `wy keys` (keys.md 3, 4.2, 6, 8, 11): the owner key on the owner machine, certificates,
// revocations and this machine's pins. Every command that unseals the owner key or replaces
// a pin runs the signing guard first and exits 4 when it refuses.

const keysUsageText = `Usage: wy keys <command> [flags]

Commands:
  init [--replace] [--yes]              make the owner key, print its fingerprint and recovery code, propose it to the hub
  status [--json]                       pins, this device's keys and certificate, the hub's owner key
  certify [<device>…|--all] [--yes]     owner-sign machine and desktop certificates and the accounts each may
                                        speak for in peer messages (passphrase once)
  revoke <device> [--reason R] [--yes]  owner-signed revocation, uploaded; the hub revokes the device
  passwd                                new passphrase for owner.sealed
  recover                               rebuild owner.sealed from the recovery code
  trust-owner <fp>                      replace this machine's owner pin (prints old and new)
  show-owner [--json]                   print the owner public key and fingerprint
  device --json [--renew]               this machine's device id and public link keys, made at first use (wy machine add)
  pin-ssh --json                        pin the hub and owner keys wy machine add hands over SSH on stdin
`

// keysTTY is the controlling terminal: passphrases and the recovery code never go anywhere
// else (keys.md 3.1, 3.2).
type keysTTY interface {
	Password(prompt string) (string, error)
	Line(prompt string) (string, error)
	Print(s string)
	Close() error
}

// keysEnv is what the commands need; tests replace every part.
type keysEnv struct {
	state          string
	stdout, stderr io.Writer
	stdin          io.Reader // pin-ssh's handoff
	stdoutTTY      bool
	guard          func() error
	tty            func() (keysTTY, error)
	hub            func() (*client.Client, error)
	// merged opens the hub on this machine (same state folder, hub_sign.key present);
	// nil service when there is none.
	merged func(ctx context.Context) (*hubkeys.Service, func(), error)
	now    func() time.Time
	rand   io.Reader
	iter   int
	// accounts are this machine's accounts (uuid -> label) for `wy keys status` and the
	// labels certify shows; nil: not known.
	accounts func() map[string]string
}

func runKeys(args []string, stdout, stderr io.Writer) error {
	env, err := realKeysEnv(stdout, stderr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return keysMain(ctx, env, args)
}

func realKeysEnv(stdout, stderr io.Writer) (keysEnv, error) {
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return keysEnv{}, err
	}
	env := keysEnv{
		state: d.State.Path, stdout: stdout, stderr: stderr, stdin: os.Stdin,
		stdoutTTY: term.IsTerminal(int(os.Stdout.Fd())),
		guard:     keys.CheckNotUnderClaude,
		tty:       openKeysTTY,
		now:       time.Now,
		rand:      rand.Reader,
		iter:      keys.DefaultIter,
		accounts: func() map[string]string {
			pe, err := peersEnv()
			if err != nil {
				return nil
			}
			return pe.Accounts()
		},
	}
	env.hub = func() (*client.Client, error) {
		f, err := config.Load(d.Config.Path)
		if err != nil {
			return nil, err
		}
		cfg, err := f.Typed()
		if err != nil {
			return nil, err
		}
		if cfg.Hub.URL == "" {
			return nil, errors.New("this machine is not connected to a hub; run `wy connect` first")
		}
		tok, err := client.ReadToken(d.State.Path)
		if err != nil {
			return nil, fmt.Errorf("no hub token on this machine: %w", err)
		}
		return client.ForConfig(cfg, tok)
	}
	env.merged = func(ctx context.Context) (*hubkeys.Service, func(), error) {
		kp, err := hubkeys.DefaultPath()
		if err != nil {
			return nil, nil, err
		}
		db, err := hubcli.DefaultDB()
		if err != nil {
			return nil, nil, err
		}
		if _, err := os.Stat(db); err != nil {
			return nil, nil, nil
		}
		// A hub that is not running here (a folder left from before the hub moved to another
		// machine, which may keep the same hub key) is not merged mode.
		if held, err := hublock.Held(filepath.Dir(db)); err != nil || !held {
			return nil, nil, nil
		}
		k, err := hubkeys.Load(kp)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		st, err := store.Open(ctx, db, store.Options{})
		if err != nil {
			return nil, nil, err
		}
		return hubkeys.New(st, k), func() { st.Close() }, nil
	}
	return env, nil
}

// devTTY is /dev/tty.
type devTTY struct {
	f *os.File
	r *bufio.Reader
}

func openKeysTTY() (keysTTY, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, errors.New("this needs you at a terminal (no controlling terminal)")
	}
	return &devTTY{f: f, r: bufio.NewReader(f)}, nil
}

func (t *devTTY) Password(prompt string) (string, error) {
	fmt.Fprint(t.f, prompt)
	b, err := term.ReadPassword(int(t.f.Fd()))
	defer clear(b)
	fmt.Fprintln(t.f)
	return string(b), err
}

func (t *devTTY) Line(prompt string) (string, error) {
	fmt.Fprint(t.f, prompt)
	s, err := t.r.ReadString('\n')
	if err != nil && s == "" {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func (t *devTTY) Print(s string) { fmt.Fprint(t.f, s) }
func (t *devTTY) Close() error   { return t.f.Close() }

func keysMain(ctx context.Context, env keysEnv, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(env.stdout, keysUsageText)
		if len(args) == 0 {
			return errUsage
		}
		return nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "status":
		return keysStatus(ctx, env, rest)
	case "show-owner":
		return keysShowOwner(env, rest)
	case "device":
		return keysDevice(ctx, env, rest)
	case "pin-ssh":
		return keysPinSSH(env, rest)
	case "init", "certify", "revoke", "passwd", "recover", "trust-owner":
	default:
		fmt.Fprintf(env.stderr, "wy keys: unknown command %q\n\n%s", sub, keysUsageText)
		return errUsage
	}
	// The signing guard (keys.md 8): before any flag is looked at or a file is read.
	if err := env.guard(); err != nil {
		return exitWith(4, err)
	}
	switch sub {
	case "init":
		return keysInit(ctx, env, rest)
	case "certify":
		return keysCertify(ctx, env, rest)
	case "revoke":
		return keysRevoke(ctx, env, rest)
	case "passwd":
		return keysPasswd(env, rest)
	case "recover":
		return keysRecover(env, rest)
	default:
		return keysTrustOwner(ctx, env, rest)
	}
}

func keysFlags(name string, env keysEnv) *flag.FlagSet {
	fs := flag.NewFlagSet("wy keys "+name, flag.ContinueOnError)
	fs.SetOutput(env.stderr)
	return fs
}

// parseKeysFlags parses flags before and after positional arguments.
func parseKeysFlags(fs *flag.FlagSet, args []string) ([]string, error) {
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

func (env keysEnv) sealedPath() string   { return keys.Path(env.state, keys.OwnerSealedFile) }
func (env keysEnv) ownerPinPath() string { return keys.Path(env.state, keys.OwnerPinFile) }
func (env keysEnv) hubPinPath() string   { return keys.Path(env.state, keys.HubPinFile) }

// readNewPassphrase asks twice and checks the length (keys.md 3.1).
func readNewPassphrase(t keysTTY) (string, error) {
	for range 3 {
		p, err := t.Password("New passphrase (12 characters or more; four or more random words work well): ")
		if err != nil {
			return "", err
		}
		if err := keys.CheckPassphrase(p); err != nil {
			t.Print(err.Error() + "\n")
			continue
		}
		q, err := t.Password("Again: ")
		if err != nil {
			return "", err
		}
		if p != q {
			t.Print("The two did not match.\n")
			continue
		}
		return p, nil
	}
	return "", errors.New("no passphrase set")
}

// unseal opens owner.sealed with a passphrase from the terminal and checks it against this
// machine's owner pin.
func unseal(env keysEnv, t keysTTY) (ed25519.PrivateKey, error) {
	so, err := keys.ReadSealedOwner(env.sealedPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("there is no owner key on this machine (wy keys init, or wy keys recover with the recovery code)")
	}
	if err != nil {
		return nil, err
	}
	pass, err := t.Password(fmt.Sprintf("Passphrase for owner key %s: ", keys.GroupFP(so.FP)))
	if err != nil {
		return nil, err
	}
	priv, err := so.Open(pass)
	if err != nil {
		return nil, err
	}
	if p, err := keys.ReadPin(env.ownerPinPath()); err == nil && p.FP != so.FP {
		clear(priv)
		return nil, fmt.Errorf("owner.sealed holds %s but this machine pins %s; run wy keys trust-owner if you replaced the key",
			keys.GroupFP(so.FP), keys.GroupFP(p.FP))
	}
	return priv, nil
}

// hubTrust is this machine's pinned hub key; when there is none yet it pins the hub's key
// from GET /v1/keys (trust on first use, how tofu) and says so.
func hubTrust(ctx context.Context, env keysEnv, hub *client.Client) (ed25519.PublicKey, string, error) {
	if p, err := keys.ReadPin(env.hubPinPath()); err == nil {
		k, err := p.Key()
		return k, p.FP, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	if hub == nil {
		return nil, "", errors.New("this machine has not pinned a hub key; connect it to the hub first")
	}
	info, err := hub.Keys(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("reading the hub's key: %w", err)
	}
	b, err := keys.UnB64(info.HubKey.Pub)
	if err != nil || len(b) != ed25519.PublicKeySize || keys.Fingerprint(b) != info.HubKey.FP {
		return nil, "", errors.New("the hub sent a malformed key")
	}
	if err := keys.SetPin(env.hubPinPath(), keys.NewPin(b, keys.PinTOFU, env.now())); err != nil {
		return nil, "", err
	}
	fmt.Fprintf(env.stdout, "Pinned the hub key %s (first use; compare with `wy hub keys show` on the hub).\n", keys.GroupFP(info.HubKey.FP))
	return b, info.HubKey.FP, nil
}

func confirmed(t keysTTY, yes bool, prompt string) (bool, error) {
	if yes {
		return true, nil
	}
	a, err := t.Line(prompt + " [y/N] ")
	if err != nil {
		return false, err
	}
	a = strings.ToLower(a)
	return a == "y" || a == "yes", nil
}

// ---- init ----

func keysInit(ctx context.Context, env keysEnv, args []string) error {
	fs := keysFlags("init", env)
	replace := fs.Bool("replace", false, "make a new owner key in place of the current one (every machine must be certified again)")
	yes := fs.Bool("yes", false, "do not ask before proposing (the passphrase is still asked)")
	if pos, err := parseKeysFlags(fs, args); err != nil || len(pos) > 0 {
		return errUsage
	}
	if !env.stdoutTTY {
		return errors.New("wy keys init prints the recovery code, so it needs a terminal as its output")
	}
	t, err := env.tty()
	if err != nil {
		return err
	}
	defer t.Close()
	ks, closeHub, err := env.merged(ctx)
	if err != nil {
		return err
	}
	if ks != nil {
		// A hub database on this machine is merged mode only when it is the hub this machine
		// is connected to; an old or stopped local hub must not take the proposal meant for
		// the real one.
		if hub, err := env.hub(); err == nil {
			if _, fp, err := hubTrust(ctx, env, hub); err == nil && fp != ks.Key.FP {
				closeHub()
				ks, closeHub = nil, nil
			}
		}
	}
	if closeHub != nil {
		defer closeHub()
	}
	var hub *client.Client
	if ks == nil {
		if hub, err = env.hub(); err != nil {
			return err
		}
	}
	var hubFP string
	if ks != nil {
		hubFP = ks.Key.FP
	} else if _, hubFP, err = hubTrust(ctx, env, hub); err != nil {
		return err
	}

	var priv ed25519.PrivateKey
	if so, err := keys.ReadSealedOwner(env.sealedPath()); err == nil && !*replace {
		// A key exists: propose it again (a proposal that expired, or a new hub).
		fmt.Fprintf(env.stdout, "This machine already has owner key %s; proposing it to the hub again (wy keys init --replace makes a new one).\n", keys.GroupFP(so.FP))
		if priv, err = unseal(env, t); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		if *replace && err == nil {
			ok, err := confirmed(t, false, "Replace the owner key "+keys.GroupFP(so.FP)+"? Every machine must then be certified again and re-pin it.")
			if err != nil || !ok {
				return errors.New("not replaced")
			}
		}
		if priv, err = newOwnerKey(env, t, *replace); err != nil {
			return err
		}
	}
	defer clear(priv)
	doc, err := keys.NewOwnerKeyDoc(priv, hubFP, env.now())
	if err != nil {
		return err
	}
	fp := keys.Fingerprint(priv.Public().(ed25519.PublicKey))
	if ks != nil {
		// Merged mode (keys.md 3.3 step 2): the hub is this machine, so the key is active now.
		ok, err := confirmed(t, *yes, "Activate owner key "+keys.GroupFP(fp)+" on the hub on this machine?")
		if err != nil || !ok {
			return errors.New("not activated; run wy keys init again to propose it")
		}
		if _, err := ks.ProposeAndConfirm(ctx, doc); err != nil {
			return keysServiceErr(err)
		}
		fmt.Fprintf(env.stdout, "Owner key %s is active on this hub.\n", keys.GroupFP(fp))
		return nil
	}
	ok, err := confirmed(t, *yes, "Propose owner key "+keys.GroupFP(fp)+" to the hub?")
	if err != nil || !ok {
		return errors.New("not proposed; run wy keys init again to propose it")
	}
	o, err := hub.ProposeOwner(ctx, doc)
	if err != nil {
		return fmt.Errorf("proposing the key: %w", err)
	}
	fmt.Fprintf(env.stdout, "Proposed owner key %s (%s). Confirm it within 24 hours on the board (Settings, Keys) or on the hub machine:\n  wy hub keys confirm-owner %s\n",
		keys.GroupFP(o.FP), o.State, o.FP)
	return nil
}

// newOwnerKey makes the key from a recovery secret, shows the code on the terminal only,
// has the last two groups typed back, then writes owner.sealed and owner.pub.
func newOwnerKey(env keysEnv, t keysTTY, replace bool) (ed25519.PrivateKey, error) {
	pass, err := readNewPassphrase(t)
	if err != nil {
		return nil, err
	}
	r, err := keys.NewRecoverySecret(env.rand)
	if err != nil {
		return nil, err
	}
	defer clear(r[:])
	priv, err := keys.OwnerFromRecovery(r)
	if err != nil {
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	code := keys.RecoveryCode(r)
	t.Print("\nOwner key " + keys.GroupFP(keys.Fingerprint(pub)) + "\n\nRecovery code:\n\n  " + code + "\n\n" +
		"Write this down and keep it offline. Anyone with it has your owner key. It replaces the passphrase\n" +
		"and the file if you lose either.\n\n")
	groups := strings.Split(code, "-")
	want := strings.Join(groups[len(groups)-2:], "-")
	for i := 0; ; i++ {
		got, err := t.Line("Type the last two groups of the code (XXXX-XXXX): ")
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(strings.ReplaceAll(got, " ", ""), want) {
			break
		}
		if i == 2 {
			return nil, errors.New("the code was not typed back; nothing was written (run wy keys init again)")
		}
	}
	seed := priv.Seed()
	defer clear(seed)
	so, err := keys.SealOwner(seed, pass, env.iter, env.now(), env.rand)
	if err != nil {
		return nil, err
	}
	if err := keys.WriteSealedOwner(env.sealedPath(), so); err != nil {
		return nil, err
	}
	pin := keys.NewPin(pub, keys.PinLocal, env.now())
	if replace {
		err = keys.ReplacePin(env.ownerPinPath(), pin)
	} else {
		err = keys.SetPin(env.ownerPinPath(), pin)
	}
	if err != nil {
		return nil, err
	}
	return priv, nil
}

func keysServiceErr(err error) error {
	var ke *hubkeys.Error
	if errors.As(err, &ke) {
		return errors.New(ke.Msg)
	}
	return err
}

// ---- status and show-owner ----

func keysStatus(ctx context.Context, env keysEnv, args []string) error {
	fs := keysFlags("status", env)
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, err := parseKeysFlags(fs, args); err != nil || len(pos) > 0 {
		return errUsage
	}
	type pinJSON struct {
		FP  string `json:"fp"`
		How string `json:"how"`
	}
	out := struct {
		HubPin      *pinJSON `json:"hub_pin"`
		OwnerPin    *pinJSON `json:"owner_pin"`
		OwnerSealed *string  `json:"owner_sealed"`
		Device      *string  `json:"device"`
		SignFP      *string  `json:"sign_fp"`
		BoxFP       *string  `json:"box_fp"`
		HubOwner    *string  `json:"hub_owner"`
		Pending     []string `json:"pending"`
		Certified   *bool    `json:"owner_signed"`
		Reg         *string  `json:"reg"`
		// Accounts are this machine's accounts (uuid -> label), Bound those the owner bound
		// to its certificate (keys.md 4.3; null: none, or a hub that does not say).
		Accounts map[string]string `json:"accounts,omitempty"`
		Bound    *[]string         `json:"bound_accounts"`
		HubError *string           `json:"hub_error,omitempty"`
	}{Pending: []string{}}
	if env.accounts != nil {
		out.Accounts = env.accounts()
	}
	if p, err := keys.ReadPin(env.hubPinPath()); err == nil {
		out.HubPin = &pinJSON{FP: p.FP, How: p.How}
	}
	if p, err := keys.ReadPin(env.ownerPinPath()); err == nil {
		out.OwnerPin = &pinJSON{FP: p.FP, How: p.How}
	}
	if so, err := keys.ReadSealedOwner(env.sealedPath()); err == nil {
		out.OwnerSealed = &so.FP
	}
	if dk, err := keys.ReadDeviceKeys(env.state); err == nil {
		if pub, err := dk.Public(); err == nil {
			sfp, bfp := fpOf(pub.Sign.Pub), fpOf(pub.Box.Pub)
			out.SignFP, out.BoxFP = &sfp, &bfp
		}
		if dk.Device != "" {
			out.Device = &dk.Device
		}
	}
	if hub, err := env.hub(); err == nil {
		if info, err := hub.Keys(ctx); err == nil {
			if info.OwnerKey != nil {
				out.HubOwner = &info.OwnerKey.FP
			}
			for _, p := range info.Pending {
				out.Pending = append(out.Pending, p.FP)
			}
			if out.Device != nil {
				for _, d := range info.Devices {
					if d.ID == *out.Device {
						signed, reg := d.Keys.OwnerSigned, d.Keys.Reg
						out.Certified, out.Reg, out.Bound = &signed, &reg, d.Bound
					}
				}
			}
		} else {
			s := err.Error()
			out.HubError = &s
		}
	}
	if *asJSON {
		enc := json.NewEncoder(env.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	w := env.stdout
	line := func(label, v string) { fmt.Fprintf(w, "%-14s %s\n", label, v) }
	pin := func(p *pinJSON) string {
		if p == nil {
			return "not pinned"
		}
		return keys.GroupFP(p.FP) + " (" + p.How + ")"
	}
	line("hub key", pin(out.HubPin))
	line("owner key", pin(out.OwnerPin))
	if out.OwnerSealed != nil {
		line("owner.sealed", keys.GroupFP(*out.OwnerSealed)+" (this is the owner machine)")
	}
	if out.SignFP != nil {
		line("link keys", "sign "+keys.GroupFP(*out.SignFP)+", box "+keys.GroupFP(*out.BoxFP))
	} else {
		line("link keys", "none yet")
	}
	if out.Certified != nil {
		state := "hub-signed only (wy keys certify on the owner machine)"
		if *out.Certified {
			state = "owner-signed"
		}
		line("certificate", state+", reg "+*out.Reg)
	}
	if len(out.Accounts) > 0 {
		var list []string
		for a, l := range out.Accounts {
			v := a
			if l != "" {
				v += " (" + l + ")"
			}
			if out.Bound != nil && !slices.Contains(*out.Bound, a) {
				v += " not bound"
			}
			list = append(list, v)
		}
		slices.Sort(list)
		line("accounts", strings.Join(list, ", "))
		if out.Bound != nil && len(*out.Bound) == 0 {
			line("", "no accounts bound: other machines refuse this machine's peer messages (wy keys certify on the owner machine)")
		}
	}
	if out.HubOwner != nil {
		v := keys.GroupFP(*out.HubOwner)
		if out.OwnerPin != nil && out.OwnerPin.FP != *out.HubOwner {
			v += " (differs from this machine's pin: check it, then wy keys trust-owner)"
		}
		line("hub's owner", v)
	}
	for _, p := range out.Pending {
		line("pending", keys.GroupFP(p))
	}
	if out.HubError != nil {
		line("hub", *out.HubError)
	}
	return nil
}

func fpOf(pubB64 string) string {
	b, err := keys.UnB64(pubB64)
	if err != nil {
		return ""
	}
	return keys.Fingerprint(b)
}

func keysShowOwner(env keysEnv, args []string) error {
	fs := keysFlags("show-owner", env)
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, err := parseKeysFlags(fs, args); err != nil || len(pos) > 0 {
		return errUsage
	}
	p, err := keys.ReadPin(env.ownerPinPath())
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("this machine has no owner key pinned")
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(env.stdout).Encode(map[string]string{"alg": keys.AlgEd25519, "pub": p.Pub, "fp": p.FP})
	}
	fmt.Fprintf(env.stdout, "%s\n%s\n", keys.GroupFP(p.FP), p.Pub)
	return nil
}

// ---- certify ----

// keysCertify owner-signs machine and desktop certificates (keys.md 4.2 step 2) and the
// machine_accounts document of each (keys.md 4.3): the accounts the hub sees on that machine
// plus those already bound, so its sessions may send peer messages as those accounts.
func keysCertify(ctx context.Context, env keysEnv, args []string) error {
	fs := keysFlags("certify", env)
	all := fs.Bool("all", false, "every machine and desktop without an owner signature or with accounts not yet bound")
	yes := fs.Bool("yes", false, "do not ask (the passphrase is still asked)")
	ids, err := parseKeysFlags(fs, args)
	if err != nil || (*all) == (len(ids) > 0) {
		if err == nil {
			fmt.Fprintln(env.stderr, "usage: wy keys certify <device>… | --all [--yes]")
		}
		return errUsage
	}
	hub, err := env.hub()
	if err != nil {
		return err
	}
	hubPub, _, err := hubTrust(ctx, env, hub)
	if err != nil {
		return err
	}
	info, err := hub.Keys(ctx)
	if err != nil {
		return fmt.Errorf("reading the hub's devices: %w", err)
	}
	machineKind := func(d client.KeyDevice) bool { return d.Kind == "machine" || d.Kind == "desktop" }
	// A hub older than keys.md 4.3 sends neither list: nothing to bind there.
	bindable := func(d client.KeyDevice) bool { return machineKind(d) && d.Bound != nil }
	var certs, binds []client.KeyDevice
	for _, d := range info.Devices {
		named := slices.Contains(ids, d.ID) || slices.Contains(ids, d.Name)
		if named || (*all && !d.Keys.OwnerSigned && machineKind(d)) {
			if !d.Keys.OwnerSigned || named {
				certs = append(certs, d)
			}
		}
		if (named || *all) && bindable(d) && len(d.Unbound()) > 0 {
			binds = append(binds, d)
		}
	}
	if len(certs) == 0 && len(binds) == 0 {
		fmt.Fprintln(env.stdout, "Nothing to certify.")
		return nil
	}
	t, err := env.tty()
	if err != nil {
		return err
	}
	defer t.Close()
	if len(certs) > 0 {
		fmt.Fprintln(env.stdout, "Certificates to sign with the owner key:")
		for _, d := range certs {
			fmt.Fprintf(env.stdout, "  %s  %-8s %-20s reg %-5s sign %s\n", d.ID, d.Kind, d.Name, d.Keys.Reg, keys.GroupFP(d.Keys.SignFP))
		}
		if slices.ContainsFunc(certs, func(d client.KeyDevice) bool { return d.Keys.Reg == keys.RegTOFU }) {
			fmt.Fprintln(env.stdout, "For reg tofu devices, compare the sign fingerprint with `wy keys status` on that machine.")
		}
	}
	var local map[string]string
	if env.accounts != nil {
		local = env.accounts()
	}
	name := func(a string) string {
		if l, ok := local[a]; ok && l != "" {
			return a + " (" + l + ")"
		}
		return a
	}
	bindList := func(d client.KeyDevice) []string {
		var list []string
		if d.Bound != nil {
			list = append(list, *d.Bound...)
		}
		list = append(list, d.Accounts...)
		slices.Sort(list)
		return slices.Compact(list)
	}
	if len(binds) > 0 {
		fmt.Fprintln(env.stdout, "Accounts to bind (sessions of these accounts on that machine may send peer messages):")
		for _, d := range binds {
			var names []string
			for _, a := range bindList(d) {
				names = append(names, name(a))
			}
			fmt.Fprintf(env.stdout, "  %s  %-8s %-20s %s\n", d.ID, d.Kind, d.Name, strings.Join(names, ", "))
		}
		fmt.Fprintln(env.stdout, "These come from what each machine reported; compare them with `wy keys status` on that machine.")
	}
	if ok, err := confirmed(t, *yes, fmt.Sprintf("Sign %d certificate(s) and %d account list(s)?", len(certs), len(binds))); err != nil || !ok {
		return errors.New("nothing signed")
	}
	priv, err := unseal(env, t)
	if err != nil {
		return err
	}
	defer clear(priv)
	ownerFP := keys.Fingerprint(priv.Public().(ed25519.PublicKey))
	if info.OwnerKey == nil || info.OwnerKey.FP != ownerFP {
		return fmt.Errorf("the hub's active owner key is not %s; confirm it first (wy hub keys confirm-owner, or the board)", keys.GroupFP(ownerFP))
	}
	// cert is the device's certificate as the pinned hub key signed it, for the keys listed
	// above only.
	cert := func(d client.KeyDevice) (keys.Cert, error) {
		doc, _, err := hub.DeviceCert(ctx, d.ID)
		if err != nil {
			return keys.Cert{}, err
		}
		// The hub's signature only: an owner signature already on it is not needed here.
		c, err := keys.VerifyCert(doc.Only(keys.ByHub), keys.Trust{Hub: hubPub}, false)
		if err == nil && (c.Body.Device != d.ID || c.Body.SignFP() != d.Keys.SignFP) {
			err = errors.New("the certificate does not match the listed keys")
		}
		return c, err
	}
	failed := 0
	for _, d := range certs {
		c, err := cert(d)
		if err == nil {
			err = hub.Certify(ctx, d.ID, ownerFP, keys.SignDoc(keys.TypeDeviceCert, c.Raw, keys.ByOwner, priv).Sig)
		}
		if err != nil {
			failed++
			fmt.Fprintf(env.stderr, "wy keys certify: %s: %v\n", d.ID, err)
			continue
		}
		fmt.Fprintf(env.stdout, "certified %s (%s)\n", d.ID, d.Name)
	}
	for _, d := range binds {
		c, err := cert(d)
		var doc keys.Doc
		if err == nil {
			doc, err = keys.SignAccounts(keys.NewAccountsBody(c.Body, bindList(d), env.now()), priv)
		}
		if err == nil {
			err = hub.SetAccounts(ctx, d.ID, doc)
		}
		if err != nil {
			failed++
			fmt.Fprintf(env.stderr, "wy keys certify: %s accounts: %v\n", d.ID, err)
			continue
		}
		fmt.Fprintf(env.stdout, "bound %d account(s) to %s (%s)\n", len(bindList(d)), d.ID, d.Name)
	}
	if failed > 0 {
		return fmt.Errorf("%d signature(s) not made", failed)
	}
	return nil
}

// ---- revoke ----

func keysRevoke(ctx context.Context, env keysEnv, args []string) error {
	fs := keysFlags("revoke", env)
	reason := fs.String("reason", "revoked", "why (kept in the signed list)")
	yes := fs.Bool("yes", false, "do not ask (the passphrase is still asked)")
	pos, err := parseKeysFlags(fs, args)
	if err != nil || len(pos) != 1 {
		if err == nil {
			fmt.Fprintln(env.stderr, "usage: wy keys revoke <device id> [--reason R] [--yes]")
		}
		return errUsage
	}
	id := pos[0]
	hub, err := env.hub()
	if err != nil {
		return err
	}
	hubPub, hubFP, err := hubTrust(ctx, env, hub)
	if err != nil {
		return err
	}
	doc, _, err := hub.DeviceCert(ctx, id)
	if err != nil {
		return fmt.Errorf("reading %s's certificate: %w", id, err)
	}
	c, err := keys.VerifyCert(doc, keys.Trust{Hub: hubPub}, false)
	if err != nil || c.Body.Device != id {
		return fmt.Errorf("%s's certificate does not verify with the pinned hub key", id)
	}
	trust, err := keys.PinnedTrust(env.state)
	if err != nil {
		return err
	}
	next := keys.RevocationBody{V: 1, Version: 1, Hub: hubFP}
	if cur, err := hub.Revocations(ctx); err != nil {
		return fmt.Errorf("reading the revocation list: %w", err)
	} else if cur != nil {
		s, err := keys.VerifyRevocations(*cur, trust)
		if err != nil {
			return fmt.Errorf("the hub's revocation list does not verify: %w", err)
		}
		// The device's current keys, not its id: a live tofu device is listed with the keys
		// it replaced (keys.md 4.2 step 3) and must still be revocable.
		if s.Body.RevokedKey(c.Body.SignFP()) && s.Body.RevokedKey(c.Body.BoxFP()) {
			fmt.Fprintf(env.stdout, "%s is already revoked.\n", id)
			return nil
		}
		next.Devices = append(next.Devices, s.Body.Devices...)
		next.Version = s.Body.Version + 1
	}
	now := env.now()
	next.IssuedAt = keys.FormatTime(now)
	next.Devices = append(next.Devices, keys.RevokedDevice{Device: id, Keys: []string{c.Body.SignFP(), c.Body.BoxFP()},
		At: keys.FormatTime(now), Reason: *reason})
	t, err := env.tty()
	if err != nil {
		return err
	}
	defer t.Close()
	fmt.Fprintf(env.stdout, "Revoke %s (%s %s, sign %s): signed list version %d.\n", id, c.Body.Kind, c.Body.Name, keys.GroupFP(c.Body.SignFP()), next.Version)
	if ok, err := confirmed(t, *yes, "Sign and upload?"); err != nil || !ok {
		return errors.New("not revoked")
	}
	priv, err := unseal(env, t)
	if err != nil {
		return err
	}
	defer clear(priv)
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	d := keys.NewDoc(keys.TypeRevocations, raw)
	if err := d.Sign(keys.ByOwner, priv); err != nil {
		return err
	}
	revoked, err := hub.UploadRevocations(ctx, d)
	if err != nil {
		return fmt.Errorf("uploading: %w", err)
	}
	fmt.Fprintf(env.stdout, "revoked %s (the hub revoked %d device(s))\n", id, len(revoked))
	return nil
}

// ---- passwd, recover, trust-owner ----

func keysPasswd(env keysEnv, args []string) error {
	fs := keysFlags("passwd", env)
	if pos, err := parseKeysFlags(fs, args); err != nil || len(pos) > 0 {
		return errUsage
	}
	t, err := env.tty()
	if err != nil {
		return err
	}
	defer t.Close()
	priv, err := unseal(env, t)
	if err != nil {
		return err
	}
	defer clear(priv)
	pass, err := readNewPassphrase(t)
	if err != nil {
		return err
	}
	seed := priv.Seed()
	defer clear(seed)
	so, err := keys.SealOwner(seed, pass, env.iter, env.now(), env.rand)
	if err != nil {
		return err
	}
	if err := keys.WriteSealedOwner(env.sealedPath(), so); err != nil {
		return err
	}
	fmt.Fprintln(env.stdout, "owner.sealed now uses the new passphrase.")
	return nil
}

func keysRecover(env keysEnv, args []string) error {
	fs := keysFlags("recover", env)
	if pos, err := parseKeysFlags(fs, args); err != nil || len(pos) > 0 {
		return errUsage
	}
	t, err := env.tty()
	if err != nil {
		return err
	}
	defer t.Close()
	code, err := t.Password("Recovery code (WYR1-…): ")
	if err != nil {
		return err
	}
	r, err := keys.ParseRecoveryCode(code)
	if err != nil {
		return err
	}
	defer clear(r[:])
	priv, err := keys.OwnerFromRecovery(r)
	if err != nil {
		return err
	}
	defer clear(priv)
	pub := priv.Public().(ed25519.PublicKey)
	fp := keys.Fingerprint(pub)
	p, perr := keys.ReadPin(env.ownerPinPath())
	if perr == nil && p.FP != fp {
		return fmt.Errorf("that code makes owner key %s, but this machine pins %s; nothing was written", keys.GroupFP(fp), keys.GroupFP(p.FP))
	}
	pass, err := readNewPassphrase(t)
	if err != nil {
		return err
	}
	seed := priv.Seed()
	defer clear(seed)
	so, err := keys.SealOwner(seed, pass, env.iter, env.now(), env.rand)
	if err != nil {
		return err
	}
	if err := keys.WriteSealedOwner(env.sealedPath(), so); err != nil {
		return err
	}
	if errors.Is(perr, os.ErrNotExist) {
		if err := keys.SetPin(env.ownerPinPath(), keys.NewPin(pub, keys.PinLocal, env.now())); err != nil {
			return err
		}
	}
	fmt.Fprintf(env.stdout, "Recovered owner key %s into owner.sealed.\n", keys.GroupFP(fp))
	return nil
}

func keysTrustOwner(ctx context.Context, env keysEnv, args []string) error {
	fs := keysFlags("trust-owner", env)
	pos, err := parseKeysFlags(fs, args)
	if err != nil || len(pos) != 1 {
		if err == nil {
			fmt.Fprintln(env.stderr, "usage: wy keys trust-owner <fingerprint>")
		}
		return errUsage
	}
	fp, ok := keys.NormalizeFP(pos[0])
	if !ok {
		return fmt.Errorf("%q is not a key fingerprint", pos[0])
	}
	// The key itself comes from the hub's confirmed owner_key document, checked with the
	// pinned hub key, and must have the fingerprint typed here.
	hub, err := env.hub()
	if err != nil {
		return err
	}
	hubPub, _, err := hubTrust(ctx, env, hub)
	if err != nil {
		return err
	}
	info, err := hub.Keys(ctx)
	if err != nil {
		return fmt.Errorf("reading the hub's owner key: %w", err)
	}
	if info.OwnerKey == nil || info.OwnerKey.FP != fp {
		return fmt.Errorf("the hub's active owner key is not %s", keys.GroupFP(fp))
	}
	o, err := keys.VerifyOwnerKey(info.OwnerKey.Doc, hubPub)
	if err != nil || !o.Confirmed || o.FP != fp {
		return errors.New("the hub's owner key document does not verify with the pinned hub key")
	}
	old := "none"
	if p, err := keys.ReadPin(env.ownerPinPath()); err == nil {
		if p.FP == fp {
			fmt.Fprintf(env.stdout, "This machine already pins %s.\n", keys.GroupFP(fp))
			return nil
		}
		old = keys.GroupFP(p.FP)
	}
	t, err := env.tty()
	if err != nil {
		return err
	}
	defer t.Close()
	fmt.Fprintf(env.stdout, "owner key pin: old %s, new %s\n", old, keys.GroupFP(fp))
	if ok, err := confirmed(t, false, "Replace the pin?"); err != nil || !ok {
		return errors.New("pin not changed")
	}
	if err := keys.ReplacePin(env.ownerPinPath(), keys.NewPin(o.Pub, keys.PinHand, env.now())); err != nil {
		return err
	}
	fmt.Fprintf(env.stdout, "This machine now pins owner key %s.\n", keys.GroupFP(fp))
	return nil
}
