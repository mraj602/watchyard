package timer

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/timer -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from golden:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func env(goos string) Env {
	return Env{
		GOOS: goos, Home: "/home/alice", UID: 1000, User: "alice",
		WyPath: "/home/alice/.local/bin/wy", StateDir: "/home/alice/.local/state/watchyard",
		Getenv: func(string) string { return "" },
	}
}

func TestGoldenUnits(t *testing.T) {
	golden(t, "watchyard-flush.service.golden", SystemdService(env("linux")))
	golden(t, "watchyard-flush.timer.golden", SystemdTimer())
	golden(t, "com.watchyard.flush.plist.golden", LaunchdPlist(env("darwin")))
	w := env("windows")
	w.WyPath = `C:\Users\alice\AppData\Local\Programs\wy\wy.exe`
	golden(t, "watchyard-flush.task.xml.golden", WindowsTaskXML(w))
}

func TestGoldenWithFolderOverrides(t *testing.T) {
	e := env("linux")
	e.WyPath = "/opt/my tools/wy"
	vars := map[string]string{"WY_HOME": "/srv/wy home", "WY_STATE_DIR": "/srv/state"}
	e.Getenv = func(k string) string { return vars[k] }
	golden(t, "watchyard-flush.service.overrides.golden", SystemdService(e))
	e.GOOS = "darwin"
	golden(t, "com.watchyard.flush.overrides.plist.golden", LaunchdPlist(e))
}

type fakeRunner struct {
	calls  []string
	failOn string
	out    map[string]string
}

func (f *fakeRunner) Run(name string, args ...string) ([]byte, error) {
	c := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, c)
	if f.failOn != "" && strings.Contains(c, f.failOn) {
		return []byte("boom"), errors.New("exit 1")
	}
	return []byte(f.out[name]), nil
}

func fakeHome(t *testing.T, goos string) (Env, *fakeRunner, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	r := &fakeRunner{out: map[string]string{}}
	out := &bytes.Buffer{}
	e := env(goos)
	e.Home = home
	e.StateDir = filepath.Join(home, "state")
	e.Runner, e.Out = r, out
	return e, r, out
}

func TestInstallLinuxUsesInjectedRunnerOnly(t *testing.T) {
	e, r, out := fakeHome(t, "linux")
	r.out["loginctl"] = "Linger=no\n"
	res, err := Install(e)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.Home, ".config", "systemd", "user")
	for _, n := range []string{"watchyard-flush.service", "watchyard-flush.timer"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("missing %s: %v", n, err)
		}
	}
	if len(res.Files) != 2 {
		t.Errorf("files: %v", res.Files)
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now watchyard-flush.timer",
		"loginctl show-user alice --property=Linger",
	}
	if strings.Join(r.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "enable-linger") {
			t.Errorf("linger must only be printed, not run: %s", c)
		}
	}
	if !strings.Contains(out.String(), "loginctl enable-linger alice") {
		t.Errorf("no linger hint: %q", out.String())
	}
}

func TestInstallLinuxNoLingerHintWhenEnabled(t *testing.T) {
	e, r, out := fakeHome(t, "linux")
	r.out["loginctl"] = "Linger=yes\n"
	if _, err := Install(e); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "enable-linger") {
		t.Errorf("hint printed although linger is on: %q", out.String())
	}
}

func TestInstallLinuxHonoursXDGConfigHome(t *testing.T) {
	e, _, _ := fakeHome(t, "linux")
	x := filepath.Join(e.Home, "xdg")
	e.Getenv = func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return x
		}
		return ""
	}
	if _, err := Install(e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(x, "systemd", "user", "watchyard-flush.timer")); err != nil {
		t.Error(err)
	}
}

