package peers

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
)

// The trusted sender (peers.md 4.1a): exactly one program that is not a session may send, the
// Doorbell (the agent-doorbell Python package), after the owner approved it at a terminal. The
// agent checks it on every send from the socket's peer credentials and /proc only.
const (
	// TrustDoorbell is the one trusted sender's name (the record's key, peer_trust.sender).
	TrustDoorbell = "doorbell"
	// ServiceDoorbell is its sender kind in audit lines, peer_log and envelopes (from.kind).
	ServiceDoorbell = "service:doorbell"
	// DoorbellDist is the Python distribution that must be installed in the recorded venv.
	DoorbellDist = "agent-doorbell"
	// DoorbellScript is the console script and DoorbellModule the package `python -m` runs.
	DoorbellScript = "doorbell"
	DoorbellModule = "doorbell"
	// DoorbellNoReplies is the header's second line for a message from Doorbell.
	DoorbellNoReplies = "Doorbell is a program, not a session: it reads no replies."
)

// TrustRecord is <state>/peers/trusted.json's Doorbell entry. Every path is resolved (no
// symlinks). A record made by any build, the trusttest build included, is valid for every build
// (a test build's is marked TestBuild).
type TrustRecord struct {
	// Venv is the virtual environment that holds agent-doorbell; Program its bin/doorbell
	// console script; Interpreter the real file its venv python resolves to.
	Venv        string `json:"venv"`
	Program     string `json:"program"`
	Interpreter string `json:"interpreter"`
	// Machine is this machine's id (a record copied to another machine is not valid there).
	Machine     string `json:"machine"`
	MachineName string `json:"machine_name,omitempty"`
	// Account is the account uuid it sends as; AccountLabel is for display.
	Account      string    `json:"account"`
	AccountLabel string    `json:"account_label,omitempty"`
	ApprovedAt   time.Time `json:"approved_at"`
	// TestBuild: a trusttest build (its approval guard off) made the record. Every build
	// honours it and shows it as "approved by a test build" (peers.md 15).
	TestBuild bool `json:"test_build,omitempty"`
}

type trustFile struct {
	V        int          `json:"v"`
	Doorbell *TrustRecord `json:"doorbell,omitempty"`
}

// TrustPath is <state>/peers/trusted.json.
func (e Env) TrustPath() string { return filepath.Join(e.State, "peers", "trusted.json") }

