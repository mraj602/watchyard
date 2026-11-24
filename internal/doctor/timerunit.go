package doctor

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/claudecli"
	"github.com/mraj602/watchyard/internal/config"
)

// systemdUserPath is the PATH a systemd user service gets when its unit sets none.
const systemdUserPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin"

// checkTimerUnit reads the timer's installed systemd service (Linux; with its drop-ins) for
// what Smart Account Switch needs from it (setup.md 8.6): KillMode=process, so the pass
// `wy tick` starts detached is not killed when tick exits, and, while the switch is on, a
// claude the timer can find (its PATH, else the usual install places).
func checkTimerUnit(rep *Report, o Options, cfg config.Config) {
	svc := filepath.Join(o.Home, ".config", "systemd", "user", brand.Slug+"-flush.service")
	b, err := os.ReadFile(svc)
	if err != nil {
		return
	}
	unit := []string{string(b)}
	drops, _ := filepath.Glob(filepath.Join(svc+".d", "*.conf"))
	sort.Strings(drops) // systemd applies them in this order: the last value wins
	for _, d := range drops {
		if b, err := os.ReadFile(d); err == nil {
			unit = append(unit, string(b))
		}
	}
	kill, path := "control-group", systemdUserPath
	for _, u := range unit {
		for _, line := range strings.Split(u, "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			switch k {
			case "KillMode":
				kill = strings.TrimSpace(v)
			case "Environment":
				for _, f := range strings.Fields(v) {
					if p, ok := strings.CutPrefix(strings.Trim(f, `"`), "PATH="); ok {
						path = strings.ReplaceAll(p, "%h", o.Home)
					}
				}
			}
		}
	}
	if kill != "process" {
		rep.add(Check{Name: "timer", Level: Warn,
			Detail: "the timer's service has KillMode=" + kill + ": it kills the Smart Account Switch pass that wy tick starts in the background",
			Fix: "the next wy update rewrites the unit with KillMode=process; until then: " + svc + ".d/killmode.conf with [Service] KillMode=process, " +
				"then systemctl --user daemon-reload"})
	}
	if !cfg.Account.SmartSwitch {
		return
	}
	for _, d := range filepath.SplitList(path) {
		if fi, err := os.Stat(filepath.Join(d, "claude")); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return
		}
	}
	if _, err := claudecli.Fallback(o.Home); err != nil {
		rep.add(Check{Name: "timer", Level: Warn,
			Detail: "the timer cannot find claude (not on its PATH " + path + " nor in ~/.local/bin): Smart Account Switch cannot stop or resume sessions",
			Fix:    "link claude into ~/.local/bin, or set its PATH in a drop-in: " + svc + ".d/path.conf with [Service] Environment=PATH=..."})
	}
}
