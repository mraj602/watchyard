package model

// State is what detect (setup.md 3 step 1) found: the planner's whole input besides Choices.
// It is also the `state` object of `wy setup --detect --json` (6.3). Paths named "dir",
// "target" in absolute form are real paths; every field called Folder/Name is the "~/..."
// form used on screen and on the wire.
type State struct {
	Claude  ClaudeInfo `json:"claude"`
	Folders []Folder   `json:"folders"`
	Hub     HubState   `json:"hub"`

	// Everything below is omitted from the 6.3 example but is part of the detect output.

	Home string `json:"home,omitempty"` // absolute home folder
	OS   string `json:"os,omitempty"`   // runtime.GOOS
	// Wy is the absolute path of the wy binary that will be baked into hooks and the status line.
	Wy string `json:"wy,omitempty"`
	// StateDir is <state> (paths.md); nothing is created there before apply.
	StateDir string `json:"state_dir,omitempty"`
	// ConfigDir is the folder holding config.toml (paths.md); the planner derives its path from it.
	ConfigDir string `json:"config_dir,omitempty"`
	// SymlinkOK is false on Windows without Developer Mode: file items become synced copies (5.7).
	SymlinkOK *bool `json:"symlink_ok,omitempty"`

	SettingsTargets []SettingsTarget `json:"settings_targets,omitempty"`
	PluginRoots     []PluginRoot     `json:"plugin_roots,omitempty"`
	Launchers       []Launcher       `json:"launchers,omitempty"` // every claude-* found (step 5)
	MCP             MCPState         `json:"mcp,omitzero"`
	// Links is the link-guard baseline (7.5): every symlink/junction found at detect time.
	Links []LinkRecord `json:"links,omitempty"`
	// Watchyard facts (step 7).
	ConfigPresent  bool       `json:"config_present,omitempty"`
	Ledger         *Ledger    `json:"ledger,omitempty"`
	IncompleteSets []string   `json:"incomplete_sets,omitempty"` // backup ids still "in-progress"
	Tailscale      *Tailscale `json:"tailscale,omitempty"`
	// Warnings are detect-time notes (old Claude Code, a probe that timed out, ...).
	Warnings []string `json:"warnings,omitempty"`
	// HomeNames are the names of every entry directly in the home folder that starts with
	// ".claude" (files and folders): the planner needs them to pick the next free folder (5.6).
	HomeNames []string `json:"home_names,omitempty"`
	// Share is what config.toml already says about sharing (empty when unset).
	Share ShareConfig `json:"share,omitzero"`
}

// ShareConfig is the `[share]` table of config.toml as read at detect time.
type ShareConfig struct {
	Primary string `json:"primary,omitempty"` // "~/.claude"
	MCP     string `json:"mcp,omitempty"`     // "true", "false" or "external"
}

// ClaudeInfo is the `claude` binary found at step 1.1.
type ClaudeInfo struct {
	Path    string `json:"path"`    // "~/.local/bin/claude"; empty when not found
	Version string `json:"version"` // "2.1.296"
}

// Folder is one Claude Code config folder (a slot) and its items.
type Folder struct {
	Folder   string `json:"folder"` // "~/.claude-2"
	LoggedIn bool   `json:"logged_in"`
	Email    string `json:"email,omitempty"`
	Org      string `json:"org,omitempty"`
	Plan     string `json:"plan,omitempty"`
	Label    string `json:"label,omitempty"`
	Colour   string `json:"colour,omitempty"`
	// Mode is config slots.modes for the folder (setup.md 8.5, Kind); not in --detect output.
	Mode     string          `json:"-"`
	Launcher *FolderLauncher `json:"launcher,omitempty"`
	// Items is keyed by item name (ShareItems[i].Name).
	Items map[string]ItemState `json:"items,omitempty"`

	Dir            string            `json:"dir,omitempty"`             // absolute, symlinks not resolved
	StateFile      string            `json:"state_file,omitempty"`      // the .claude.json holding oauthAccount
	Primary        bool              `json:"primary,omitempty"`         // this is the primary folder
	Default        bool              `json:"default,omitempty"`         // ~/.claude with CLAUDE_CONFIG_DIR unset
	New            bool              `json:"new,omitempty"`             // planned in this run, does not exist yet
	SettingsTarget string            `json:"settings_target,omitempty"` // resolved settings.json (may not exist)
	PluginRoot     string            `json:"plugin_root,omitempty"`     // resolved plugin root
	OwnsRoot       bool              `json:"owns_root,omitempty"`       // its own plugins/ is the root (5.2 step 2)
	MCPNames       map[string]string `json:"mcp_names,omitempty"`       // mcpServers name -> SHA-256 of its value (never values)
	AuthError      string            `json:"auth_error,omitempty"`      // `claude auth status` failed or timed out
}

// FolderLauncher is the launcher of one folder as shown in the 6.3 example
// (`"launcher": {"name": "claude-2", "kind": "shell-function", "ours": false}`).
type FolderLauncher struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // LauncherKind*
	Ours bool   `json:"ours"`
}

// Launcher kinds (setup.md 3 step 1.5).
const (
	LauncherFile     = "file"           // executable on PATH or in ~/.local/bin
	LauncherFunction = "shell-function" // found by the probe or the rc scan
	LauncherAlias    = "alias"
	LauncherScript   = "script" // an rc-called script such as ~/bin/claude-launch
)

