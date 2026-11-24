package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/mraj602/watchyard/internal/client"
	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/localtime"
	peerspkg "github.com/mraj602/watchyard/internal/peers"
)

// The matrix commands of `wy peers` (peers.md 5.4, 7, 11): matrix, allow, deny, same-account,
// max-turns, pause, resume, enable, disable, status, audit.
//
// Widening (allow, same-account on, max-turns up, resume) is signed here with the owner key:
// the signing guard, a terminal, the passphrase. Narrowing (deny, same-account off, max-turns
// down, pause) is an edit the hub signs, accepted from any paired device, so it works inside
// a session too. Either way the hub's answer is verified with this machine's pins and stored
// with keys.SaveAcceptedMatrix, which is also what the local evaluator reads.

// peersKeysEnv builds the keys environment (state folder, hub client, terminal, guard) of
// these commands. A variable so tests give a temp state, a fake terminal and a test hub.
var peersKeysEnv = realKeysEnv

// peerAcct is one account of the matrix print: its number, uuid and label.
type peerAcct struct {
	N     int    `json:"n"`
	UUID  string `json:"account"`
	Label string `json:"label"`
}

// peerAccounts numbers the accounts the matrix can name: this machine's (slot labels) and
// those the matrix document already lists, sorted by label then uuid.
func peerAccounts(local, doc map[string]string, allow [][]string) []peerAcct {
	labels := map[string]string{}
	for u, l := range doc {
		labels[u] = l
	}
	for u, l := range local {
		labels[u] = l
	}
	for _, p := range allow {
		for _, u := range p {
			if _, ok := labels[u]; !ok {
				labels[u] = ""
			}
		}
	}
	var out []peerAcct
	for u, l := range labels {
		if l == "" {
			l = u[:min(8, len(u))]
		}
		out = append(out, peerAcct{UUID: u, Label: l})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Label), strings.ToLower(out[j].Label)
		if a != b {
			return a < b
		}
		return out[i].UUID < out[j].UUID
	})
	for i := range out {
		out[i].N = i + 1
	}
	return out
}

// resolveAccount maps a label, the number the matrix print shows or an account uuid to the
// uuid; "*" passes when star is set.
func resolveAccount(list []peerAcct, ref string, star bool) (string, error) {
	if ref == "*" && star {
		return "*", nil
	}
	var byLabel []string
	for _, a := range list {
		if strings.EqualFold(a.Label, ref) {
			byLabel = append(byLabel, a.UUID)
		}
	}
	switch {
	case len(byLabel) == 1:
		return byLabel[0], nil
	case len(byLabel) > 1:
		return "", fmt.Errorf("%q names %d accounts; use the number or the uuid from wy peers matrix", ref, len(byLabel))
	}
	if n, err := strconv.Atoi(ref); err == nil {
		for _, a := range list {
			if a.N == n {
				return a.UUID, nil
			}
		}
	}
	for _, a := range list {
		if a.UUID == ref {
			return a.UUID, nil
		}
	}
	var names []string
	for _, a := range list {
		names = append(names, fmt.Sprintf("%d %s", a.N, a.Label))
	}
	return "", fmt.Errorf("no account %q (known: %s)", ref, strings.Join(names, ", "))
}

// matrixRun is one command's view: the keys environment, this machine's peers environment,
// the hub client and the newest matrix (fetched and verified, else the stored one).
type matrixRun struct {
	ctx    context.Context
	env    keysEnv
	pe     peerEnv
	hub    *client.Client
	hubErr error // why there is no fresh answer from the hub
	cur    *keys.Signed[keys.MatrixBody]
	out    io.Writer
	errw   io.Writer
}

func newMatrixRun(stdout, stderr io.Writer) (*matrixRun, error) {
	pe, err := peersEnv()
	if err != nil {
		return nil, err
	}
	env, err := peersKeysEnv(stdout, stderr)
	if err != nil {
		return nil, err
	}
	return &matrixRun{ctx: context.Background(), env: env, pe: pe, out: stdout, errw: stderr}, nil
}

// sync asks the hub for the matrix (the hub makes version 1 when it has none), verifies it
// with this machine's pins and stores it when keys.AcceptMatrix takes it: the minimal fetch
// of a machine whose link does not deliver `matrix` frames yet. Without a hub it keeps what
// is stored. m.cur is nil when there is no matrix at all.
func (m *matrixRun) sync() {
	if c, err := keys.LoadAcceptedMatrix(m.env.state); err == nil {
		m.cur = c
	}
	hub, err := m.env.hub()
	if err != nil {
		m.hubErr = err
		return
	}
	m.hub = hub
	if _, _, err := hubTrust(m.ctx, m.env, hub); err != nil {
		m.hubErr = err
		return
	}
	info, err := hub.Matrix(m.ctx)
	if err != nil {
		m.hubErr = err
		return
	}
	m.absorb(info.Doc)
}