// LoadTrust reads the Doorbell record; nil when there is none. A file that does not parse
// is an error (no record is assumed then: a send is refused).
func (e Env) LoadTrust() (*TrustRecord, error) {
	b, err := os.ReadFile(e.TrustPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f trustFile
	if err := json.Unmarshal(b, &f); err != nil || f.V != 1 {
		return nil, fmt.Errorf("%s is not a trust record", e.TrustPath())
	}
	if f.Doorbell == nil || f.Doorbell.Venv == "" || f.Doorbell.Interpreter == "" || f.Doorbell.Account == "" {
		return nil, nil
	}
	return f.Doorbell, nil
}

// SaveTrust writes the record (0600, folder 0700); nil removes it.
func (e Env) SaveTrust(rec *TrustRecord) error {
	if rec == nil {
		err := os.Remove(e.TrustPath())
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.TrustPath()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(trustFile{V: 1, Doorbell: rec}, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(e.TrustPath(), append(b, '\n'), 0o600)
}

// ServiceSessionID is the trusted sender's pseudo-session on a machine (peers.md 4.1a): the
// sending session of its threads, rates and audit lines. It matches the wrapper's
// from-session pattern.
func ServiceSessionID(machineID string) string { return "svc-doorbell-" + machineID }

// ServiceName is how a message from Doorbell names it: "doorbell (service) on <machine>".
func ServiceName(machineName string) string {
	return "doorbell (service) on " + CleanName(machineName)
}

// ServiceHeader is the two header lines of a message from Doorbell (peers.md 4.1a, 8.5).
func ServiceHeader(machine, label, tag, thread string) string {
	machine, label, tag, thread = CleanName(machine), CleanName(label), CleanName(tag), CleanName(thread)
	return fmt.Sprintf("[Watchyard] from %s · account %s · tag %s · thread %s\n%s", ServiceName(machine), label, tag, thread, DoorbellNoReplies)
}

// ---- the install ----

// DoorbellInstall is a resolved Doorbell install (peers.md 4.1a step 3).
type DoorbellInstall struct {
	Venv, Program, Interpreter string
}

// ResolveDoorbellInstall finds the install from path: the console script (any symlink to it,
// such as ~/.local/bin/doorbell) or the venv folder. Every symlink is resolved; the venv must
// hold pyvenv.cfg and the agent-doorbell distribution as installed (CheckDoorbellInstall), the
// script must be the venv's bin/doorbell and its launcher, and the interpreter is the one the script's first line names (a #!/bin/sh
// trampoline means <venv>/bin/python), resolved to the real file.
func ResolveDoorbellInstall(path string) (DoorbellInstall, error) {
	if path == "" {
		return DoorbellInstall{}, errors.New("no path to Doorbell given")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return DoorbellInstall{}, fmt.Errorf("%s: %w", path, err)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return DoorbellInstall{}, err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return DoorbellInstall{}, err
	}
	var in DoorbellInstall
	if fi.IsDir() {
		in.Venv = real
		in.Program = filepath.Join(real, "bin", DoorbellScript)
		p, err := filepath.EvalSymlinks(in.Program)
		if err != nil {
			return DoorbellInstall{}, fmt.Errorf("%s has no bin/%s console script", real, DoorbellScript)
		}
		in.Program = p
	} else {
		in.Program = real
		in.Venv = filepath.Dir(filepath.Dir(real))
	}
	if filepath.Base(in.Program) != DoorbellScript || filepath.Dir(in.Program) != filepath.Join(in.Venv, "bin") {
		return DoorbellInstall{}, fmt.Errorf("%s is not a virtual environment's bin/%s", in.Program, DoorbellScript)
	}
	if st, err := os.Stat(filepath.Join(in.Venv, "pyvenv.cfg")); err != nil || st.IsDir() {
		return DoorbellInstall{}, fmt.Errorf("%s is not a Python virtual environment (no pyvenv.cfg)", in.Venv)
	}
	if err := CheckDoorbellInstall(in.Venv, in.Program); err != nil {
		return DoorbellInstall{}, err
	}
	interp, err := shebangInterpreter(in.Program, in.Venv)
	if err != nil {
		return DoorbellInstall{}, err
	}
	ri, err := filepath.EvalSymlinks(interp)
	if err != nil {
		return DoorbellInstall{}, fmt.Errorf("the interpreter %s: %w", interp, err)
	}
	if st, err := os.Stat(ri); err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
		return DoorbellInstall{}, fmt.Errorf("the interpreter %s is not an executable file", ri)
	}
	in.Interpreter = ri
	return in, nil
}

// shebangInterpreter is the Python the console script's first line runs: `#!<python> [args]`,
// or `<venv>/bin/python` for the `#!/bin/sh` trampoline some installers write.
func shebangInterpreter(program, venv string) (string, error) {
	f, err := os.Open(program)
	if err != nil {
		return "", err
	}
	defer f.Close()
	line, _ := bufio.NewReaderSize(f, 1024).ReadString('\n')
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "#!") {
		return "", fmt.Errorf("%s does not start with an interpreter line", program)
	}
	fields := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(fields) == 0 {
		return "", fmt.Errorf("%s has an empty interpreter line", program)
	}
	interp := fields[0]
	switch b := filepath.Base(interp); {
	case strings.HasPrefix(b, "python"):
		if !filepath.IsAbs(interp) {
			return "", fmt.Errorf("%s names a relative interpreter", program)
		}
		return interp, nil
	case b == "sh" || b == "bash":
		return filepath.Join(venv, "bin", "python"), nil
	}
	return "", fmt.Errorf("%s runs %s, not the venv's Python; give --program the venv folder", program, interp)
}

// HasDoorbellDist reports whether venv holds the agent-doorbell distribution: a
// lib/python*/site-packages/agent_doorbell-*.dist-info whose METADATA names it.
func HasDoorbellDist(venv string) bool {
	metas, _ := filepath.Glob(filepath.Join(venv, "lib", "python*", "site-packages", "agent_doorbell-*.dist-info", "METADATA"))
	for _, m := range metas {
		f, err := os.Open(m)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for i := 0; i < 64 && sc.Scan(); i++ {
			if name, ok := strings.CutPrefix(sc.Text(), "Name:"); ok {
				if normDist(name) == normDist(DoorbellDist) {
					f.Close()
					return true
				}
				break
			}
		}
		f.Close()
	}
	return false
}

// normDist normalises a distribution name (PEP 503: case, and runs of -_. are one -).
func normDist(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if r == '-' || r == '_' || r == '.' {
			if !dash {
				b.WriteByte('-')
			}
			dash = true
			continue
		}
		dash = false
		b.WriteRune(r)
	}
	return b.String()
}

