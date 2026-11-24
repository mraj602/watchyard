// Package config reads and writes config.toml (hub address, privacy, hooks, status line,
// outbox and slot settings), keeping unknown keys on rewrite.
//
// Contract: docs/spec/paths.md section 4.
//
// The file is held as a generic TOML document so keys this version does not know are
// carried through a rewrite untouched. Comments are not preserved (go-toml/v2 does not
// keep them).
//
// Depends on: paths (the caller passes paths.Dirs.Config.Path).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/mraj602/watchyard/internal/atomicfile"
)

// FileName is the only name wy owns inside the config folder (paths.md section 2).
const FileName = "config.toml"

// Version is the schema version written to new files.
const Version = 1

// ErrNotFound means a key is neither set nor a known key.
var ErrNotFound = errors.New("key not found")

// Config is the typed view of the known keys, with defaults filled in.
type Config struct {
	Version int `toml:"version"`
	Hub     struct {
		URL         string `toml:"url"`
		InsecureLAN bool   `toml:"insecure_lan"`
		// Reach is how this machine's records reach the hub: direct, forward or pull
		// (machines.md 3). Dial is the forward end ("127.0.0.1:47830" or
		// "unix:<path>") the client connects to while keeping URL for TLS and Host.
		Reach string `toml:"reach"`
		Dial  string `toml:"dial"`
	} `toml:"hub"`
	// Push.Direct is direct ntfy from this machine (machines.md 5). The topic and token
	// are never here: they live in <state>/push/direct.json.
	Push struct {
		Direct struct {
			Enabled bool   `toml:"enabled"`
			Server  string `toml:"server"`
			Private bool   `toml:"private"`
			AfterS  int    `toml:"after_s"`
		} `toml:"direct"`
	} `toml:"push"`
	// Update is the [update] table of machines.md 7.3.
	Update struct {
		BaseURL           string `toml:"base_url"`
		VerifyAttestation bool   `toml:"verify_attestation"`
	} `toml:"update"`
	// Machines is the operator's [machines] table (machines.md 4.4).
	Machines struct {
		HubAdmin string `toml:"hub_admin"`
	} `toml:"machines"`
	Privacy struct {
		LastMessage bool `toml:"last_message"`
	} `toml:"privacy"`
	Hooks struct {
		ToolEvents bool `toml:"tool_events"`
	} `toml:"hooks"`
	Statusline struct {
		Wrap string `toml:"wrap"`
		// Show: append Watchyard's part ("label · 5h · ctx") to the line. Off still records
		// the limit samples; only the text is left out.
		Show bool `toml:"show"`
	} `toml:"statusline"`
	Outbox struct {
		Fsync    bool `toml:"fsync"`
		MaxMB    int  `toml:"max_mb"`
		KeepDays int  `toml:"keep_days"`
	} `toml:"outbox"`
	Slots struct {
		Extra   []string          `toml:"extra"`
		Labels  map[string]string `toml:"labels"`
		Colours map[string]string `toml:"colours"`
		// Modes is slots.modes: the picker mode proposed for each folder's login on a
		// machine with a hub (setup.md 8.5, Kind): protected or aggressive.
		Modes map[string]string `toml:"modes"`
	} `toml:"slots"`
	// Share is the [share] table of setup.md 9.2.
	Share struct {
		Primary string `toml:"primary"`
		// MCP is true, false or "external" (a hand-made script syncs it); read it with MCPMode.
		MCP        any      `toml:"mcp"`
		MCPExclude []string `toml:"mcp_exclude"`
	} `toml:"share"`
	// Terminal is the [terminal] table of docs/spec/terminal.md T12.
	Terminal struct {
		// Titles is written only by `wy term titles on|off` (a config_set in its plan).
		Titles       bool   `toml:"titles"`
		Band         bool   `toml:"band"`
		PaneAutoOpen bool   `toml:"pane_auto_open"`
		Source       string `toml:"source"` // auto | hub | local
		ASCII        bool   `toml:"ascii"`
	} `toml:"terminal"`
	// Peers is the [peers] table of docs/spec/peers.md. Enabled turns on the session
	// files `wy hook` writes (2.4); `wy agent enable` sets it too. Delivery is the
	// inbox delivery mode chosen by the delivery probe (8.5): honest | accept | courier.
	Peers struct {
		Enabled  bool   `toml:"enabled"`
		Delivery string `toml:"delivery"`
		// HubMayWiden: accept a hub-signed matrix that widens (keys.md 5.4). Default true.
		HubMayWiden bool `toml:"hub_may_widen"`
	} `toml:"peers"`
	// Next is the [next] table of `wy account next` (docs/spec/setup.md 8.5).
	Next Next `toml:"next"`
	// Account is the [account] table: Smart Account Switch (setup.md 8.6). SmartSwitch on
	// lets `wy tick` move sessions by itself (default off; `wy move` by hand works either way).
	// MoveLaunch is the command a moved session is started with ({dir}, {n}, {label}; empty:
	// env CLAUDE_CONFIG_DIR={dir} claude).
	Account struct {
		SmartSwitch bool   `toml:"smart_switch"`
		MoveLaunch  string `toml:"move_launch"`
		// SmartSwitchScope is which sessions the switch may move: all (the default), tag:<tag>
		// (sessions with that peers tag) or sessions:<id>,<id> (ids or 8+ character prefixes).
		SmartSwitchScope string `toml:"smart_switch_scope"`
		// MoveOrgs: the organisations a session may move to: any (the default), same-first or
		// same. The Move* numbers are the decision rules of setup.md 8.6 (each key's default and
		// reason are in its Decision table).
		MoveOrgs             string  `toml:"move_orgs"`
		MoveCap5h            float64 `toml:"move_cap_5h"`
		MoveCap7d            float64 `toml:"move_cap_7d"`
		MoveCap5hProtected   float64 `toml:"move_cap_5h_protected"`
		MoveCap7dProtected   float64 `toml:"move_cap_7d_protected"`
		MoveHysteresis       float64 `toml:"move_hysteresis"`
		MoveBurnMin          float64 `toml:"move_burn_min"`
		MoveWeekSpanH        float64 `toml:"move_week_span_h"`
		MoveSpikePasses      float64 `toml:"move_spike_passes"`
		MoveSpikeNearMin     float64 `toml:"move_spike_near_min"`
		MoveCooldownMin      float64 `toml:"move_cooldown_min"`
		MoveNoReturnMin      float64 `toml:"move_no_return_min"`
		MoveAccountMoves     float64 `toml:"move_account_moves"`
		MoveAccountWindowMin float64 `toml:"move_account_window_min"`
		MoveStoppedWaitMin   float64 `toml:"move_stopped_wait_min"`
		MoveOwnerGraceMin    float64 `toml:"move_owner_grace_min"`
		MoveStepWaitMin      float64 `toml:"move_step_wait_min"`
	} `toml:"account"`
}