// absorb verifies and stores a document the hub sent; what it refuses is reported on stderr.
func (m *matrixRun) absorb(d keys.Doc) {
	s, accepted, err := keys.SaveAcceptedMatrix(m.env.state, d, !m.pe.NoHubWiden)
	switch {
	case errors.Is(err, keys.ErrHubWidening):
		fmt.Fprintln(m.errw, "wy: the hub's matrix widens what this machine allows, and peers.hub_may_widen is false; keeping the stored one. Widen with the owner key (wy peers allow).")
		return
	case err != nil:
		m.hubErr = fmt.Errorf("the hub's matrix does not verify: %w", err)
		return
	}
	if accepted {
		m.cur = &s
	}
}

func (m *matrixRun) need() error {
	if m.cur != nil {
		return nil
	}
	if m.hubErr != nil {
		return fmt.Errorf("no matrix on this machine, and the hub could not be asked: %w", m.hubErr)
	}
	return errors.New("no matrix on this machine")
}

func (m *matrixRun) accounts() []peerAcct {
	var doc map[string]string
	var allow [][]string
	if m.cur != nil {
		doc, allow = m.cur.Body.Accounts, m.cur.Body.Allow
	}
	return peerAccounts(m.pe.Env.Accounts(), doc, allow)
}

func sameMatrixContent(a, b keys.MatrixBody) bool {
	return a.Paused == b.Paused && a.SameAccount == b.SameAccount && a.MaxTurns == b.MaxTurns &&
		slices.EqualFunc(a.Allow, b.Allow, slices.Equal[[]string])
}

// guardFirst runs the signing guard before anything else, for commands that always widen.
func (m *matrixRun) guardFirst() error {
	if err := m.env.guard(); err != nil {
		return exitWith(peerspkg.ExitGuard, err)
	}
	return nil
}

// change applies edit to the current matrix. A result that widens is signed with the owner
// key here (what says what: summary); one that only narrows goes to the hub as an edit (or
// the pause call). Nothing changes when the edit changes nothing.
func (m *matrixRun) change(edit client.MatrixEdit, summary string, yes bool) error {
	if err := m.need(); err != nil {
		return err
	}
	cur := m.cur.Body
	next, err := hubkeys.MatrixEdit(edit).Apply(cur, m.env.now())
	if err != nil {
		return err
	}
	if sameMatrixContent(cur, next) {
		fmt.Fprintf(m.out, "nothing to change: %s\n", summary)
		return nil
	}
	if m.hub == nil {
		return fmt.Errorf("this needs the hub: %w", m.hubErr)
	}
	if keys.Widens(cur, next) {
		return m.signAndPut(next, summary, yes)
	}
	var info client.MatrixInfo
	if edit.Paused != nil && len(edit.Allow)+len(edit.Deny) == 0 && edit.SameAccount == nil && edit.MaxTurns == nil {
		info, err = m.hub.PausePeers(m.ctx, *edit.Paused)
	} else {
		e := edit
		e.BaseVersion = &cur.Version
		info, err = m.hub.PutMatrixEdit(m.ctx, e)
	}
	if err != nil {
		return fmt.Errorf("the hub refused: %w", err)
	}
	m.absorb(info.Doc)
	fmt.Fprintf(m.out, "%s: matrix v%d, signed by the hub\n", summary, info.Matrix.Version)
	return nil
}