// ---- the identity check ----

// ProcDetail is the part of the process reader the trusted sender's checks need on top of
// Proc: the whole command line, the working folder, the stat fields and (for the approval
// guard only) the environment's names, never a value. LinuxProc has it; macOS's ps reader does
// not, so the trusted sender is Linux only (peers.md 4.1a, 14).
type ProcDetail interface {
	Argv(pid int) ([]string, error)
	Cwd(pid int) (string, error)
	Stat(pid int) (ProcStat, error)
	EnvNames(pid int) ([]string, error)
}

// Argv is /proc/<pid>/cmdline split on NUL.
func (l LinuxProc) Argv(pid int) ([]string, error) {
	b, err := os.ReadFile(l.path(pid, "cmdline"))
	if err != nil {
		return nil, err
	}
	s := strings.TrimRight(string(b), "\x00")
	if s == "" {
		return nil, fmt.Errorf("proc %d: empty command line", pid)
	}
	return strings.Split(s, "\x00"), nil
}

// Cwd is readlink /proc/<pid>/cwd.
func (l LinuxProc) Cwd(pid int) (string, error) { return os.Readlink(l.path(pid, "cwd")) }

// PythonRun is what a Python command line runs: a script path, or a module (-m).
type PythonRun struct {
	Script, Module string
	// Isolated: -I or -P, so the working folder is not on sys.path.
	Isolated bool
}

// ParsePythonArgv reads a Python command line (argv[0] is the interpreter): the options before
// the script or -m are skipped (-X and -W take a value; a cluster such as -Es is read letter by
// letter). ok is false for -c, -, or nothing to run.
func ParsePythonArgv(argv []string) (run PythonRun, ok bool) {
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--":
			if i+1 < len(argv) && argv[i+1] != "-" {
				return PythonRun{Script: argv[i+1], Isolated: run.Isolated}, true
			}
			return run, false
		case a == "-":
			return run, false
		case !strings.HasPrefix(a, "-"):
			run.Script = a
			return run, true
		case strings.HasPrefix(a, "--"):
			if a == "--check-hash-based-pycs" {
				i++
			}
			continue
		}
		flags := a[1:]
	cluster:
		for j := 0; j < len(flags); j++ {
			switch flags[j] {
			case 'c':
				return run, false
			case 'm':
				mod := flags[j+1:]
				if mod == "" {
					if i+1 >= len(argv) {
						return run, false
					}
					mod = argv[i+1]
				}
				run.Module = mod
				return run, mod != ""
			case 'X', 'W':
				if j == len(flags)-1 {
					i++ // the value is the next argument
				}
				break cluster // else the rest of the cluster is the value
			case 'I', 'P':
				run.Isolated = true
			}
		}
	}
	return run, false
}

