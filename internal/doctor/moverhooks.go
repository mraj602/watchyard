package doctor

import (
	"regexp"

	"github.com/mraj602/watchyard/internal/identity"
)

// wyStepHookRe matches the step hook `wy hook --step ...` (setup.md 8.6, Between steps).
var wyStepHookRe = regexp.MustCompile(`(^|[\s/\\"'])wy(\.exe)?["']?\s+hook\s+--step(\s|$|["'])`)

// checkMoverHooks warns, with Smart Account Switch on, when a logged-in folder's settings
// carry `wy hook` entries by hand but not the step hooks (PreToolUse and PostToolUse with
// `wy hook --step`) the mover uses to move a session between steps: without them every
// mid-turn move may stop a session inside a step. The plugin has them (not inspected: an
// enabled plugin is taken as complete; its own checkHooks line covers it).
func checkMoverHooks(rep *Report, slot identity.Slot, s settings, serr error, on bool) {
	if !on || serr != nil || !s.exists || s.watchyardPlugin() != "" {
		return
	}
	has := func(ev string) bool {
		for _, c := range s.hooks[ev] {
			if wyStepHookRe.MatchString(c) {
				return true
			}
		}
		return false
	}
	var missing []string
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		if !has(ev) {
			missing = append(missing, ev)
		}
	}
	if len(missing) == 0 {
		return
	}
	rep.add(Check{Name: "mover-hooks", Slot: slot.Name, Level: Warn,
		Detail: "Smart Account Switch is on but `wy hook --step` is missing for: " + join(missing) + ": a mid-turn move cannot wait for a step to end",
		Fix:    "use the watchyard plugin (its hooks.json has them), or add `wy hook --step PreToolUse`, `wy hook --step PostToolUse` and `wy hook --step PostToolUseFailure` on every tool (docs/spec/setup.md 8.6, Between steps)"})
}

func join(a []string) string {
	s := ""
	for i, x := range a {
		if i > 0 {
			s += ", "
		}
		s += x
	}
	return s
}
