package model

import (
	"fmt"
	"regexp"
	"strings"
)

// Choices are the answers that, with a State, determine a Plan: every flag of setup.md
// section 4 (and, interactively, the screens' answers map one-to-one onto these fields).
// Mode flags that do not change the plan (--dry-run, --yes, --json, --detect, --undo*) are
// not here; the command layer owns them.
type Choices struct {
	// Primary is the folder shared items live in ("~" or absolute); empty = share.primary,
	// else ~/.claude.
	Primary string `json:"primary,omitempty"`
	// Labels maps a folder path or login email to a label and optional colour (--labels).
	Labels map[string]LabelColour `json:"labels,omitempty"`
	// Add creates one new folder per entry (--add). Accounts is the --accounts shorthand,
	// labels for existing folders in natural order, extras becoming Add.
	Add      []LabelColour `json:"add,omitempty"`
	Accounts []string      `json:"accounts,omitempty"`
	// Share is the raw --share spec as tokens: "defaults", "none", switch names, "-name".
	// Resolve it with ResolveShare.
	Share []string `json:"share,omitempty"`
	// Conflicts is the default resolution for every differing item: keep (default), primary, merge.
	Conflicts string `json:"conflicts,omitempty"`
	// Resolve overrides per item; key "<folder>:<item>" with folder in "~" form
	// (see ResolveKey), value keep|primary|folder|merge.
	Resolve map[string]string `json:"resolve,omitempty"`
	Hub     HubChoice         `json:"hub"`
	// Hub token inputs; the token itself never appears in a plan.
	TokenStdin bool   `json:"token_stdin,omitempty"`
	Code       string `json:"code,omitempty"`
	// CodeStdin reads the code (or a 22-character pairing secret) from the first stdin line
	// after the plan is accepted (wy machine add); a dry run never reads it.
	CodeStdin bool `json:"code_stdin,omitempty"`
	// Dial and Reach go to hub.dial and hub.reach with --hub join (machines.md 3).
	Dial        string `json:"dial,omitempty"`
	Reach       string `json:"reach,omitempty"`
	InsecureLAN bool   `json:"insecure_lan,omitempty"`
	Listen      string `json:"listen,omitempty"` // hub listen address, default 127.0.0.1:8787

	NoPlugin       bool   `json:"no_plugin,omitempty"`
	NoStatusLine   bool   `json:"no_statusline,omitempty"`
	NoLaunchers    bool   `json:"no_launchers,omitempty"`
	NamedLaunchers bool   `json:"named_launchers,omitempty"`
	LauncherDir    string `json:"launcher_dir,omitempty"` // default ~/.local/bin
	Login          string `json:"login,omitempty"`        // ask | never
	ExpectPlan     string `json:"expect_plan,omitempty"`  // checked by the command layer, not the planner
	Interactive    bool   `json:"interactive,omitempty"`  // a terminal is attached (login default: ask)
	// Modes are the picker modes answered in an interactive run (setup.md 8.5, Choosing in wy
	// setup): config key (slots.modes.<folder> or next.accounts.<key>.mode) -> protected or
	// aggressive. They go into the config.toml action of the plan.
	Modes map[string]string `json:"-"`
}

// ValidModeKey reports whether k is a config key a picker mode answer may set:
// slots.modes.<folder> or next.accounts.<key>.mode.
func ValidModeKey(k string) bool {
	if rest, ok := strings.CutPrefix(k, "slots.modes."); ok {
		return rest != ""
	}
	if rest, ok := strings.CutPrefix(k, "next.accounts."); ok {
		key, ok := strings.CutSuffix(rest, ".mode")
		return ok && key != ""
	}
	return false
}

// LabelColour is a label with an optional colour ("" = next free palette colour).
type LabelColour struct {
	Label  string `json:"label"`
	Colour string `json:"colour,omitempty"`
}

// Hub modes for HubChoice.Mode.
const (
	HubKeep  = "keep"
	HubHere  = "here"
	HubLater = "later"
	HubJoin  = "join"
	HubPull  = "pull" // pulled over SSH by the hub (machines.md 3.4)
)

// HubChoice is the --hub flag: keep, here, later, or join:<url>.
type HubChoice struct {
	Mode string `json:"mode"`
	URL  string `json:"url,omitempty"` // for join
}

// Resolution values for --conflicts / --resolve.
const (
	ResolveKeep    = "keep"
	ResolvePrimary = "primary"
	ResolveFolder  = "folder"
	ResolveMerge   = "merge"
)

// ResolveKey builds the Choices.Resolve key for a folder ("~" form) and item.
func ResolveKey(folder, item string) string { return folder + ":" + item }

var labelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)
var colourRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// ValidLabel reports whether s is a valid label (`^[a-z0-9][a-z0-9-]{0,23}$`).
func ValidLabel(s string) bool { return labelRE.MatchString(s) }

// ResolveColour turns a palette name or "#rrggbb" into "#rrggbb" ("" and ok=false if invalid).
func ResolveColour(s string) (string, bool) {
	if colourRE.MatchString(s) {
		return strings.ToLower(s), true
	}
	for _, c := range Palette {
		if c.Name == s {
			return c.Hex, true
		}
	}
	return "", false
}

// ShareSet is the resolved --share spec.
type ShareSet struct {
	On map[Switch]bool
	// ExplicitOff are switches named with "-" explicitly: only these may un-share links that
	// already exist (setup.md section 4, --share), and only with --yes.
	ExplicitOff map[Switch]bool
}

// ResolveShare resolves --share tokens. An empty list means "defaults". "defaults" is all
// seven switches, except mcp when handMadeMCP; "none" is no switch; a bare switch name
// replaces the defaults (so "settings,plugins" is exactly those two); "-name" removes one
// from whatever is on (it applies after "defaults" is expanded and marks it ExplicitOff).
// Unknown tokens are an error. Plugins is forced on while Settings is on.
func ResolveShare(tokens []string, handMadeMCP bool) (ShareSet, error) {
	s := ShareSet{On: map[Switch]bool{}, ExplicitOff: map[Switch]bool{}}
	defaults := func() {
		for _, sw := range AllSwitches {
			if sw == SwitchMCP && handMadeMCP {
				continue
			}
			s.On[sw] = true
		}
	}
	if len(tokens) == 0 {
		defaults()
	}
	var offs []Switch
	named := false
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		switch {
		case t == "":
		case t == "defaults":
			defaults()
		case t == "none":
			s.On = map[Switch]bool{}
		case strings.HasPrefix(t, "-"):
			sw, ok := parseSwitch(t[1:])
			if !ok {
				return s, fmt.Errorf("unknown share switch %q", t[1:])
			}
			offs = append(offs, sw)
		default:
			sw, ok := parseSwitch(t)
			if !ok {
				return s, fmt.Errorf("unknown share switch %q", t)
			}
			if !named && len(s.On) > 0 && !hasToken(tokens, "defaults") {
				s.On = map[Switch]bool{}
			}
			named = true
			s.On[sw] = true
		}
	}
	for _, sw := range offs {
		delete(s.On, sw)
		s.ExplicitOff[sw] = true
	}
	if s.On[SwitchSettings] {
		s.On[SwitchPlugins] = true
	}
	return s, nil
}

func hasToken(tokens []string, want string) bool {
	for _, t := range tokens {
		if strings.TrimSpace(t) == want {
			return true
		}
	}
	return false
}

func parseSwitch(s string) (Switch, bool) {
	for _, sw := range AllSwitches {
		if string(sw) == s {
			return sw, true
		}
	}
	return "", false
}