// MatchesDoorbell reports whether process pi runs the recorded Doorbell (peers.md 4.1a step
// 3): its resolved executable is the recorded interpreter, and its command line runs the
// venv's bin/doorbell, or `-m doorbell` with argv[0] in the venv's bin/ and no doorbell
// package in its working folder unless isolated. reason says why not.
func MatchesDoorbell(p Proc, pi ProcInfo, rec TrustRecord) (ok bool, reason string) {
	d, has := p.(ProcDetail)
	if !has {
		return false, "this OS cannot read a process's command line here"
	}
	if pi.Exe == "" {
		return false, "its program cannot be read"
	}
	exe := pi.Exe
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if filepath.Clean(exe) != filepath.Clean(rec.Interpreter) {
		return false, "not the recorded interpreter"
	}
	argv, err := d.Argv(pi.PID)
	if err != nil {
		return false, "its command line cannot be read"
	}
	run, ok := ParsePythonArgv(argv)
	if !ok {
		return false, "it does not run a script or module"
	}
	cwd, _ := d.Cwd(pi.PID)
	abs := func(s string) string {
		if filepath.IsAbs(s) {
			return filepath.Clean(s)
		}
		if cwd == "" {
			return ""
		}
		return filepath.Join(cwd, s)
	}
	bin := filepath.Join(rec.Venv, "bin")
	if run.Script != "" {
		s := abs(run.Script)
		if s == "" {
			return false, "its working folder cannot be read"
		}
		r, err := filepath.EvalSymlinks(s)
		if err != nil {
			return false, "its script cannot be resolved"
		}
		if r != filepath.Join(bin, DoorbellScript) || !within(r, rec.Venv) {
			return false, "its script is not the recorded venv's bin/doorbell"
		}
		return true, ""
	}
	if run.Module != DoorbellModule {
		return false, "it runs another module"
	}
	if !strings.Contains(argv[0], "/") {
		return false, "its interpreter was found on PATH, which is not read"
	}
	a0 := abs(argv[0])
	if a0 == "" {
		return false, "its working folder cannot be read"
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(a0))
	if err != nil || dir != bin {
		return false, "its interpreter is not in the recorded venv"
	}
	if !run.Isolated {
		if cwd == "" {
			return false, "its working folder cannot be read"
		}
		for _, shadow := range []string{DoorbellModule, DoorbellModule + ".py"} {
			if _, err := os.Lstat(filepath.Join(cwd, shadow)); err == nil {
				return false, "its working folder holds another doorbell package"
			}
		}
	}
	return true, ""
}

// within reports whether path is inside dir (both clean and absolute).
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

// FindDoorbell walks up from start (itself included, at most MaxWalk steps) to the nearest
// process that runs the recorded Doorbell; 0 and the last reason when none does.
func FindDoorbell(p Proc, start int, rec TrustRecord) (pid int, reason string) {
	return findDoorbellIn(p, Ancestors(p, start), rec)
}

func findDoorbellIn(p Proc, walk []ProcInfo, rec TrustRecord) (pid int, reason string) {
	reason = "no Doorbell process above this command"
	for _, a := range walk {
		ok, why := MatchesDoorbell(p, a, rec)
		if ok {
			return a.PID, ""
		}
		if why != "not the recorded interpreter" {
			reason = why
		}
	}
	return 0, reason
}

// TrustedSender identifies the requester (StartPID, from the socket's peer credentials) as the
// approved Doorbell (peers.md 4.1a). Nothing the requester says is read: only the process
// tree, the record and the venv. The walk must be complete, and every process it read (the
// requester and Doorbell among them) must be unchanged at the end. Any refusal is
// not_a_session, so the caller keeps the answer of 4.1 with this reason added.
func (e Env) TrustedSender() (TrustRecord, *Refusal) {
	no := func(format string, a ...any) (TrustRecord, *Refusal) {
		return TrustRecord{}, Refuse(CodeNotASession, "not inside a Claude Code session, and not the approved Doorbell: "+format, a...)
	}
	if e.Proc == nil {
		return no("this OS is not supported")
	}
	d, ok := e.Proc.(ProcDetail)
	if !ok {
		return no("the trusted sender is checked on Linux only")
	}
	walk := snapWalk(e.Proc, d, e.StartPID)
	if c := walk.claude(); c != nil {
		return no("a Claude Code process (pid %d) is above this command", c.PID)
	}
	if !walk.complete {
		return no("the processes above this command cannot all be read, so a session above it cannot be ruled out")
	}
	rec, err := e.LoadTrust()
	if err != nil {
		return no("%v", err)
	}
	if rec == nil {
		return no("no trusted sender is approved on this machine (wy peers trust doorbell)")
	}
	if rec.Machine != e.Machine.ID {
		return no("the trust record was made on another machine; approve Doorbell again here")
	}
	if !e.accountHere(rec.Account) {
		return no("the account it sends as is no longer logged in to a folder on this machine; approve it again")
	}
	if err := CheckDoorbellInstall(rec.Venv, rec.Program); err != nil {
		return no("%s no longer holds the install that was approved (%v); approve it again", rec.Venv, err)
	}
	if pid, why := findDoorbellIn(e.Proc, walk.procs, *rec); pid == 0 {
		return no("%s", why)
	}
	if !walk.stable(d) {
		return no("a process changed while it was checked")
	}
	if e.PeerAlive != nil && !e.PeerAlive() {
		return no("this command ended while it was checked")
	}
	return *rec, nil
}