// signAndPut is the owner-key path (keys.md 5.3): guard, terminal, confirmation, passphrase,
// signature, upload.
func (m *matrixRun) signAndPut(next keys.MatrixBody, summary string, yes bool) error {
	env := m.env
	if err := env.guard(); err != nil {
		return exitWith(peerspkg.ExitGuard, err)
	}
	if _, err := os.Stat(env.sealedPath()); err != nil {
		return exitWith(peerspkg.ExitGuard, fmt.Errorf("there is no owner key on this machine: open More, Peers on the board (%s), signed in as owner, to make this change", m.hub.BaseURL))
	}
	t, err := env.tty()
	if err != nil {
		return exitWith(peerspkg.ExitGuard, err)
	}
	defer t.Close()
	fmt.Fprintf(env.stdout, "%s: matrix v%d, signed with the owner key.\n", summary, next.Version)
	if ok, err := confirmed(t, yes, "Sign and upload?"); err != nil || !ok {
		return errors.New("not changed")
	}
	priv, err := unseal(env, t)
	if err != nil {
		return err
	}
	defer clear(priv)
	next.Accounts = map[string]string{}
	for _, a := range m.accounts() {
		next.Accounts[a.UUID] = a.Label
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	d := keys.NewDoc(keys.TypeMatrix, raw)
	if err := d.Sign(keys.ByOwner, priv); err != nil {
		return err
	}
	info, err := m.hub.PutMatrixDoc(m.ctx, d)
	if err != nil {
		return fmt.Errorf("uploading: %w", err)
	}
	m.absorb(info.Doc)
	fmt.Fprintf(env.stdout, "done: matrix v%d\n", info.Matrix.Version)
	return nil
}

// ---- commands ----

// peersMatrixCmd runs the matrix subcommands; handled reports whether sub is one of them.
func peersMatrixCmd(sub string, args []string, stdout, stderr io.Writer) (handled bool, err error) {
	switch sub {
	case "enable":
		return true, peersEnable(args, stdout, stderr)
	case "disable":
		if len(args) > 0 {
			return true, errUsage
		}
		return true, agentDisable(stdout, stderr)
	case "matrix":
		return true, peersMatrix(args, stdout, stderr)
	case "allow", "deny":
		return true, peersAllowDeny(sub == "allow", args, stdout, stderr)
	case "same-account":
		return true, peersSameAccount(args, stdout, stderr)
	case "max-turns":
		return true, peersMaxTurns(args, stdout, stderr)
	case "pause", "resume":
		return true, peersPauseResume(sub == "pause", args, stdout, stderr)
	case "status":
		return true, peersStatus(args, stdout, stderr)
	case "audit":
		return true, peersAudit(args, stdout, stderr)
	}
	return false, nil
}

// parsePeersFlags parses flags before and after the positional arguments.
func parsePeersFlags(fs *flag.FlagSet, args []string) ([]string, error) {
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

func peersFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("peers "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// peersEnable is `wy peers enable`: wy agent enable, then matrix version 1 when the hub has
// none (asking the hub for the matrix makes it, and this machine stores it).
func peersEnable(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "wy peers enable: unexpected argument %q\n", args[0])
		return errUsage
	}
	if err := agentEnable(stdout, stderr, false); err != nil {
		return err
	}
	m, err := newMatrixRun(stdout, stderr)
	if err != nil {
		return err
	}
	m.sync()
	switch {
	case m.cur != nil:
		fmt.Fprintf(stdout, "matrix v%d (%s): wy peers matrix shows it.\n", m.cur.Body.Version, m.cur.Signer)
	case m.hubErr != nil:
		fmt.Fprintf(stdout, "No matrix yet: the hub could not be asked (%v). Nothing is allowed until this machine has one; run wy peers matrix when it is connected.\n", m.hubErr)
	}
	return nil
}

func peersMatrix(args []string, stdout, stderr io.Writer) error {
	fs := peersFlagSet("matrix", stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, err := parsePeersFlags(fs, args); err != nil || len(pos) > 0 {
		return errUsage
	}
	m, err := newMatrixRun(stdout, stderr)
	if err != nil {
		return err
	}
	m.sync()
	if m.cur == nil {
		msg := "no matrix on this machine, so nothing is allowed"
		if m.hubErr != nil {
			msg += " (the hub could not be asked: " + m.hubErr.Error() + ")"
		}
		return peersExit(peerspkg.ExitPolicy, errors.New(msg))
	}
	if m.hubErr != nil {
		fmt.Fprintf(stderr, "wy: showing the stored matrix; the hub could not be asked: %v\n", m.hubErr)
	}
	if *asJSON {
		return writeMatrixJSON(stdout, m.cur, m.accounts())
	}
	printMatrix(stdout, m.cur, m.accounts())
	return nil
}

func signerLine(s *keys.Signed[keys.MatrixBody]) string {
	fp := ""
	if len(s.Doc.Sigs) > 0 {
		fp = " " + keys.GroupFP(s.Doc.Sigs[0].FP)
	}
	return s.Signer + fp
}

func writeMatrixJSON(w io.Writer, s *keys.Signed[keys.MatrixBody], accts []peerAcct) error {
	b := s.Body
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"version": b.Version, "signer": s.Signer, "signed_by": signerLine(s), "paused": b.Paused,
		"same_account": b.SameAccount, "max_turns": b.MaxTurns, "accounts": accts, "allow": b.Allow,
	})
}