// Next is the [next] table: the global settings of `wy account next` and, under
// next.accounts, the settings of each account keyed by email, seat id, folder or label.
type Next struct {
	Strategy     string                 `toml:"strategy"`
	Model        string                 `toml:"model"`
	Session5h    float64                `toml:"session_5h"`
	Session7d    float64                `toml:"session_7d"`
	SessionMin   float64                `toml:"session_min"`
	StaleMin     float64                `toml:"stale_min"`
	ReserveMin   float64                `toml:"reserve_min"`
	StickyMargin float64                `toml:"sticky_margin"`
	Accounts     map[string]NextAccount `toml:"accounts"`
}

// NextAccount is one next.accounts.<key> table. A zero number means unset.
type NextAccount struct {
	Mode       string  `toml:"mode"`
	Cap5h      float64 `toml:"cap_5h"`
	Cap7d      float64 `toml:"cap_7d"`
	CapModel   float64 `toml:"cap_model"`
	ReservePct float64 `toml:"reserve_pct"`
	Priority   float64 `toml:"priority"`
	Weight     float64 `toml:"weight"`
	Exclude    bool    `toml:"exclude"`
	Hours      string  `toml:"hours"`
	Days       string  `toml:"days"`
}

// MCPMode returns share.mcp as "true", "false" or "external" ("" when unset).
func (c Config) MCPMode() string {
	switch v := c.Share.MCP.(type) {
	case bool:
		if v {
			return "true"
		}
		return "false"
	case string:
		return v
	}
	return ""
}