// SameApproval reports whether b is still the approval a: the same venv, account and time.
func SameApproval(a, b TrustRecord) bool {
	return a.Venv == b.Venv && a.Program == b.Program && a.Interpreter == b.Interpreter &&
		a.Account == b.Account && a.ApprovedAt.Equal(b.ApprovedAt)
}

// accountHere reports whether a managed slot of this machine is logged in to account.
func (e Env) accountHere(account string) bool {
	_, ok := e.localLabels()[account]
	return ok
}

// ManagedAccounts are the accounts this machine's managed slots are logged in to, in slot
// order, each with the label this machine shows for it (the first slot's). The first is the
// trusted sender's default account (peers.md 4.1a step 4).
func (e Env) ManagedAccounts() []Party {
	var out []Party
	seen := map[string]bool{}
	login := e.loginReader()
	for _, slot := range e.Slots() {
		l, err := login(slot)
		if err != nil || !l.HasSeat || l.Seat.Account == "" || seen[l.Seat.Account] {
			continue
		}
		seen[l.Seat.Account] = true
		out = append(out, Party{Account: l.Seat.Account, Label: e.accountLabel(slot.Name)})
	}
	return out
}

// ApproveDoorbell resolves the install from program again (never trusting a caller's
// resolution) and writes the record for account (peers.md 4.1a step 6). The caller has run the
// guard.
func (e Env) ApproveDoorbell(program, account string) (TrustRecord, error) {
	if _, ok := e.Proc.(ProcDetail); !ok {
		return TrustRecord{}, errors.New("not supported on this OS yet")
	}
	in, err := ResolveDoorbellInstall(program)
	if err != nil {
		return TrustRecord{}, err
	}
	var label string
	for _, a := range e.ManagedAccounts() {
		if a.Account == account {
			label = a.Label
		}
	}
	if label == "" {
		return TrustRecord{}, fmt.Errorf("account %s is not logged in to a folder Watchyard manages on this machine", account)
	}
	rec := TrustRecord{Venv: in.Venv, Program: in.Program, Interpreter: in.Interpreter, Machine: e.Machine.ID,
		MachineName: e.Machine.Name, Account: account, AccountLabel: label, ApprovedAt: e.now().UTC().Truncate(time.Second),
		TestBuild: TrustGuardOff}
	if err := e.SaveTrust(&rec); err != nil {
		return TrustRecord{}, err
	}
	e.audit(auditLine{From: ServiceSessionID(e.Machine.ID), FromA: account, Status: "trust", FromKind: ServiceDoorbell}, e.now())
	return rec, nil
}

// RevokeDoorbell removes the record (wy peers trust doorbell --off, or the hub's revocation);
// by names who: "cli" or "hub".
func (e Env) RevokeDoorbell(by string) (bool, error) {
	rec, _ := e.LoadTrust()
	if err := e.SaveTrust(nil); err != nil {
		return false, err
	}
	if rec != nil {
		e.audit(auditLine{From: ServiceSessionID(e.Machine.ID), FromA: rec.Account, Status: "trust_off", Detail: by, FromKind: ServiceDoorbell}, e.now())
	}
	return rec != nil, nil
}
