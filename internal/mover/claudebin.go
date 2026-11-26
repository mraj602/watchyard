package mover

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/mraj602/watchyard/internal/claudecli"
)

// The timer runs the switch with the service manager's PATH, which often lacks claude's
// folder (~/.local/bin): `claude stop` and the default resume command (`env
// CLAUDE_CONFIG_DIR=... claude`) then fail with "not found" (live run, 1.3.3-pre3). Every
// claude the mover runs goes through ClaudeBin / ClaudeArgv.

// claudePath is exec.LookPath("claude"); locate is claudecli.LocateFrom; procExe is the
// executable of a process. Tests replace them.
var (
	claudePath = func() (string, error) { return exec.LookPath("claude") }
	locate     = claudecli.LocateFrom
	procExe    = func(pid int) string {
		if runtime.GOOS != "linux" || pid <= 1 {
			return ""
		}
		p, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
		if err != nil {
			return ""
		}
		return strings.TrimSuffix(p, " (deleted)")
	}
)

// ClaudeBin is the claude to run: "claude" when it is on PATH, else the absolute path
// claudecli.LocateFrom finds (~/.local/bin/claude and the usual places, then hints such as a
// session's own executable), else "claude" (the error then names it).
func ClaudeBin(hints ...string) string {
	if _, err := claudePath(); err == nil {
		return "claude"
	}
	home, _ := os.UserHomeDir()
	if p, err := locate(home, hints...); err == nil {
		return p
	}
	return "claude"
}

// ClaudeArgv is argv with its program word "claude" (argv[0], or the command after `env
// NAME=value...`) replaced by ClaudeBin(hints...). A launcher (account.move_launch) is left
// as it is: it finds claude itself.
func ClaudeArgv(argv []string, hints ...string) []string {
	i := 0
	if len(argv) > 0 && argv[0] == "env" {
		i = 1
		for i < len(argv) && strings.Contains(argv[i], "=") && !strings.HasPrefix(argv[i], "-") {
			i++
		}
	}
	if i >= len(argv) || argv[i] != "claude" {
		return argv
	}
	bin := ClaudeBin(hints...)
	if bin == "claude" {
		return argv
	}
	out := append([]string(nil), argv...)
	out[i] = bin
	return out
}

// sessionExe is the executable of process pid when it is claude's own binary (named claude,
// or a native install's ~/.local/share/claude/versions/<v>), for ClaudeBin's hints; "" when
// it is anything else (node, a shell).
func sessionExe(pid int) string {
	p := procExe(pid)
	if filepath.Base(p) == "claude" || strings.Contains(filepath.ToSlash(p), "/claude/versions/") {
		return p
	}
	return ""
}
