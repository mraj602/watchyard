package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/paths"
)

type stCall struct {
	name      string
	args, env []string
}

func selftestFixture(t *testing.T, movelaunch string) (moveEnv, *bytes.Buffer, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home) // mover.Command expands ~ from it
	d, err := paths.Resolve(paths.Env{GOOS: runtime.GOOS, Home: home, Getenv: func(k string) string {
		if k == "WY_HOME" {
			return filepath.Join(home, "wy")
		}
		return ""
	}})
	if err != nil {
		t.Fatal(err)
	}
	if movelaunch != "" {
		if err := os.MkdirAll(d.Config.Path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d.Config.Path, "config.toml"), []byte("[account]\nmove_launch = \""+movelaunch+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unit := filepath.Join(home, ".config", "systemd", "user", "watchyard-flush.service")
	if err := os.MkdirAll(filepath.Dir(unit), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "[Service]\nType=oneshot\nEnvironment=WY_HOME=%h/wy\nEnvironment=PATH=%h/tools:/usr/bin\n" +
		"ExecStart=\"%h/bin/wy\" tick --quiet\nKillMode=process\n"
	if err := os.WriteFile(unit, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	e := moveEnv{nextEnv: nextEnv{home: home, dirs: d, getenv: func(string) string { return "" }, stdout: &out, stderr: io.Discard}}
	return e, &out, home
}

func stub(t *testing.T, goos string, look func(string) (string, error), run func(stCall) error) *[]stCall {
	t.Helper()
	og, ol, oe := selftestGOOS, selftestLook, selftestExec
	t.Cleanup(func() { selftestGOOS, selftestLook, selftestExec = og, ol, oe })
	var calls []stCall
	selftestGOOS, selftestLook = goos, look
	selftestExec = func(name string, args, env []string, stdout, stderr io.Writer) error {
		c := stCall{name, args, env}
		calls = append(calls, c)
		io.WriteString(stdout, "PLAN OUTPUT\n")
		return run(c)
	}
	return &calls
}

func mkexe(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestMoveSelftestSystemdRun(t *testing.T) {
	e, out, home := selftestFixture(t, "~/tools/launch {dir}")
	mkexe(t, filepath.Join(home, "tools", "claude"))
	mkexe(t, filepath.Join(home, "tools", "launch"))
	calls := stub(t, "linux", func(string) (string, error) { return "/usr/bin/systemd-run", nil }, func(stCall) error { return nil })
	if err := moveCmd(e, []string{"--selftest"}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls: %+v", *calls)
	}
	c := (*calls)[0]
	want := "--user --wait --pipe --quiet --collect -p KillMode=process --setenv=WY_HOME=" + home + "/wy --setenv=PATH=" + home + "/tools:/usr/bin " +
		home + "/bin/wy move --plan"
	if c.name != "/usr/bin/systemd-run" || strings.Join(c.args, " ") != want || c.env != nil {
		t.Fatalf("systemd-run: %s %q env=%v\nwant %s", c.name, c.args, c.env, want)
	}
	s := out.String()
	for _, w := range []string{"KillMode:  process", "PATH:      " + home + "/tools:/usr/bin", "PLAN OUTPUT",
		"claude: found (" + home + "/tools/claude)", "move_launch: found (" + home + "/tools/launch)", "exit status 0", "Result: ok."} {
		if !strings.Contains(s, w) {
			t.Errorf("output lacks %q:\n%s", w, s)
		}
	}
	if strings.Contains(strings.Join(c.args, " "), "--auto") {
		t.Error("selftest must never run --auto")
	}
}

func TestMoveSelftestFallbackAndVerdict(t *testing.T) {
	e, out, home := selftestFixture(t, "nolauncher {dir}")
	calls := stub(t, "linux", func(string) (string, error) { return "", errors.New("not found") },
		func(stCall) error { return exec.Command("sh", "-c", "exit 3").Run() })
	t.Setenv("HOME", home) // claudecli fallback looks in <home>/.local/bin only; none exists
	err := moveCmd(e, []string{"--selftest"})
	if err == nil {
		t.Fatalf("a missing claude and launcher must fail:\n%s", out)
	}
	c := (*calls)[0]
	if c.name != home+"/bin/wy" || strings.Join(c.args, " ") != "move --plan" {
		t.Fatalf("direct: %+v", c)
	}
	if strings.Join(c.env, "|") != "HOME="+home+"|WY_HOME="+home+"/wy|PATH="+home+"/tools:/usr/bin" {
		t.Fatalf("env must be the unit's plus HOME only: %q", c.env)
	}
	s := out.String()
	for _, w := range []string{"cleared environment", "systemd-run is not used", "move_launch: NOT found (nolauncher", "exit status 3"} {
		if !strings.Contains(s, w) {
			t.Errorf("output lacks %q:\n%s", w, s)
		}
	}
	if !strings.Contains(s, "claude: ") {
		t.Errorf("no claude line:\n%s", s)
	}
}

func TestMoveSelftestNoUnit(t *testing.T) {
	e, _, home := selftestFixture(t, "")
	if err := os.RemoveAll(filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}
	stub(t, "linux", func(string) (string, error) { return "x", nil }, func(stCall) error { return nil })
	if err := moveCmd(e, []string{"--selftest"}); err == nil || !strings.Contains(err.Error(), "no timer job") {
		t.Fatalf("%v", err)
	}
}
