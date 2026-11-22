package plan_test

import (
	"encoding/json"
	"testing"

	"github.com/mraj602/watchyard/internal/setup/detect/detecttest"
	"github.com/mraj602/watchyard/internal/setup/model"
	"github.com/mraj602/watchyard/internal/setup/plan"
)

// Accounts lists the folders the plan keeps or creates with their final labels; the picker
// modes answered in an interactive run go into the config.toml action (setup.md 8.5, Choosing
// in wy setup), and a key or value outside slots.modes / next.accounts.<key>.mode is refused.
func TestAccountsAndModes(t *testing.T) {
	h := detecttest.Fresh(t)
	st := h.State()
	c := model.Choices{Add: []model.LabelColour{{Label: "work"}}}
	accs, err := plan.Accounts(st, c)
	if err != nil {
		t.Fatal(err)
	}
	var added *plan.Account
	for i := range accs {
		if accs[i].New {
			added = &accs[i]
		}
	}
	if added == nil || added.Label != "work" || added.Folder != "~/.claude-2" {
		t.Fatalf("accounts %+v", accs)
	}

	c.Modes = map[string]string{"slots.modes.~/.claude-2": "protected", "next.accounts.me@example.test.mode": "aggressive"}
	p := mustPlan(t, st, c)
	found := false
	for _, a := range p.Actions {
		if a.Op != model.OpConfigSet {
			continue
		}
		var m map[string]any
		_ = json.Unmarshal(a.Value, &m)
		if m["slots.modes.~/.claude-2"] == "protected" && m["next.accounts.me@example.test.mode"] == "aggressive" {
			found = true
		}
	}
	if !found {
		t.Fatalf("modes not in the config action\n%s", dump(p))
	}
	for _, bad := range []map[string]string{
		{"slots.labels.~/.claude-2": "protected"},
		{"slots.modes.~/.claude-2": "normal"},
		{"next.accounts.me@example.test.cap_7d": "protected"},
	} {
		c.Modes = bad
		if _, err := plan.PlanWith(st, c, opt); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
