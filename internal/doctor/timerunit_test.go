package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
)

// setup.md 8.6: the timer's service must leave the detached switch pass alive
// (KillMode=process) and, with the switch on, find claude.
func TestTimerUnitChecks(t *testing.T) {
	linuxTimer(t)
	home := t.TempDir()
	svc := filepath.Join(home, ".config", "systemd", "user", "watchyard-flush.service")
	o := Options{Home: home}
	cfg := config.Defaults()
	timer := func() []Check {
		var rep Report
		checkTimerUnit(&rep, o, cfg)
		var out []Check
		for _, c := range rep.Checks {
			if c.Name == "timer" {
				out = append(out, c)
			}
		}
		return out
	}
	if c := timer(); len(c) != 0 {
		t.Fatalf("no unit: %+v", c)
	}
	// A unit an older wy wrote: no KillMode.
	write(t, svc, "[Service]\nType=oneshot\nExecStart=/home/u/.local/bin/wy tick --quiet\n")
	c := timer()
	if len(c) != 1 || c[0].Level != Warn || !strings.Contains(c[0].Detail, "KillMode=control-group") || !strings.Contains(c[0].Fix, "killmode.conf") {
		t.Fatalf("old unit: %+v", c)
	}
	// A drop-in fixes it, as does the unit this wy renders.
	write(t, filepath.Join(svc+".d", "killmode.conf"), "[Service]\nKillMode=process\n")
	if c := timer(); len(c) != 0 {
		t.Fatalf("drop-in: %+v", c)
	}
	write(t, filepath.Join(svc+".d", "killmode.conf"), "")
	write(t, svc, "[Service]\nType=oneshot\nKillMode=process\n")
	if c := timer(); len(c) != 0 {
		t.Fatalf("new unit: %+v", c)
	}

	// Switch on: claude must be on the timer's PATH or in ~/.local/bin.
	cfg.Account.SmartSwitch = true
	if _, err := os.Stat("/usr/local/bin/claude"); err == nil {
		t.Skip("this machine has /usr/local/bin/claude")
	}
	if _, err := os.Stat("/opt/homebrew/bin/claude"); err == nil {
		t.Skip("this machine has /opt/homebrew/bin/claude")
	}
	c = timer()
	if len(c) != 1 || !strings.Contains(c[0].Detail, "cannot find claude") {
		t.Fatalf("no claude: %+v", c)
	}
	bin := filepath.Join(home, "tools", "claude")
	write(t, bin, "#!/bin/sh\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(svc+".d", "path.conf"), "[Service]\nEnvironment=PATH=%h/tools:/usr/bin\n")
	if c := timer(); len(c) != 0 {
		t.Fatalf("on the unit's PATH: %+v", c)
	}
	write(t, filepath.Join(svc+".d", "path.conf"), "")
	local := filepath.Join(home, ".local", "bin", "claude")
	write(t, local, "#!/bin/sh\n")
	if err := os.Chmod(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if c := timer(); len(c) != 0 {
		t.Fatalf("in ~/.local/bin: %+v", c)
	}
}

// setup.md 8.6: account.move_launch's program must resolve from the timer's PATH.
func TestTimerMoveLaunchCheck(t *testing.T) {
	linuxTimer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	svc := filepath.Join(home, ".config", "systemd", "user", "watchyard-flush.service")
	write(t, svc, "[Service]\nKillMode=process\nEnvironment=PATH=%h/tools:/usr/bin\n")
	mk := func(p string, mode os.FileMode) {
		write(t, p, "#!/bin/sh\n")
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(home, "tools", "claude"), 0o755)
	cfg := config.Defaults()
	cfg.Account.SmartSwitch = true
	run := func(tpl string) []Check {
		cfg.Account.MoveLaunch = tpl
		var rep Report
		checkTimerUnit(&rep, Options{Home: home}, cfg)
		return rep.Checks
	}
	if c := run(""); len(c) != 0 {
		t.Fatalf("no launcher: %+v", c)
	}
	c := run("mylauncher {dir}")
	if len(c) != 1 || c[0].Level != Warn || !strings.Contains(c[0].Detail, "mylauncher") || !strings.Contains(c[0].Fix, "absolute path") {
		t.Fatalf("not on PATH: %+v", c)
	}
	mk(filepath.Join(home, "tools", "mylauncher"), 0o644)
	if c := run("mylauncher {dir}"); len(c) != 1 {
		t.Fatalf("not executable: %+v", c)
	}
	mk(filepath.Join(home, "tools", "mylauncher"), 0o755)
	for _, tpl := range []string{"mylauncher {dir}", "~/tools/mylauncher {n}", "$HOME/tools/mylauncher", "env CLAUDE_CONFIG_DIR={dir} mylauncher", home + "/tools/mylauncher"} {
		if c := run(tpl); len(c) != 0 {
			t.Fatalf("%q: %+v", tpl, c)
		}
	}
	if c := run("/nonexistent/launcher"); len(c) != 1 {
		t.Fatalf("absolute missing: %+v", c)
	}
	cfg.Account.SmartSwitch = false
	if c := run("mylauncher-gone"); len(c) != 0 {
		t.Fatalf("switch off: %+v", c)
	}
}

// setup.md 8.6, Between steps: hand-written hooks need the step hooks; the plugin has them.
func TestMoverHooksCheck(t *testing.T) {
	slot := identity.Slot{Name: "~/.claude"}
	run := func(s settings, on bool) []Check {
		var rep Report
		checkMoverHooks(&rep, slot, s, nil, on)
		return rep.Checks
	}
	plain := settings{exists: true, hooks: map[string][]string{"PreToolUse": {"wy hook PreToolUse"}, "Stop": {"wy hook Stop"}}}
	if c := run(plain, false); len(c) != 0 {
		t.Fatalf("switch off: %+v", c)
	}
	c := run(plain, true)
	if len(c) != 1 || c[0].Level != Warn || !strings.Contains(c[0].Detail, "PreToolUse, PostToolUse") {
		t.Fatalf("missing: %+v", c)
	}
	plain.hooks["PreToolUse"] = append(plain.hooks["PreToolUse"], "command -v wy >/dev/null && wy hook --step PreToolUse || true")
	c = run(plain, true)
	if len(c) != 1 || !strings.Contains(c[0].Detail, "PostToolUse") || strings.Contains(c[0].Detail, "PreToolUse,") {
		t.Fatalf("post missing: %+v", c)
	}
	plain.hooks["PostToolUse"] = []string{"wy hook --step PostToolUse"}
	if c := run(plain, true); len(c) != 0 {
		t.Fatalf("complete: %+v", c)
	}
	plug := settings{exists: true, plugins: map[string]bool{"watchyard@watchyard": true}}
	if c := run(plug, true); len(c) != 0 {
		t.Fatalf("plugin: %+v", c)
	}
}

// linuxTimer makes the checks read the systemd unit whatever the OS running the test.
func linuxTimer(t *testing.T) {
	g := timerGOOS
	timerGOOS = "linux"
	t.Cleanup(func() { timerGOOS = g })
}