// Defaults returns the configuration of a machine with no config.toml.
func Defaults() Config {
	var c Config
	c.Version = Version
	c.Privacy.LastMessage = true
	c.Hub.Reach = ReachDirect
	c.Push.Direct.Server = DefaultDirectServer
	c.Push.Direct.Private = true
	c.Push.Direct.AfterS = DefaultDirectAfterS
	c.Outbox.Fsync = true
	c.Outbox.MaxMB = 50
	c.Outbox.KeepDays = 7
	c.Slots.Extra = []string{}
	c.Slots.Labels = map[string]string{}
	c.Slots.Colours = map[string]string{}
	c.Slots.Modes = map[string]string{}
	c.Share.MCPExclude = []string{}
	c.Statusline.Show = true
	c.Terminal.Band = true
	c.Terminal.PaneAutoOpen = true
	c.Terminal.Source = TerminalSourceAuto
	c.Peers.Delivery = "honest"
	c.Peers.HubMayWiden = true
	c.Account.SmartSwitchScope = "all"
	c.Account.MoveOrgs = "any"
	for k, v := range moveDefaults {
		*c.moveField(k) = v
	}
	c.Update.VerifyAttestation = true
	c.Next = Next{Strategy: NextStrategyLanes, Session5h: 15, Session7d: 2, SessionMin: 120, StaleMin: 30,
		ReserveMin: 12, StickyMargin: 10, Accounts: map[string]NextAccount{}}
	return c
}

type kind int

const (
	kString kind = iota
	kBool
	kInt
	kStrings
	kMCP   // true, false or "external" (share.mcp)
	kFloat // a number >= 0 (next.*)
)

// spec describes one known key: its type, default and whether `wy config set` may change it.
// A managed key is changed only through a plan (SetJSON, setup.md 6.1 config_set) by the
// command that owns it; `wy config set` refuses it and names that command.
type spec struct {
	kind     kind
	def      any
	readOnly bool
	managed  string // the command that owns the key, e.g. "wy term titles on|off"
}

var known = map[string]spec{
	"version":                    {kind: kInt, def: int64(Version), readOnly: true},
	"hub.url":                    {kind: kString, def: ""},
	"hub.insecure_lan":           {kind: kBool, def: false},
	"hub.reach":                  {kind: kString, def: ReachDirect},
	"hub.dial":                   {kind: kString, def: ""},
	"push.direct.enabled":        {kind: kBool, def: false},
	"push.direct.server":         {kind: kString, def: DefaultDirectServer},
	"push.direct.private":        {kind: kBool, def: true},
	"push.direct.after_s":        {kind: kInt, def: int64(DefaultDirectAfterS)},
	"update.base_url":            {kind: kString, def: ""},
	"update.verify_attestation":  {kind: kBool, def: true},
	"machines.hub_admin":         {kind: kString, def: ""},
	"privacy.last_message":       {kind: kBool, def: true},
	"hooks.tool_events":          {kind: kBool, def: false},
	"statusline.wrap":            {kind: kString, def: ""},
	"statusline.show":            {kind: kBool, def: true},
	"outbox.fsync":               {kind: kBool, def: true},
	"outbox.max_mb":              {kind: kInt, def: int64(50)},
	"outbox.keep_days":           {kind: kInt, def: int64(7)},
	"slots.extra":                {kind: kStrings, def: []any{}},
	"share.primary":              {kind: kString, def: ""},
	"share.mcp":                  {kind: kMCP, def: ""},
	"share.mcp_exclude":          {kind: kStrings, def: []any{}},
	"terminal.titles":            {kind: kBool, def: false, managed: "wy term titles on|off"},
	"terminal.band":              {kind: kBool, def: true},
	"terminal.pane_auto_open":    {kind: kBool, def: true},
	"terminal.source":            {kind: kString, def: TerminalSourceAuto},
	"terminal.ascii":             {kind: kBool, def: false},
	"peers.enabled":              {kind: kBool, def: false},
	"peers.delivery":             {kind: kString, def: "honest"},
	"peers.hub_may_widen":        {kind: kBool, def: true},
	"account.smart_switch":       {kind: kBool, def: false},
	"account.move_launch":        {kind: kString, def: ""},
	"account.smart_switch_scope": {kind: kString, def: "all"},
	"account.move_orgs":          {kind: kString, def: "any"},
	"next.strategy":              {kind: kString, def: NextStrategyLanes},
	"next.model":                 {kind: kString, def: ""},
	"next.session_5h":            {kind: kFloat, def: float64(15)},
	"next.session_7d":            {kind: kFloat, def: float64(2)},
	"next.session_min":           {kind: kFloat, def: float64(120)},
	"next.stale_min":             {kind: kFloat, def: float64(30)},
	"next.reserve_min":           {kind: kFloat, def: float64(12)},
	"next.sticky_margin":         {kind: kFloat, def: float64(10)},
}

