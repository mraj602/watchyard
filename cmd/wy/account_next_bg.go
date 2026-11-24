package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/leases"
	peerspkg "github.com/mraj602/watchyard/internal/peers"
	"github.com/mraj602/watchyard/internal/picker"
)

// isBg: the command of --exec is a claude that starts a background session.
func isBg(argv []string) bool {
	if len(argv) == 0 || !strings.EqualFold(strings.TrimSuffix(filepath.Base(argv[0]), ".exe"), "claude") {
		return false
	}
	for _, a := range argv[1:] {
		if a == "--bg" {
			return true
		}
	}
	return false
}

// bgLine is what claude --bg prints when it hands the session over: "backgrounded · <id> · <name>".
var bgLine = regexp.MustCompile(`backgrounded\s+·\s+([0-9a-f]{8})`)

// bgFind is how long nextBg looks for the new session's registry file.
var bgFind = 5 * time.Second

// nextBg runs a `claude --bg` lane: claude ignores --session-id there and its supervisor names
// the session, so the lease is bound to that id after the launch (setup.md 8.5). The session
// also runs in the supervisor's environment, not this one: CLAUDE_CONFIG_DIR picks the
// account, other variables (WY_FINISH_BY) do not reach it.
func nextBg(e nextEnv, p picker.Pick, argv []string, leaseID string) error {
	look := e.lookPath
	if look == nil {
		look = exec.LookPath
	}
	path, err := look(argv[0])
	if err != nil {
		return fmt.Errorf("cannot find %s: %w", argv[0], err)
	}
	run := e.runBg
	if run == nil {
		run = runBgChild
	}
	captured, err := run(path, argv, execEnv(e, p))
	if err != nil {
		return err
	}
	m := bgLine.FindStringSubmatch(captured)
	if m == nil {
		fmt.Fprintln(e.stderr, "wy account next: the background session's id was not printed; its lease is not bound to it")
		return nil
	}
	sid := bgSession(p.Ranked.Dir, m[1])
	if sid == "" {
		fmt.Fprintf(e.stderr, "wy account next: background session %s not found in %s/sessions; its lease is not bound to it\n", m[1], p.Ranked.Dir)
		return nil
	}
	g := leases.Ledger{Dir: filepath.Join(e.dirs.State.Path, "next")}
	unlock, err := g.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	now := e.now()
	if err := g.Append(leases.Line{V: 1, Op: leases.OpBind, ID: leaseID, At: now.UTC(), SessionID: sid}); err != nil {
		return err
	}
	_, _ = g.Mark(sid, leases.OpLaunched, now)
	return nil
}

// bgSession finds the full id of the background session whose id starts with short in the
// registry of folder dir, waiting up to bgFind for it to appear.
func bgSession(dir, short string) string {
	deadline := time.Now().Add(bgFind)
	for {
		for _, x := range peerspkg.ReadRegistry(identity.OSFS{}, identity.Slot{Dir: dir}) {
			if strings.HasPrefix(x.SessionID, short) {
				return x.SessionID
			}
		}
		if !time.Now().Before(deadline) {
			return ""
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// runBgChild runs claude --bg as a child (it returns once the supervisor has the session).
// Its output goes to a temporary file, not a pipe: a supervisor it starts inherits the
// descriptors and would hold a pipe open for hours. The file is then passed through.
func runBgChild(path string, argv, env []string) (string, error) {
	f, err := os.CreateTemp("", "wy-bg-*.out")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	cmd := exec.Command(path, argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, f, f
	runErr := cmd.Run()
	b, _ := os.ReadFile(f.Name())
	_, _ = os.Stdout.Write(b)
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		return string(b), exitWith(ee.ExitCode(), nil)
	}
	return string(b), runErr
}