// printMatrix is the grid of peers.md 7.3: rows send, columns receive; = the same account,
// ⇄ both ways, → the row may start a thread with the column, · nothing.
func printMatrix(w io.Writer, s *keys.Signed[keys.MatrixBody], accts []peerAcct) {
	b := s.Body
	names := make([]string, len(accts))
	for i, a := range accts {
		names[i] = fmt.Sprintf("%d %s", a.N, a.Label)
	}
	first := utf8.RuneCountInString(`from \ to`)
	for _, n := range names {
		first = max(first, utf8.RuneCountInString(n))
	}
	pad := func(s string, n int) string { return s + strings.Repeat(" ", max(0, n-utf8.RuneCountInString(s))) }
	center := func(s string, n int) string {
		l := max(0, (n-utf8.RuneCountInString(s))/2)
		return pad(strings.Repeat(" ", l)+s, n)
	}
	var sb strings.Builder
	sb.WriteString(pad(`from \ to`, first))
	for _, n := range names {
		sb.WriteString("   " + n)
	}
	fmt.Fprintln(w, strings.TrimRight(sb.String(), " "))
	for i, from := range accts {
		sb.Reset()
		sb.WriteString(pad(names[i], first))
		for j, to := range accts {
			cell := "·"
			switch {
			case i == j:
				cell = "="
			case b.Allows(from.UUID, to.UUID) && b.Allows(to.UUID, from.UUID):
				cell = "⇄"
			case b.Allows(from.UUID, to.UUID):
				cell = "→"
			}
			sb.WriteString("   " + center(cell, utf8.RuneCountInString(names[j])))
		}
		fmt.Fprintln(w, strings.TrimRight(sb.String(), " "))
	}
	onOff := map[bool]string{true: "on", false: "off"}
	pause := "not paused"
	if b.Paused {
		pause = "PAUSED"
	}
	fmt.Fprintf(w, "matrix v%d · signed by %s · same account: %s · replies without you: %s · %s\n", b.Version, signerLine(s), onOff[b.SameAccount], turnLimitText(b.MaxTurns), pause)
}

// turnLimitText is max_turns as the matrix print says it: a number, or "no limit" for 0.
func turnLimitText(n int) string {
	if n == keys.NoTurnLimit {
		return "no limit"
	}
	return strconv.Itoa(n)
}

// peersAllowDeny is `wy peers allow <from> <to> [--both]` (widening: owner key) and `deny
// <from> <to|'*'> [--both]` (narrowing: hub-signed, works anywhere).
func peersAllowDeny(allow bool, args []string, stdout, stderr io.Writer) error {
	name := map[bool]string{true: "allow", false: "deny"}[allow]
	fs := peersFlagSet(name, stderr)
	both := fs.Bool("both", false, "both directions")
	yes := fs.Bool("yes", false, "do not ask (the passphrase is still asked); narrowing never asks")
	pos, err := parsePeersFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		fmt.Fprintf(stderr, "usage: wy peers %s <from> <to%s> [--both]\n", name, map[bool]string{true: "", false: "|'*'"}[allow])
		return errUsage
	}
	m, err := newMatrixRun(stdout, stderr)
	if err != nil {
		return err
	}
	if allow {
		if err := m.guardFirst(); err != nil {
			return err
		}
	}
	m.sync()
	if err := m.need(); err != nil {
		return err
	}
	list := m.accounts()
	a, err := resolveAccount(list, pos[0], !allow)
	if err != nil {
		fmt.Fprintln(stderr, "wy peers:", err)
		return errUsage
	}
	b, err := resolveAccount(list, pos[1], !allow)
	if err != nil {
		fmt.Fprintln(stderr, "wy peers:", err)
		return errUsage
	}
	if a == b && a != "*" {
		fmt.Fprintln(stderr, "wy peers: that is one account; same_account governs it (wy peers same-account on|off)")
		return errUsage
	}
	if a == "*" {
		fmt.Fprintln(stderr, "wy peers: the first account must be a real one; '*' is for the second")
		return errUsage
	}
	label := func(u string) string {
		for _, x := range list {
			if x.UUID == u {
				return x.Label
			}
		}
		return "every other account"
	}
	arrow := "→"
	if *both {
		arrow = "⇄"
	}
	pairs := [][]string{{a, b}}
	if *both {
		pairs = append(pairs, []string{b, a})
	}
	var e client.MatrixEdit
	if allow {
		e.Allow = pairs
	} else {
		e.Deny = pairs
	}
	verb := map[bool]string{true: "allow", false: "deny"}[allow]
	return m.change(e, fmt.Sprintf("%s %s %s %s", verb, label(a), arrow, label(b)), *yes)
}