// moveDefaults are the defaults of the account.move_* numbers (setup.md 8.6, Decision).
var moveDefaults = map[string]float64{
	"move_cap_5h": 95, "move_cap_7d": 98, "move_cap_5h_protected": 80, "move_cap_7d_protected": 80,
	"move_hysteresis": 5, "move_burn_min": 10, "move_week_span_h": 5, "move_spike_passes": 2, "move_spike_near_min": 30,
	"move_cooldown_min": 20, "move_no_return_min": 60, "move_account_moves": 3, "move_account_window_min": 15,
	"move_stopped_wait_min": 20, "move_owner_grace_min": 0, "move_step_wait_min": 3,
}

func init() {
	for k, v := range moveDefaults {
		known["account."+k] = spec{kind: kFloat, def: v}
	}
}

// moveField is the Account field of the move_* key k.
func (c *Config) moveField(k string) *float64 {
	a := &c.Account
	switch k {
	case "move_cap_5h":
		return &a.MoveCap5h
	case "move_cap_7d":
		return &a.MoveCap7d
	case "move_cap_5h_protected":
		return &a.MoveCap5hProtected
	case "move_cap_7d_protected":
		return &a.MoveCap7dProtected
	case "move_hysteresis":
		return &a.MoveHysteresis
	case "move_burn_min":
		return &a.MoveBurnMin
	case "move_week_span_h":
		return &a.MoveWeekSpanH
	case "move_spike_passes":
		return &a.MoveSpikePasses
	case "move_spike_near_min":
		return &a.MoveSpikeNearMin
	case "move_cooldown_min":
		return &a.MoveCooldownMin
	case "move_no_return_min":
		return &a.MoveNoReturnMin
	case "move_account_moves":
		return &a.MoveAccountMoves
	case "move_account_window_min":
		return &a.MoveAccountWindowMin
	case "move_stopped_wait_min":
		return &a.MoveStoppedWaitMin
	case "move_owner_grace_min":
		return &a.MoveOwnerGraceMin
	case "move_step_wait_min":
		return &a.MoveStepWaitMin
	}
	panic("config: unknown move key " + k)
}

// labelsPrefix: "slots.labels.<slot>" is a free-form key; the slot (e.g. "~/.claude-2")
// may contain dots, so everything after the prefix is the map key. coloursPrefix is the same
// for "slots.colours.<slot>" (setup.md 9.2), whose value must be "#rrggbb", and modesPrefix
// for "slots.modes.<slot>" (setup.md 8.5, Kind), whose value is protected or aggressive.
const (
	labelsPrefix  = "slots.labels."
	coloursPrefix = "slots.colours."
	modesPrefix   = "slots.modes."
)

// slotMaps are the free-form slot tables, by prefix.
var slotMaps = map[string]string{labelsPrefix: "labels", coloursPrefix: "colours", modesPrefix: "modes"}