func TestInstallLinuxSystemctlFailureKeepsFilesAndExplains(t *testing.T) {
	e, r, _ := fakeHome(t, "linux")
	r.failOn = "enable --now"
	_, err := Install(e)
	if err == nil || !strings.Contains(err.Error(), "cron") {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(filepath.Join(e.Home, ".config", "systemd", "user", "watchyard-flush.timer")); serr != nil {
		t.Error("unit files should stay for manual completion")
	}
}

func TestUninstallLinux(t *testing.T) {
	e, r, _ := fakeHome(t, "linux")
	if _, err := Install(e); err != nil {
		t.Fatal(err)
	}
	r.calls = nil
	removed, err := Uninstall(e)
	if err != nil || len(removed) != 2 {
		t.Fatalf("removed %v, %v", removed, err)
	}
	if got := strings.Join(r.calls, "|"); got != "systemctl --user disable --now watchyard-flush.timer|systemctl --user daemon-reload" {
		t.Errorf("calls %q", got)
	}
	if _, err := Uninstall(e); err != nil { // second time: nothing to remove, no error
		t.Error(err)
	}
}

func TestUninstallLinuxRemovesEmptyUnitFolders(t *testing.T) {
	e, _, _ := fakeHome(t, "linux")
	if _, err := Install(e); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.Home, ".config", "systemd")); err == nil {
		t.Error("the empty systemd folder Install created is left behind")
	}
	// A folder that holds something else stays.
	if _, err := Install(e); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(e.Home, ".config", "systemd", "user", "mine.service")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("someone else's unit was removed")
	}
}

func TestInstallAndUninstallMac(t *testing.T) {
	e, r, _ := fakeHome(t, "darwin")
	if _, err := Install(e); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(e.Home, "Library", "LaunchAgents", "com.watchyard.flush.plist")
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	if got := r.calls[len(r.calls)-1]; got != "launchctl bootstrap gui/1000 "+p {
		t.Errorf("last call %q", got)
	}
	if _, err := Uninstall(e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("plist not removed")
	}
}

func TestInstallWindowsPrintsInstructions(t *testing.T) {
	e, r, out := fakeHome(t, "windows")
	if _, err := Install(e); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 {
		t.Errorf("windows install must not run commands: %v", r.calls)
	}
	b, err := os.ReadFile(filepath.Join(e.StateDir, WindowsXMLFile))
	if err != nil || len(b) < 4 || b[0] != 0xFF || b[1] != 0xFE {
		t.Fatalf("xml file not UTF-16LE with BOM: %v", err)
	}
	if !strings.Contains(out.String(), "schtasks /Create") {
		t.Errorf("no instructions: %q", out.String())
	}
}

func TestUnsupportedOS(t *testing.T) {
	e, _, _ := fakeHome(t, "plan9")
	if _, err := Install(e); err == nil {
		t.Error("want error")
	}
}

func TestSystemdQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/wy":      "/usr/bin/wy",
		"/opt/my tools/wy": `"/opt/my tools/wy"`,
		`a"b`:              `"a\"b"`,
		"50%":              `"50%%"`,
		// Regression: a newline must not end the directive and start a new one.
		"/x\nExecStartPre=/bin/evil": `"/x\x0aExecStartPre=/bin/evil"`,
		"a\rb\tc\x00d\x7f":           `"a\x0db\x09c\x00d\x7f"`,
		`a\b`:                        `"a\\b"`,
	} {
		if got := systemdQuote(in); got != want {
			t.Errorf("systemdQuote(%q) = %q, want %q", in, got, want)
		}
	}
	// Environment= does no "$" expansion: a "$" there stays single (systemd.exec(5)).
	if got := systemdEnvQuote(`WY_STATE_DIR=/d/$x`); got != `"WY_STATE_DIR=/d/$x"` {
		t.Errorf("systemdEnvQuote = %q", got)
	}
}

// wy tick starts the Smart Account Switch pass detached: the service must not kill it when
// tick exits (live run 1.3.3-pre3: the default KillMode=control-group did).
func TestSystemdServiceKeepsDetachedPass(t *testing.T) {
	if s := SystemdService(env("linux")); !strings.Contains(s, "\nKillMode=process\n") {
		t.Errorf("no KillMode=process:\n%s", s)
	}
}
