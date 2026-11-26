package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"

	"github.com/mraj602/watchyard/internal/claudecli"
	"github.com/mraj602/watchyard/internal/mover"
	"github.com/mraj602/watchyard/internal/timer"
)

// Tests replace these.
var (
	selftestGOOS = runtime.GOOS
	// selftestLook finds a program on the real PATH (systemd-run).
	selftestLook = exec.LookPath
	// selftestExec runs name with args; env nil means this process's own environment, else
	// exactly env. It returns the run's error (an *exec.ExitError for a non-zero status).
	selftestExec = func(name string, args, env []string, stdout, stderr io.Writer) error {
		c := exec.Command(name, args...)
		c.Env, c.Stdout, c.Stderr = env, stdout, stderr
		return c.Run()
	}
)

// selftestSystemdRun is the systemd-run command line that runs argv in the timer's
// environment: a transient user service with the unit's KillMode and Environment= values.
func selftestSystemdRun(in timer.Installed, argv []string) []string {
	out := []string{"--user", "--wait", "--pipe", "--quiet", "--collect", "-p", "KillMode=process"}
	for _, kv := range in.Env {
		out = append(out, "--setenv="+kv[0]+"="+kv[1])
	}
	return append(out, argv...)
}

// moveSelftest is `wy move --selftest` (setup.md 8.6, Self-test): `wy move --plan` run the way
// the timer runs the switch (never --auto), then a verdict on what the timer can find.
func moveSelftest(e moveEnv) error {
	te := timer.Env{GOOS: selftestGOOS, Home: e.home, Getenv: e.getenv}
	in, err := timer.ReadInstalled(te)
	if err != nil {
		return fmt.Errorf("selftest: no timer job is installed here (%s): run wy setup or wy connect first", err)
	}
	wy := in.WyPath
	if wy == "" {
		return errors.New("selftest: cannot read the wy path from " + in.Path)
	}
	path := in.Get("PATH")
	if path == "" {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin"
		if in.GOOS == "darwin" {
			path = "/usr/bin:/bin:/usr/sbin:/sbin"
		}
	}
	argv := []string{wy, "move", "--plan"}
	how := "systemd-run (a transient user service with the unit's KillMode and Environment=)"
	var sd string
	if in.GOOS == "linux" {
		sd, err = selftestLook("systemd-run")
	} else {
		err = errors.New("not Linux")
	}
	fmt.Fprintln(e.stdout, "Self-test of the timer environment (nothing is moved):")
	fmt.Fprintf(e.stdout, "  unit:      %s\n", in.Path)
	fmt.Fprintf(e.stdout, "  wy:        %s\n", wy)
	fmt.Fprintf(e.stdout, "  PATH:      %s\n", path)
	if in.GOOS == "linux" {
		fmt.Fprintf(e.stdout, "  KillMode:  %s\n", in.KillMode)
	}
	for _, kv := range in.Env {
		if strings.HasPrefix(kv[0], "WY_") {
			fmt.Fprintf(e.stdout, "  %s=%s\n", kv[0], kv[1])
		}
	}
	runErr := error(nil)
	ran := false
	if err == nil {
		fmt.Fprintf(e.stdout, "  run via:   %s\n\n", how)
		var errBuf bytes.Buffer
		runErr = selftestExec(sd, selftestSystemdRun(in, argv), nil, e.stdout, io.MultiWriter(e.stderr, &errBuf))
		ran = true
		var ee *exec.ExitError
		if runErr != nil && (!errors.As(runErr, &ee) || strings.Contains(errBuf.String(), "Failed to connect")) {
			fmt.Fprintf(e.stdout, "\n(systemd-run could not start a user service: %v; running the command directly instead)\n", runErr)
			ran = false
		}
	}
	if !ran {
		env := []string{"HOME=" + e.home}
		for _, kv := range in.Env {
			env = append(env, kv[0]+"="+kv[1])
		}
		if err != nil {
			fmt.Fprintf(e.stdout, "  run via:   the command directly with a cleared environment (the unit's variables and HOME only; systemd-run is not used: %v)\n\n", err)
		}
		runErr = selftestExec(argv[0], argv[1:], env, e.stdout, e.stderr)
	}

	fmt.Fprintln(e.stdout)
	fail := false
	if p, ok := mover.LookIn(path, "claude"); ok {
		fmt.Fprintf(e.stdout, "claude: found (%s)\n", p)
	} else if p, err := claudecli.Fallback(e.home); err == nil {
		fmt.Fprintf(e.stdout, "claude: not on the timer's PATH, found by the fallback (%s)\n", p)
	} else {
		fmt.Fprintln(e.stdout, "claude: NOT found (not on the timer's PATH nor in ~/.local/bin)")
		fail = true
	}
	if tpl := e.config().Account.MoveLaunch; strings.TrimSpace(tpl) == "" {
		fmt.Fprintln(e.stdout, "move_launch: not set (moved sessions start with env CLAUDE_CONFIG_DIR=<folder> claude)")
	} else if prog := mover.LaunchProgram(tpl); prog == "" {
		fmt.Fprintf(e.stdout, "move_launch: NOT usable (no program in %q)\n", tpl)
		fail = true
	} else if p, ok := mover.LookIn(path, prog); ok {
		fmt.Fprintf(e.stdout, "move_launch: found (%s)\n", p)
	} else {
		fmt.Fprintf(e.stdout, "move_launch: NOT found (%s is not an executable file on the timer's PATH)\n", prog)
		fail = true
	}
	status := "exit status 0"
	if runErr != nil {
		status = "failed: " + runErr.Error()
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			status = fmt.Sprintf("exit status %d", ee.ExitCode())
		}
		fail = true
	}
	fmt.Fprintf(e.stdout, "wy move --plan: %s\n", status)
	if fail {
		fmt.Fprintln(e.stdout, "Result: the timer's pass would have a problem (wy doctor shows the fix).")
		return exitWith(1, errors.New("selftest found a problem"))
	}
	fmt.Fprintln(e.stdout, "Result: ok.")
	return nil
}