// ValidSlotMode reports whether s is a slots.modes value (protected or aggressive).
func ValidSlotMode(s string) bool { return s == "protected" || s == "aggressive" }

func slotMapKey(key string) (table, rest string, ok bool) {
	for prefix, t := range slotMaps {
		if r, found := strings.CutPrefix(key, prefix); found {
			return t, r, true
		}
	}
	return "", "", false
}

// Known lists the keys `wy config get/set` understands, sorted.
func Known() []string {
	out := make([]string, 0, len(known)+1)
	for k := range known {
		out = append(out, k)
	}
	out = append(out, labelsPrefix+"<slot>", coloursPrefix+"<slot>", modesPrefix+"<slot>", nextAccountsPrefix+"<account>.<field>")
	sort.Strings(out)
	return out
}

// File is a loaded config.toml. The zero value is not usable; call Load.
type File struct {
	dir string
	doc map[string]any
}

// Load reads <dir>/config.toml. A missing file is not an error: the result is empty and
// behaves as the defaults. Load never creates anything.
func Load(dir string) (*File, error) {
	f := &File{dir: dir, doc: map[string]any{}}
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(data, &f.doc); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, FileName), err)
	}
	if f.doc == nil {
		f.doc = map[string]any{}
	}
	return f, nil
}

// Path is where Save writes.
func (f *File) Path() string { return filepath.Join(f.dir, FileName) }

// MostPrivate is the configuration used when config.toml cannot be read at all: every
// privacy switch at its most private setting, everything else at its default.
func MostPrivate() Config {
	c := Defaults()
	c.Privacy.LastMessage = false
	return c
}

// Typed decodes the known keys over the defaults. A known key with the wrong type is an
// error (so a hand edit such as `max_mb = "lots"` is reported, not silently ignored).
//
// The error does not discard the rest: the Config returned with it holds every key that
// did decode, with each key that did not at its default, except privacy keys, which fall
// back to their most private value. A typo in an unrelated key therefore never re-enables
// content capture.
func (f *File) Typed() (Config, error) {
	c := Defaults()
	if data, err := toml.Marshal(f.doc); err == nil {
		if err := toml.Unmarshal(data, &c); err == nil {
			c.normalize()
			return c, nil
		}
	}
	c = Defaults()
	var errs []error
	keys := make([]string, 0, len(known)+1)
	for k := range known {
		keys = append(keys, k)
	}
	keys = append(keys, "slots.labels", "slots.colours", "slots.modes", "next.accounts")
	sort.Strings(keys)
	for _, k := range keys {
		parts := strings.Split(k, ".")
		v := lookupPath(f.doc, parts)
		private := strings.HasPrefix(k, "privacy.")
		if v == nil {
			// "privacy = 3" replaces the whole table: nothing to decode, so be private.
			if _, isTable := f.doc[parts[0]].(map[string]any); private && f.doc[parts[0]] != nil && !isTable {
				c.Privacy.LastMessage = false
			}
			continue
		}
		var nested any = v
		for i := len(parts) - 1; i >= 0; i-- {
			nested = map[string]any{parts[i]: nested}
		}
		tmp := c
		data, err := toml.Marshal(nested)
		if err == nil {
			err = toml.Unmarshal(data, &tmp)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k, err))
			if private {
				c.Privacy.LastMessage = false
			}
			continue
		}
		c = tmp
	}
	c.normalize()
	if len(errs) == 0 {
		errs = append(errs, errors.New("unreadable value"))
	}
	return c, fmt.Errorf("%s: %w", f.Path(), errors.Join(errs...))
}

func (c *Config) normalize() {
	if c.Slots.Extra == nil {
		c.Slots.Extra = []string{}
	}
	if c.Slots.Labels == nil {
		c.Slots.Labels = map[string]string{}
	}
	if c.Slots.Colours == nil {
		c.Slots.Colours = map[string]string{}
	}
	if c.Slots.Modes == nil {
		c.Slots.Modes = map[string]string{}
	}
	if c.Share.MCPExclude == nil {
		c.Share.MCPExclude = []string{}
	}
	if c.Next.Accounts == nil {
		c.Next.Accounts = map[string]NextAccount{}
	}
}

