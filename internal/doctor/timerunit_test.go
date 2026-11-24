package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mraj602/watchyard/internal/config"
)

// setup.md 8.6: the timer's service must leave the detached switch pass alive
// (KillMode=process) and, with the switch on, find claude.
func TestTimerUnitChecks(t *testing.T) {
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
