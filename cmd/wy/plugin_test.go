package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var titleEvents = map[string]bool{"SessionStart": true, "UserPromptSubmit": true, "PermissionRequest": true,
	"Notification": true, "Stop": true, "StopFailure": true, "SessionEnd": true}

// The plugin registers exactly the events of docs/spec/states.md section 2, all async,
// all through a PATH lookup that does nothing when wy is missing.
func TestPluginHooksJSON(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "plugin", "watchyard", "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Async   bool   `json:"async"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	want := []string{"Notification", "PermissionDenied", "PermissionRequest", "PostToolUse", "PostToolUseFailure", "PreCompact",
		"PreToolUse", "SessionEnd", "SessionStart", "Stop", "StopFailure", "SubagentStop", "UserPromptSubmit"}
	var got []string
	for ev, groups := range doc.Hooks {
		got = append(got, ev)
		// The step entries (setup.md 8.6, Between steps): `wy hook --step <Event>`, async, for
		// every tool. PostToolUse and PostToolUseFailure have only that one; PreToolUse has it
		// after its AskUserQuestion|ExitPlanMode group.
		if ev == "PreToolUse" || ev == "PostToolUse" || ev == "PostToolUseFailure" {
			sg := groups[len(groups)-1]
			if len(sg.Hooks) != 1 || sg.Matcher != "*" || !sg.Hooks[0].Async ||
				sg.Hooks[0].Command != "command -v wy >/dev/null 2>&1 && wy hook --step "+ev+" || true" {
				t.Errorf("%s step entry: %+v", ev, sg)
			}
			if ev != "PreToolUse" {
				if len(groups) != 1 {
					t.Errorf("%s: %d groups", ev, len(groups))
				}
				continue
			}
			groups = groups[:len(groups)-1]
		}
		// The seven title events (docs/spec/terminal.md T6) carry a second group: the
		// synchronous `wy hook --title <Event>` entry with timeout 1.
		wantGroups := 1
		if titleEvents[ev] {
			wantGroups = 2
		}
		// SessionStart has a third: `wy hook --peers-context SessionStart` (peers.md 2.5),
		// synchronous with timeout 1, so its additionalContext reaches the session.
		if ev == "SessionStart" {
			wantGroups = 3
			ch := groups[len(groups)-1].Hooks
			if len(ch) != 1 || ch[0].Command != "command -v wy >/dev/null 2>&1 && wy hook --peers-context SessionStart || true" ||
				ch[0].Async || ch[0].Timeout != 1 || groups[len(groups)-1].Matcher != "" {
				t.Errorf("SessionStart peers context entry: %+v", groups[len(groups)-1])
			}
		}
		if len(groups) != wantGroups || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s: want %d group(s) with one hook each", ev, wantGroups)
		}
		if titleEvents[ev] {
			th := groups[1].Hooks[0]
			if wantCmd := "command -v wy >/dev/null 2>&1 && wy hook --title " + ev + " || true"; th.Type != "command" ||
				th.Command != wantCmd || th.Async || th.Timeout != 1 || len(groups[1].Hooks) != 1 || groups[1].Matcher != "" {
				t.Errorf("%s title entry: %+v", ev, groups[1])
			}
		}
		h := groups[0].Hooks[0]
		// SessionEnd is synchronous (an async hook can be killed as Claude Code exits and
		// `ended` would be lost) with a short timeout; every other event is async.
		if ev == "SessionEnd" {
			if h.Type != "command" || h.Async || h.Timeout <= 0 || h.Timeout > 5 {
				t.Errorf("SessionEnd must be sync with a short timeout: %+v", h)
			}
		} else if h.Type != "command" || !h.Async {
			t.Errorf("%s: %+v", ev, h)
		}
		if wantCmd := "command -v wy >/dev/null 2>&1 && wy hook " + ev + " || true"; h.Command != wantCmd {
			t.Errorf("%s: command %q", ev, h.Command)
		}
		if m := groups[0].Matcher; (ev == "PreToolUse") != (m != "") || (m != "" && m != "AskUserQuestion|ExitPlanMode") {
			t.Errorf("%s: matcher %q", ev, m)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events %v, want %v", got, want)
	}
}

// The hook command from the plugin, run through sh with wy missing from PATH, must be
// silent and exit 0.
func TestPluginCommandWithoutWy(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no sh")
	}
	env, _ := sandboxEnv(t)
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=/usr/bin:/bin"
		}
	}
	out, errOut := runShell(t, env, `command -v wy >/dev/null 2>&1 && wy hook Stop || true`)
	if out != "" || errOut != "" {
		t.Fatalf("output %q %q", out, errOut)
	}
}

func runShell(t *testing.T, env []string, script string) (stdout, stderr string) {
	t.Helper()
	c := exec.Command("/bin/sh", "-c", script)
	c.Env = env
	var o, e bytes.Buffer
	c.Stdout, c.Stderr = &o, &e
	if err := c.Run(); err != nil {
		t.Fatalf("sh: %v", err)
	}
	return o.String(), e.String()
}
