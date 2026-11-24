package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mraj602/watchyard/internal/mover"
	"github.com/mraj602/watchyard/internal/paths"
)

// shimMark is in every claude shim wy writes, so --remove-shim removes only its own.
const shimMark = "# Watchyard claude shim (wy run --install-shim)"

func runUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  wy run [--] claude [args...]     run claude so Smart Account Switch can move it in place
  wy run --install-shim <dir>      write <dir>/claude that runs the real claude through wy run
                                   (put <dir> before the real claude on PATH; opt-in)
  wy run --remove-shim <dir>       remove that shim

When the session is moved to another account, its claude is stopped and wy run starts
claude --resume <id> under the new account in the same terminal (setup.md 8.6).
`)
}

func runRun(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		runUsage(stderr)
		if len(args) == 0 {
			return errUsage
		}
		return nil
	}
	switch args[0] {
	case "--install-shim", "--remove-shim":
		if len(args) != 2 {
			runUsage(stderr)
			return errUsage
		}
		if args[0] == "--install-shim" {
			return installShim(args[1], os.Getenv("PATH"), stdout)
		}
		return removeShim(args[1], stdout)
	case "--":
		args = args[1:]
	}
	if len(args) == 0 {
		runUsage(stderr)
		return errUsage
	}
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return err
	}
	code, err := wrap(mover.NewState(d.State.Path), args, os.Environ(), stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return exitWith(code, nil)
	}
	return nil
}

// wrap runs argv, and again with a relaunch order each time the mover leaves one, until the
// command ends without one. It returns the command's exit code.
func wrap(st mover.State, argv, env []string, stderr io.Writer) (int, error) {
	me := os.Getpid()
	defer st.RemoveRun(me)
	// Ctrl-C and Ctrl-\ go to the whole foreground group: claude handles them, wy run waits.
	signal.Ignore(os.Interrupt, syscall.SIGQUIT)
	defer signal.Reset(os.Interrupt, syscall.SIGQUIT)
	fwd := make(chan os.Signal, 4)
	signal.Notify(fwd, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(fwd)
	for {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdin, os.Stdout, os.Stderr, env
		if err := cmd.Start(); err != nil {
			return 127, err
		}
		dir, _ := os.Getwd()
		_ = st.WriteRun(mover.RunInfo{Wrapper: me, Child: cmd.Process.Pid, Argv: argv, Dir: dir, At: time.Now()})
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var werr error
	wait:
		for {
			select {
			case s := <-fwd:
				_ = cmd.Process.Signal(s)
			case werr = <-done:
				break wait
			}
		}
		if o, ok := st.TakeOrder(me); ok {
			fmt.Fprintf(stderr, "wy run: Watchyard moved this session to %s; resuming it\n", o.ConfigDir)
			argv, env = o.Argv, mover.CleanEnv(env)
			continue
		}
		var ee *exec.ExitError
		switch {
		case werr == nil:
			return 0, nil
		case errors.As(werr, &ee):
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				return 128 + int(ws.Signal()), nil
			}
			return ee.ExitCode(), nil
		}
		return 1, werr
	}
}

// installShim writes <dir>/claude: the real claude (the first on PATH outside dir) through
// wy run.
func installShim(dir, path string, stdout io.Writer) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	real := ""
	for _, p := range filepath.SplitList(path) {
		if pa, _ := filepath.Abs(p); pa == abs || p == "" {
			continue
		}
		c := filepath.Join(p, "claude")
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			if b, err := os.ReadFile(c); err == nil && strings.Contains(string(b), shimMark) {
				continue
			}
			real = c
			break
		}
	}
	if real == "" {
		return errors.New("no claude on PATH outside " + abs)
	}
	wy, err := os.Executable()
	if err != nil {
		return err
	}
	shim := filepath.Join(abs, "claude")
	if b, err := os.ReadFile(shim); err == nil && !strings.Contains(string(b), shimMark) {
		return fmt.Errorf("%s exists and is not a Watchyard shim: not replaced", shim)
	}
	body := "#!/bin/sh\n" + shimMark + "\n# Remove with: wy run --remove-shim " + mover.Shell([]string{abs}) + "\nexec " +
		mover.Shell([]string{wy}) + " run -- " + mover.Shell([]string{real}) + " \"$@\"\n"
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s (runs %s through wy run)\nput %s before %s on PATH to use it\n", shim, real, abs, filepath.Dir(real))
	return nil
}

func removeShim(dir string, stdout io.Writer) error {
	shim := filepath.Join(dir, "claude")
	b, err := os.ReadFile(shim)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stdout, "no shim in "+dir)
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.Contains(string(b), shimMark) {
		return fmt.Errorf("%s is not a Watchyard shim: left alone", shim)
	}
	if err := os.Remove(shim); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "removed "+shim)
	return nil
}