// Get returns the value of key: the stored value, else the default of a known key.
// Tables come back as map[string]any, arrays as []any, integers as int64.
func (f *File) Get(key string) (any, error) {
	if v, ok := f.lookup(key); ok {
		return v, nil
	}
	if sp, ok := known[key]; ok {
		return sp.def, nil
	}
	if key == "slots.labels" || key == "slots.colours" || key == "slots.modes" || key == "next.accounts" {
		return map[string]any{}, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
}

func (f *File) lookup(key string) (any, bool) {
	if acct, field, ok := nextAccountKey(key); ok {
		m, _ := lookupPath(f.doc, []string{"next", "accounts", acct}).(map[string]any)
		v, ok := m[field]
		return v, ok
	}
	if table, rest, ok := slotMapKey(key); ok && rest != "" {
		m, _ := lookupPath(f.doc, []string{"slots", table}).(map[string]any)
		v, ok := m[rest]
		return v, ok
	}
	v := lookupPath(f.doc, strings.Split(key, "."))
	return v, v != nil
}

func lookupPath(m map[string]any, parts []string) any {
	var cur any = m
	for _, p := range parts {
		t, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = t[p]
		if !ok {
			return nil
		}
	}
	return cur
}

// Set parses args according to the type of key and stores it. Only known keys can be set;
// unknown keys already in the file are left alone. Booleans take true/false, integers must
// be positive, string lists take one argument per item (or a single JSON-style ["a","b"]).
func (f *File) Set(key string, args []string) error { return f.set(key, args, false) }

// set is Set; fromPlan allows managed keys (SetJSON, the path a setup plan takes).
func (f *File) set(key string, args []string, fromPlan bool) error {
	if strings.HasPrefix(key, nextAccountsPrefix) {
		acct, field, ok := nextAccountKey(key)
		if !ok {
			return fmt.Errorf("usage: next.accounts.<email|seat|folder|label>.<field> <value> (fields: %s)", strings.Join(nextAccountFieldNames(), ", "))
		}
		if len(args) != 1 {
			return fmt.Errorf("%s takes exactly one value", key)
		}
		val, err := parseNextAccountField(key, field, args[0])
		if err != nil {
			return err
		}
		f.table("next", "accounts", acct)[field] = val
		return nil
	}
	if table, rest, ok := slotMapKey(key); ok {
		if rest == "" || len(args) != 1 {
			return fmt.Errorf("usage: slots.%s.<slot> <value>", table)
		}
		if table == "colours" && !colourRe.MatchString(args[0]) {
			return fmt.Errorf("%s: want a colour #rrggbb, got %q", key, args[0])
		}
		if table == "modes" && !ValidSlotMode(args[0]) {
			return fmt.Errorf("%s: want protected or aggressive, got %q", key, args[0])
		}
		if table == "labels" && !ValidLabel(args[0]) {
			return fmt.Errorf("%s: invalid label %q (use a-z, 0-9 and -, at most 24 characters, starting with a letter or digit)", key, args[0])
		}
		f.table("slots", table)[rest] = args[0]
		return nil
	}
	sp, ok := known[key]
	if !ok {
		return fmt.Errorf("unknown key %q (known: %s)", key, strings.Join(Known(), ", "))
	}
	if sp.readOnly {
		return fmt.Errorf("%s is managed by wy and cannot be set", key)
	}
	if sp.managed != "" && !fromPlan {
		return fmt.Errorf("%s is changed by `%s` (it also edits files), not by wy config set", key, sp.managed)
	}
	val, err := parse(key, sp.kind, args)
	if err != nil {
		return err
	}
	parts := strings.Split(key, ".")
	f.table(parts[:len(parts)-1]...)[parts[len(parts)-1]] = val
	return nil
}

// table returns the nested table at path, creating (or replacing a non-table value with) it.
func (f *File) table(path ...string) map[string]any {
	cur := f.doc
	for _, p := range path {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	return cur
}

func parse(key string, k kind, args []string) (any, error) {
	if k == kStrings {
		return parseList(args)
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("%s takes exactly one value", key)
	}
	v := args[0]
	switch k {
	case kBool:
		switch strings.ToLower(v) {
		case "true", "on", "yes", "1":
			return true, nil
		case "false", "off", "no", "0":
			return false, nil
		}
		return nil, fmt.Errorf("%s: want true or false, got %q", key, v)
	case kMCP:
		switch strings.ToLower(v) {
		case "true", "on", "yes":
			return true, nil
		case "false", "off", "no":
			return false, nil
		case "external":
			return "external", nil
		}
		return nil, fmt.Errorf("%s: want true, false or external, got %q", key, v)
	case kInt:
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || fmt.Sprint(n) != v || n <= 0 {
			return nil, fmt.Errorf("%s: want a positive whole number, got %q", key, v)
		}
		return n, nil
	case kFloat:
		return parseFloat(key, v)
	}
	switch key {
	case "next.strategy":
		if !ValidNextStrategy(v) {
			return nil, fmt.Errorf("next.strategy: want one of %s, got %q", strings.Join(NextStrategies, ", "), v)
		}
	case "hub.reach":
		switch v {
		case ReachDirect, ReachForward, ReachPull:
		default:
			return nil, fmt.Errorf("hub.reach: want direct, forward or pull, got %q", v)
		}
	case "hub.dial":
		if err := CheckDial(v); err != nil {
			return nil, err
		}
	case "push.direct.server":
		if err := CheckDirectServer(v); err != nil {
			return nil, err
		}
	case "update.base_url":
		if err := CheckUpdateBase(v); err != nil {
			return nil, err
		}
	case "terminal.source":
		if err := CheckTerminalSource(v); err != nil {
			return nil, err
		}
	case "account.smart_switch_scope":
		if err := CheckSwitchScope(v); err != nil {
			return nil, err
		}
	case "account.move_orgs":
		if v != "any" && v != "same-first" && v != "same" {
			return nil, fmt.Errorf("account.move_orgs: want any, same-first or same, got %q", v)
		}
	}
	if key == "hub.url" && v != "" {
		if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			return nil, fmt.Errorf("hub.url: want an http(s) URL or empty, got %q", v)
		}
	}
	return v, nil
}

func parseList(args []string) (any, error) {
	if len(args) == 1 && strings.HasPrefix(strings.TrimSpace(args[0]), "[") {
		var wrapper struct {
			V []string `toml:"v"`
		}
		if err := toml.Unmarshal([]byte("v = "+args[0]), &wrapper); err != nil {
			return nil, fmt.Errorf("not a list of strings: %w", err)
		}
		args = wrapper.V
	}
	out := make([]any, 0, len(args))
	for _, a := range args {
		if a != "" {
			out = append(out, a)
		}
	}
	return out, nil
}

// Unknown lists the dotted keys in the file that this version does not know, for `wy doctor`.
func (f *File) Unknown() []string {
	var out []string
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			full := prefix + k
			if _, ok := known[full]; ok {
				continue
			}
			if full == "slots.labels" || full == "slots.colours" || full == "slots.modes" {
				continue
			}
			if full == "next.accounts" {
				out = append(out, unknownNextAccountKeys(v)...)
				continue
			}
			if sub, ok := v.(map[string]any); ok && isKnownTable(full) {
				walk(full+".", sub)
				continue
			}
			out = append(out, full)
		}
	}
	walk("", f.doc)
	sort.Strings(out)
	return out
}