func peersSameAccount(args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		fmt.Fprintln(stderr, "usage: wy peers same-account on|off")
		return errUsage
	}
	m, err := newMatrixRun(stdout, stderr)
	if err != nil {
		return err
	}
	m.sync()
	on := args[0] == "on"
	return m.change(client.MatrixEdit{SameAccount: &on}, "same account "+args[0], false)
}

func peersMaxTurns(args []string, stdout, stderr io.Writer) error {
	n, ok := -1, len(args) == 1
	if ok {
		switch strings.ToLower(args[0]) {
		case "none", "no-limit", "off":
			n = keys.NoTurnLimit
		default:
			var err error
			n, err = strconv.Atoi(args[0])
			ok = err == nil && keys.ValidMaxTurns(n)
		}
	}
	if !ok {
		fmt.Fprintf(stderr, "usage: wy peers max-turns <%d-%d|none>   (none or 0: no limit)\n", keys.MinMaxTurns, keys.MaxMaxTurns)
		return errUsage
	}
	m, err := newMatrixRun(stdout, stderr)
	if err != nil {
		return err
	}
	m.sync()
	return m.change(client.MatrixEdit{MaxTurns: &n}, "replies without you: "+turnLimitText(n), false)
}

// peersPauseResume is the kill switch (peers.md 11): pause narrows, any device; resume widens.
func peersPauseResume(pause bool, args []string, stdout, stderr io.Writer) error {
	fs := peersFlagSet("pause", stderr)
	yes := fs.Bool("yes", false, "do not ask (the passphrase is still asked)")
	if pos, err := parsePeersFlags(fs, args); err != nil || len(pos) > 0 {
		return errUsage
	}
	m, err := newMatrixRun(stdout, stderr)
	if err != nil {
		return err
	}
	if !pause {
		if err := m.guardFirst(); err != nil {
			return err
		}
	}
	m.sync()
	if err := m.change(client.MatrixEdit{Paused: &pause}, map[bool]string{true: "pause all peers", false: "resume peers"}[pause], *yes); err != nil {
		return err
	}
	return nil
}

// ---- status and audit ----

