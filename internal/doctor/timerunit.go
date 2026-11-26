package doctor

import (
	"path/filepath"
	"runtime"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/claudecli"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/mover"
	"github.com/mraj602/watchyard/internal/timer"
)

// systemdUserPath is the PATH a systemd user service gets when its unit sets none;
// launchdPath the one a LaunchAgent gets.
const (
	systemdUserPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin"
	launchdPath     = "/usr/bin:/bin:/usr/sbin:/sbin"
)

// timerGOOS is the OS whose timer job is read (tests keep it as it is: the unit files they
// write are systemd's).
var timerGOOS = runtime.GOOS

// checkTimerUnit reads the timer's installed job (systemd service with its drop-ins, or the
// LaunchAgent) for what Smart Account Switch needs from it (setup.md 8.6): KillMode=process
// (systemd), so the pass `wy tick` starts detached is not killed when tick exits; and, while
// the switch is on, a claude the timer can find (its PATH, else the usual install places)
// and, when account.move_launch is set, a launcher program it can find and run.
func checkTimerUnit(rep *Report, o Options, cfg config.Config) {
	te := timer.Env{GOOS: timerGOOS, Home: o.Home, Getenv: o.Getenv}
	in, err := timer.ReadInstalled(te)
	if err != nil {
		return
	}
	path := in.Get("PATH")
	if path == "" {
		path = systemdUserPath
		if in.GOOS == "darwin" {
			path = launchdPath
		}
	}
	svc := in.Path
	if in.GOOS == "linux" && in.KillMode != "process" {
		rep.add(Check{Name: "timer", Level: Warn,
			Detail: "the timer's service has KillMode=" + in.KillMode + ": it kills the Smart Account Switch pass that wy tick starts in the background",
			Fix: "the next wy update rewrites the unit with KillMode=process; until then: " + svc + ".d/killmode.conf with [Service] KillMode=process, " +
				"then systemctl --user daemon-reload"})
	}
	if !cfg.Account.SmartSwitch {
		return
	}
	pathFix := "link claude into ~/.local/bin, or set its PATH in a drop-in: " + svc + ".d/path.conf with [Service] Environment=PATH=..."
	if in.GOOS == "darwin" {
		pathFix = "link claude into ~/.local/bin, or add a PATH to EnvironmentVariables in " + svc + " (then launchctl bootout and bootstrap it)"
	}
	if _, ok := mover.LookIn(path, "claude"); !ok {
		if _, err := claudecli.Fallback(o.Home); err != nil {
			rep.add(Check{Name: "timer", Level: Warn,
				Detail: "the timer cannot find claude (not on its PATH " + path + " nor in ~/.local/bin): Smart Account Switch cannot stop or resume sessions",
				Fix:    pathFix})
		}
	}
	if prog := mover.LaunchProgram(cfg.Account.MoveLaunch); prog != "" {
		if _, ok := mover.LookIn(path, prog); !ok {
			rep.add(Check{Name: "timer", Level: Warn,
				Detail: "account.move_launch runs " + prog + ", which the timer cannot find or run (it is not an executable file on its PATH " + path + "): moved sessions would not restart",
				Fix:    "use an absolute path in account.move_launch (wy config set account.move_launch '/full/path/launcher ...'), or put its folder on the timer's PATH: " + filepath.Join(filepath.Dir(svc), brand.Slug+"-flush.service.d", "path.conf")})
		}
	}
}