func isKnownTable(name string) bool {
	for k := range known {
		if strings.HasPrefix(k, name+".") {
			return true
		}
	}
	return false
}

// Save writes config.toml atomically (temp file in the same folder, fsync, rename), mode
// 0600. It creates the folder (mode 0700) only if it does not exist, and creates no other
// file in it: the folder is shared with other tools (paths.md section 2).
func (f *File) Save() error {
	if _, ok := f.doc["version"]; !ok {
		f.doc["version"] = int64(Version)
	}
	data, err := toml.Marshal(f.doc)
	if err != nil {
		return err
	}
	header := []byte("# Watchyard (wy) settings. Unofficial; not affiliated with Anthropic.\n" +
		"# Change with `wy config set`; comments added by hand are not kept.\n")
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return err
	}
	return atomicfile.Write(f.Path(), append(header, data...), 0o600)
}

var colourRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// labelRe is the account label rule; it must stay identical to model.ValidLabel
// (setup.md step 2). Labels end up in launcher file names and launcher text, so slots.labels.* values
// are checked on every write.
var labelRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)

// CheckSwitchScope validates account.smart_switch_scope: all, tag:<tag> (a peers tag: a-z,
// 0-9 and -, 1 to 40 characters) or sessions:<id>[,<id>...] (session ids or prefixes of at
// least 8 characters).
func CheckSwitchScope(v string) error {
	switch {
	case v == "all":
		return nil
	case strings.HasPrefix(v, "tag:"):
		if t := v[4:]; scopeTagRe.MatchString(t) {
			return nil
		}
	case strings.HasPrefix(v, "sessions:"):
		ok := true
		for _, id := range strings.Split(v[9:], ",") {
			ok = ok && scopeIDRe.MatchString(strings.TrimSpace(id))
		}
		if ok {
			return nil
		}
	}
	return fmt.Errorf("account.smart_switch_scope: want all, tag:<tag> or sessions:<id>,<id>, got %q", v)
}