// Launcher is one launcher found on the machine (not tied to a folder in this list).
type Launcher struct {
	Name   string `json:"name"` // "claude-2"
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`   // file or script path, "~" form
	Ours   bool   `json:"ours,omitempty"`   // has the "# wy-launcher v1" marker
	Hash   string `json:"sha256,omitempty"` // of the file, for "still ours"
	Folder string `json:"folder,omitempty"` // folder it launches, "~" form, when known
	// MentionsMCP is set when the launcher or a script it calls mentions mcpServers (hand-made sync).
	MentionsMCP bool `json:"mentions_mcp,omitempty"`
}

// ItemState state values. Detect fills the raw ones; Compare is filled when both sides exist.
const (
	ItemAbsent    = "absent"    // nothing at the path
	ItemLinked    = "linked"    // link resolving to the primary's item
	ItemElsewhere = "elsewhere" // link resolving somewhere else ("shared elsewhere")
	ItemFile      = "file"      // regular file
	ItemDir       = "dir"       // regular directory
	ItemCopy      = "copy"      // Windows synced copy (copies.json)
	ItemOther     = "other"     // anything else (socket, dangling link)
)

// Compare values for ItemState.Compare (5.3 table).
const (
	CompareSame    = "same"    // identical to the primary's item
	CompareDiffers = "differs" // conflict
	CompareUnknown = "unknown" // directories are compared lazily; not yet compared
)

// ItemState is one item in one folder (step 1.3).
type ItemState struct {
	State  string `json:"state"`            // Item* constant
	Target string `json:"target,omitempty"` // for links: resolved target, "~" form
	// LinkRaw is the raw readlink string (relative links stay relative); the link guard and
	// undo compare this, never Target.
	LinkRaw string `json:"link_raw,omitempty"`
	Size    int64  `json:"size,omitempty"`
	SHA256  string `json:"sha256,omitempty"` // files under 1 MiB only; never directories
	Compare string `json:"compare,omitempty"`
	// Diff is set by detect for a regular directory that differs from the reference and has a
	// "dir" merge (5.3): which entries a merge would move and which stay conflicts.
	Diff *DirDiff `json:"diff,omitempty"`
}

// DirDiff lists, for a differing directory F/X against the reference P/X, the entries of F
// that P lacks (a merge moves them into P) and the entries present in both that differ (they
// stay a conflict and F/X is then not linked). Paths are relative to X, "/" separated.
type DirDiff struct {
	Missing []string `json:"missing,omitempty"`
	Differ  []string `json:"differ,omitempty"`
}

// HubState is the hub as seen from this machine (step 1.7).
type HubState struct {
	URL     *string `json:"url"` // null when not connected
	Healthy bool    `json:"healthy"`
	// Optional detail.
	TokenPresent     bool `json:"token_present,omitempty"`
	TimerActive      bool `json:"timer_active,omitempty"`
	ServiceInstalled bool `json:"service_installed,omitempty"`
	ServiceActive    bool `json:"service_active,omitempty"`
	LingerOff        bool `json:"linger_off,omitempty"` // Linux: `loginctl show-user -p Linger` is "no"
	// hub.reach when it is not "direct", and hub.dial.
	Reach string `json:"reach,omitempty"`
	Dial  string `json:"dial,omitempty"`
}

// Tailscale is `tailscale status --json` reduced to what step 4 offers.
type Tailscale struct {
	Up      bool   `json:"up"`
	DNSName string `json:"dns_name,omitempty"`
}

// SettingsTarget is one real settings.json after following links (shared by several folders).
type SettingsTarget struct {
	Path              string          `json:"path"`    // resolved, absolute
	Folders           []string        `json:"folders"` // folders using it, "~" form
	Exists            bool            `json:"exists"`
	EnabledPlugins    map[string]bool `json:"enabled_plugins,omitempty"`
	ExtraMarketplaces []string        `json:"extra_marketplaces,omitempty"` // names
	EnvKeys           []string        `json:"env_keys,omitempty"`           // keys only, never values
	// PluginCacheDir is the value of env.CLAUDE_CODE_PLUGIN_CACHE_DIR when set (a path, not a secret).
	PluginCacheDir string      `json:"plugin_cache_dir,omitempty"`
	StatusLine     *StatusLine `json:"status_line,omitempty"`
	// WatchyardPlugins are every enabled watchyard@* (and watchyard@skills-dir) id.
	WatchyardPlugins []string `json:"watchyard_plugins,omitempty"`
}

// StatusLine is the settings statusLine object, reduced.
type StatusLine struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command,omitempty"`
	Ours    bool   `json:"ours,omitempty"`
}

// PluginRoot is one distinct plugin root (5.2).
type PluginRoot struct {
	Root    string   `json:"root"`              // resolved absolute path
	Owner   string   `json:"owner,omitempty"`   // folder whose own plugins/ is the root, "~" form
	Folders []string `json:"folders"`           // folders that use it
	Plugins []string `json:"plugins,omitempty"` // installed ids "name@marketplace"
	Markets []string `json:"markets,omitempty"` // known marketplace names
	// Sources maps a marketplace name to its source (a directory path, repo or URL), from
	// known_marketplaces.json; the planner copies a folder's own plugins into the shared root with it.
	Sources map[string]string `json:"sources,omitempty"`
	FromEnv bool              `json:"from_env,omitempty"` // chosen by CLAUDE_CODE_PLUGIN_CACHE_DIR (process or settings)
	Adopted bool              `json:"adopted,omitempty"`  // reached through a link to the primary's plugins/
}

// MCPState is hand-made MCP sync detection (step 1.6).
type MCPState struct {
	HandMade bool   `json:"hand_made,omitempty"`
	Script   string `json:"script,omitempty"` // "~/.claude/sync-mcp-to-2.sh"
}

// LinkRecord is one link in the link-guard baseline (7.5).
type LinkRecord struct {
	Path string `json:"path"` // absolute
	Raw  string `json:"raw"`  // raw readlink string
	Kind string `json:"kind,omitempty"`
}
