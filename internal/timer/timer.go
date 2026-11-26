// Package timer installs the OS timer that runs `wy flush` every minute and at boot:
// a systemd user timer on Linux, a LaunchAgent on macOS, and instructions plus a Task
// Scheduler XML file on Windows (docs/spec/setup.md).
//
// Nothing here touches the real system unless the caller hands in a real Runner and the
// user's real home folder; tests use a fake home and a recording Runner.
package timer

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/mraj602/watchyard/internal/brand"
)

// Command is what the timer runs (arguments after the wy binary): `wy tick` (share sync,
// scan, agents every 5 minutes, flush).
var Command = []string{"tick", "--quiet"}

// Names of the units and jobs.
const (
	SystemdName    = brand.Slug + "-flush"
	LaunchdLabel   = "com." + brand.Slug + ".flush"
	WindowsTask    = brand.Name + " Flush"
	WindowsXMLFile = SystemdName + ".task.xml"
)

// Runner runs an external command. Tests inject a recording fake so nothing real runs.
type Runner interface {
	Run(name string, args ...string) (output []byte, err error)
}

// ExecRunner runs real commands.
type ExecRunner struct{}

func (ExecRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// Env is everything Install and Uninstall depend on.
type Env struct {
	GOOS     string
	Home     string
	UID      int
	User     string
	WyPath   string // absolute path of the wy binary the timer runs
	StateDir string // for logs and the Windows XML
	Getenv   func(string) string
	Runner   Runner
	Out      io.Writer // hints and instructions
}

// passEnv are the folder overrides copied into the unit, so a timer started by the service
// manager sees the same folders as the shell that ran `wy connect`.
var passEnv = []string{"WY_HOME", "WY_CONFIG_DIR", "WY_STATE_DIR", "WY_DATA_DIR"}

func (e Env) envPairs() [][2]string {
	if e.Getenv == nil {
		return nil
	}
	var out [][2]string
	for _, k := range passEnv {
		if v := e.Getenv(k); v != "" {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}

// Result lists what Install did.
type Result struct {
	Files []string // files written
	Notes []string // things the user should know (fallbacks, linger)
}

// SystemdDir is where the user units live.
func SystemdDir(e Env) string {
	if e.Getenv != nil {
		if x := e.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
			return filepath.Join(x, "systemd", "user")
		}
	}
	return filepath.Join(e.Home, ".config", "systemd", "user")
}

// LaunchAgentPath is the plist location.
func LaunchAgentPath(e Env) string {
	return filepath.Join(e.Home, "Library", "LaunchAgents", LaunchdLabel+".plist")
}

// ---- unit text (golden-tested) ----------------------------------------------------------

// systemdQuote quotes s for an ExecStart= or Environment= word. systemd splits on
// whitespace and understands double quotes and C-style escapes; a raw CR or LF would end
// the line and start a new directive, so every control byte is written as a \xNN escape
// (systemd decodes it back to the same byte). Keep in sync with internal/service.
func systemdQuote(s string) string { return systemdQuoteWord(s, true) }

// systemdEnvQuote quotes a NAME=value word for Environment=, where (unlike ExecStart=) "$"
// is not special and must not be doubled (systemd.exec(5): no variable expansion there).
func systemdEnvQuote(s string) string { return systemdQuoteWord(s, false) }

func systemdQuoteWord(s string, dollar bool) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\$%;") && !hasControl(s) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' || c == '"':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '%':
			b.WriteString("%%")
		case c == '$' && dollar:
			b.WriteString("$$")
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// SystemdKillMode is the service's KillMode: only tick itself ends with the run; what it
// started detached (the switch's pass) runs on.
const SystemdKillMode = "process"

// SystemdService renders watchyard-flush.service.
func SystemdService(e Env) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Watchyard: send queued records to the hub\n")
	b.WriteString("Documentation=https://github.com/mraj602/watchyard\n\n")
	b.WriteString("[Service]\nType=oneshot\n")
	for _, kv := range e.envPairs() {
		fmt.Fprintf(&b, "Environment=%s\n", systemdEnvQuote(kv[0]+"="+kv[1]))
	}
	args := []string{systemdQuote(e.WyPath)}
	for _, a := range Command {
		args = append(args, systemdQuote(a))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(args, " "))
	b.WriteString("Nice=10\nTimeoutStartSec=60\n")
	// `wy tick` starts the Smart Account Switch pass detached (it may wait minutes for a step
	// to end); the default KillMode=control-group would kill it when tick exits.
	b.WriteString("KillMode=" + SystemdKillMode + "\n")
	return b.String()
}

// SystemdTimer renders watchyard-flush.timer: 60 s after the previous run finished, and
// 30 s after boot.
func SystemdTimer() string {
	return "[Unit]\n" +
		"Description=Watchyard: flush every minute and at boot\n\n" +
		"[Timer]\n" +
		"OnBootSec=30s\n" +
		"OnUnitInactiveSec=60s\n" +
		"AccuracySec=1s\n" +
		"Unit=" + SystemdName + ".service\n\n" +
		"[Install]\n" +
		"WantedBy=timers.target\n"
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// LaunchdPlist renders the LaunchAgent (paths in it are always slash paths: it is only for macOS).
func LaunchdPlist(e Env) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + LaunchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(e.WyPath) + `</string>
`)
	for _, a := range Command {
		b.WriteString("\t\t<string>" + xmlEscape(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	if pairs := e.envPairs(); len(pairs) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, kv := range pairs {
			b.WriteString("\t\t<key>" + xmlEscape(kv[0]) + "</key>\n\t\t<string>" + xmlEscape(kv[1]) + "</string>\n")
		}
		b.WriteString("\t</dict>\n")
	}
	b.WriteString(`	<key>RunAtLoad</key>
	<true/>
	<key>StartInterval</key>
	<integer>60</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>LowPriorityIO</key>
	<true/>
	<key>StandardOutPath</key>
	<string>` + xmlEscape(path.Join(filepath.ToSlash(e.StateDir), "flush.log")) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(path.Join(filepath.ToSlash(e.StateDir), "flush.log")) + `</string>
</dict>
</plist>
`)
	return b.String()
}

// WindowsTaskXML renders a Task Scheduler definition: at logon and every minute.
func WindowsTaskXML(e Env) string {
	args := strings.Join(Command, " ")
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Watchyard: send queued records to the hub</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <Repetition>
        <Interval>PT1M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>PT1M</ExecutionTimeLimit>
    <Hidden>true</Hidden>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(e.WyPath) + `</Command>
      <Arguments>` + xmlEscape(args) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

// ---- install / uninstall ----------------------------------------------------------------

func (e Env) printf(format string, a ...any) {
	if e.Out != nil {
		fmt.Fprintf(e.Out, format, a...)
	}
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// Install writes the timer files and enables the timer. A failing service manager is
// reported in the returned error but the files stay, so the user can finish by hand.
func Install(e Env) (Result, error) {
	var res Result
	switch e.GOOS {
	case "linux":
		return res, installSystemd(e, &res)
	case "darwin":
		return res, installLaunchd(e, &res)
	case "windows":
		return res, installWindows(e, &res)
	}
	return res, fmt.Errorf("no timer support for %s; run `wy tick --quiet` every minute from cron", e.GOOS)
}

// Uninstall disables the timer and removes its files. Missing pieces are not errors.
func Uninstall(e Env) (removed []string, err error) {
	rm := func(p string) {
		if os.Remove(p) == nil {
			removed = append(removed, p)
		}
	}
	switch e.GOOS {
	case "linux":
		_, _ = e.Runner.Run("systemctl", "--user", "disable", "--now", SystemdName+".timer")
		dir := SystemdDir(e)
		rm(filepath.Join(dir, SystemdName+".timer"))
		rm(filepath.Join(dir, SystemdName+".service"))
		// Folders Install created are removed again when they are empty (os.Remove never
		// removes a folder that still holds something), so an undo leaves no trace.
		_ = os.Remove(dir)
		_ = os.Remove(filepath.Dir(dir))
		_, _ = e.Runner.Run("systemctl", "--user", "daemon-reload")
	case "darwin":
		_, _ = e.Runner.Run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", e.UID, LaunchdLabel))
		rm(LaunchAgentPath(e))
	case "windows":
		rm(filepath.Join(e.StateDir, WindowsXMLFile))
		e.printf("Remove the scheduled task with:\n  schtasks /Delete /TN \"%s\" /F\n", WindowsTask)
	}
	return removed, nil
}

func installSystemd(e Env, res *Result) error {
	dir := SystemdDir(e)
	svc, tm := filepath.Join(dir, SystemdName+".service"), filepath.Join(dir, SystemdName+".timer")
	if err := writeFile(svc, SystemdService(e)); err != nil {
		return err
	}
	if err := writeFile(tm, SystemdTimer()); err != nil {
		return err
	}
	res.Files = append(res.Files, svc, tm)
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", SystemdName + ".timer"},
	} {
		if out, err := e.Runner.Run("systemctl", args...); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s\nThe unit files are in %s; start the timer later with:\n  systemctl --user enable --now %s.timer\nOr, without systemd, run `wy tick --quiet` every minute from cron.",
				strings.Join(args, " "), err, strings.TrimSpace(string(out)), dir, SystemdName)
		}
	}
	// Linger keeps the user's timers running after logout (needed on SSH servers). We only
	// say so; changing it needs the user's decision.
	if e.User == "" {
		return nil
	}
	out, err := e.Runner.Run("loginctl", "show-user", e.User, "--property=Linger")
	if err != nil || !strings.Contains(string(out), "Linger=yes") {
		note := "To keep the timer running when you are logged out (SSH servers), run once:\n  loginctl enable-linger " + e.User
		res.Notes = append(res.Notes, note)
		e.printf("%s\n", note)
	}
	return nil
}

func installLaunchd(e Env, res *Result) error {
	p := LaunchAgentPath(e)
	if err := writeFile(p, LaunchdPlist(e)); err != nil {
		return err
	}
	res.Files = append(res.Files, p)
	domain := fmt.Sprintf("gui/%d", e.UID)
	_, _ = e.Runner.Run("launchctl", "bootout", domain+"/"+LaunchdLabel) // replace an older copy
	if out, err := e.Runner.Run("launchctl", "bootstrap", domain, p); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s\nThe agent file is %s; load it with:\n  launchctl bootstrap %s %s",
			err, strings.TrimSpace(string(out)), p, domain, p)
	}
	return nil
}

// UTF16LE encodes s as UTF-16LE with a byte order mark, which schtasks /XML requires.
func UTF16LE(s string) []byte {
	units := []uint16{0xFEFF}
	for _, r := range s {
		if r >= 0x10000 {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			units = append(units, uint16(r))
		}
	}
	out := make([]byte, 0, len(units)*2)
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func installWindows(e Env, res *Result) error {
	p := filepath.Join(e.StateDir, WindowsXMLFile)
	if err := os.MkdirAll(e.StateDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p, UTF16LE(WindowsTaskXML(e)), 0o600); err != nil {
		return err
	}
	res.Files = append(res.Files, p)
	note := fmt.Sprintf("Windows: create the scheduled task by running, in a normal (not admin) prompt:\n  schtasks /Create /TN \"%s\" /XML \"%s\"\nIt runs `wy tick --quiet` at logon and every minute.", WindowsTask, p)
	res.Notes = append(res.Notes, note)
	e.printf("%s\n", note)
	return nil
}