var (
	scopeTagRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	scopeIDRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{8,80}$`)
)

// ValidLabel reports whether s is a valid account label (`^[a-z0-9][a-z0-9-]{0,23}$`).
func ValidLabel(s string) bool { return labelRe.MatchString(s) }

// SetJSON sets key from a JSON value, the form `wy setup` plans config changes in
// (setup.md 6.1 config_set): a string or bool is passed to Set, an array of strings sets a
// list, and null deletes the key (only the free-form slot keys and share.* may be deleted).
func (f *File) SetJSON(key string, raw json.RawMessage) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	switch t := v.(type) {
	case nil:
		return f.Delete(key)
	case string:
		return f.set(key, []string{t}, true)
	case bool:
		return f.set(key, []string{fmt.Sprint(t)}, true)
	case float64:
		return f.set(key, []string{fmt.Sprint(int64(t))}, true)
	case []any:
		var args []string
		for _, x := range t {
			s, ok := x.(string)
			if !ok {
				return fmt.Errorf("%s: want a list of strings", key)
			}
			args = append(args, s)
		}
		if args == nil {
			args = []string{"[]"}
		}
		return f.set(key, args, true)
	}
	return fmt.Errorf("%s: unsupported value %s", key, raw)
}

// Delete removes a key from the file (a free-form slot key, or a share.* key). A missing
// key is not an error.
func (f *File) Delete(key string) error {
	if table, rest, ok := slotMapKey(key); ok && rest != "" {
		if m, ok := lookupPath(f.doc, []string{"slots", table}).(map[string]any); ok {
			delete(m, rest)
		}
		return nil
	}
	if !strings.HasPrefix(key, "share.") {
		return fmt.Errorf("%s cannot be deleted", key)
	}
	if m, ok := lookupPath(f.doc, []string{"share"}).(map[string]any); ok {
		delete(m, strings.TrimPrefix(key, "share."))
	}
	return nil
}

// CheckDirectServer validates a push.direct.server value: an origin over https, or over http
// only for loopback, private and tailnet addresses (the `wy connect` rule, push.md 7.1). The
// secret topic and the Bearer token go to this server, so a public http URL is refused.
func CheckDirectServer(server string) error {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("push.direct.server: want an origin like https://ntfy.sh, got %q", server)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || tailnetIP(ip)) {
			return nil
		}
		return fmt.Errorf("push.direct.server: http is allowed only for loopback, private and tailnet addresses, got %q", server)
	}
	return fmt.Errorf("push.direct.server: want https, got %q", server)
}

func tailnetIP(ip net.IP) bool {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return cgnat.Contains(ip)
}