// localAuditLine is one line of <state>/peers/audit.jsonl (internal/peers.auditLine).
type localAuditLine struct {
	TS     string `json:"ts"`
	Msg    string `json:"msg"`
	Thread string `json:"thread"`
	From   string `json:"from"`
	To     string `json:"to"`
	FromA  string `json:"from_account"`
	ToA    string `json:"to_account"`
	Size   int    `json:"size"`
	Local  bool   `json:"local"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	// FromKind and Tag mark a trusted sender's line (peers.md 4.1a, 10).
	FromKind string `json:"from_kind,omitempty"`
	Tag      string `json:"tag,omitempty"`
}

// sender is the FROM cell of the local audit table: a session's short id, or the trusted
// sender by name with the tag it sent to (peers.md 7.1).
func (l localAuditLine) sender() string {
	if l.FromKind == peerspkg.ServiceDoorbell {
		s := "doorbell (service)"
		if l.Tag != "" {
			s += " tag:" + l.Tag
		}
		return s
	}
	return shortID(l.From)
}

func readLocalAudit(e peerspkg.Env, since time.Time) []localAuditLine {
	f, err := os.Open(e.AuditPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 16<<20))
	var out []localAuditLine
	for _, ln := range strings.Split(string(b), "\n") {
		var l localAuditLine
		if strings.TrimSpace(ln) == "" || json.Unmarshal([]byte(ln), &l) != nil {
			continue
		}
		if ts, err := time.Parse(time.RFC3339Nano, l.TS); err == nil && ts.Before(since) {
			continue
		}
		out = append(out, l)
	}
	slices.Reverse(out) // newest first
	return out
}

func shortID(s string) string { return s[:min(8, len(s))] }

// peersStatus is `wy peers status [<msg>]`: what became of recent messages on this machine,
// from the local log (inside a session: that session's own messages).
func peersStatus(args []string, stdout, stderr io.Writer) error {
	fs := peersFlagSet("status", stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parsePeersFlags(fs, args)
	if err != nil || len(pos) > 1 {
		return errUsage
	}
	pe, err := peersEnv()
	if err != nil {
		return err
	}
	lines := readLocalAudit(pe.Env, time.Time{})
	session := ""
	if c, rf := pe.Env.Whoami(); rf == nil {
		session = c.Entry.SessionID
	}
	var keep []localAuditLine
	for _, l := range lines {
		if l.Msg == "" {
			continue // a thread resume (peers.md 6.3), not a message; `wy peers audit` shows it
		}
		if session != "" && l.From != session && l.To != session {
			continue
		}
		if len(pos) == 1 && !strings.HasPrefix(l.Msg, pos[0]) {
			continue
		}
		keep = append(keep, l)
		if len(keep) == 20 && len(pos) == 0 {
			break
		}
	}
	if *asJSON {
		if keep == nil {
			keep = []localAuditLine{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(keep)
	}
	state := "on"
	if pe.Env.Off() {
		state = "off (wy peers on)"
	}
	if !pe.Enabled {
		state = "not enabled (wy peers enable)"
	}
	fmt.Fprintf(stdout, "peers: %s", state)
	if s, err := keys.LoadAcceptedMatrix(pe.Env.State); err == nil && s != nil {
		fmt.Fprintf(stdout, " · matrix v%d", s.Body.Version)
		if s.Body.Paused {
			fmt.Fprint(stdout, " · PAUSED")
		}
	} else {
		fmt.Fprint(stdout, " · no matrix")
	}
	fmt.Fprintln(stdout)
	if len(keep) == 0 {
		fmt.Fprintln(stdout, "no messages yet")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tSTATUS\tFROM\tTO\tSIZE\tMSG")
	for _, l := range keep {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", localtime.String(l.TS), l.Status, shortID(l.From), shortID(l.To), l.Size, l.Msg)
	}
	return tw.Flush()
}

// parseSince reads "24h", "90m" or "7d".
func parseSince(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		d, err := strconv.Atoi(n)
		if err != nil || d < 0 {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		return time.Duration(d) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("bad duration %q", s)
	}
	return d, nil
}

// peersAudit is `wy peers audit [--since 24h] [--json]`: metadata only. The hub's log when
// this machine's token may read it (an owner session or viewer token), else this machine's
// local log (peers.md 10).
func peersAudit(args []string, stdout, stderr io.Writer) error {
	fs := peersFlagSet("audit", stderr)
	since := fs.String("since", "24h", "how far back (24h, 90m, 7d)")
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, err := parsePeersFlags(fs, args); err != nil || len(pos) > 0 {
		return errUsage
	}
	d, err := parseSince(*since)
	if err != nil {
		fmt.Fprintln(stderr, "wy peers audit:", err)
		return errUsage
	}
	pe, err := peersEnv()
	if err != nil {
		return err
	}
	from := time.Now().Add(-d)
	var hubEntries []client.PeerAuditEntry
	hubOK := false
	if env, err := peersKeysEnv(stdout, stderr); err == nil {
		if hub, err := env.hub(); err == nil {
			if es, err := hub.PeerAudit(context.Background(), from, 200); err == nil {
				hubEntries, hubOK = es, true
			}
		}
	}
	local := readLocalAudit(pe.Env, from)
	if *asJSON {
		out := map[string]any{"hub": nil, "local": local}
		if local == nil {
			out["local"] = []localAuditLine{}
		}
		if hubOK {
			if hubEntries == nil {
				hubEntries = []client.PeerAuditEntry{}
			}
			out["hub"] = hubEntries
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	// Section titles are plain lines, not table cells, so they do not widen WHEN; each
	// section is its own table.
	if hubOK {
		fmt.Fprintln(stdout, "HUB")
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "WHEN\tEVENT\tBY\tDETAIL")
		for _, e := range hubEntries {
			det, _ := json.Marshal(e.Detail)
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", localtime.String(e.At), e.Event, e.Actor, det)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(stdout, "(the hub's log is not readable with this token; showing this machine's)")
	}
	fmt.Fprintln(stdout, "THIS MACHINE")
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tSTATUS\tFROM → TO\tSIZE")
	for _, l := range local {
		status := l.Status
		if l.Detail != "" {
			status += " (" + l.Detail + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s → %s\t%d\n", localtime.String(l.TS), status, l.sender(), shortID(l.To), l.Size)
	}
	return tw.Flush()
}
