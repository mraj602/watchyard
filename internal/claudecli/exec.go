package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Exec is the real Runner: it runs the `claude` binary with os/exec. Every call sets
// CLAUDE_CONFIG_DIR from the Folder (Folder.Environ), closes stdin (except AuthLogin) and is
// bounded by Timeout (DefaultTimeout when zero). It never reads any file in a config folder.
type Exec struct {
	Path    string        // the claude binary
	Environ []string      // base environment; nil means os.Environ()
	Timeout time.Duration // per call; 0 means DefaultTimeout
	// Dir is the working directory of every call; "" inherits wy's. Claude Code reads
	// <cwd>/.claude/settings.json as project settings, so with cwd = HOME the user's own
	// ~/.claude/settings.json is also the project file and `plugin enable` in another folder
	// answers "already enabled". Setup sets this to a neutral folder (the state dir).
	Dir string
}

var _ Runner = (*Exec)(nil)

// NewExec returns an Exec for the claude binary at path.
func NewExec(path string) *Exec { return &Exec{Path: path} }

// Locate finds the claude binary as setup.md 3 step 1.1 says: `exec.LookPath("claude")`, else
// <home>/.local/bin/claude. It returns ErrNotFound when neither exists.
func Locate(home string) (string, error) {
	return LocateFrom(home)
}

// lookPath is exec.LookPath (tests replace it).
var lookPath = exec.LookPath

// LocateFrom is Locate with more places for a process whose PATH may lack claude's folder (a
// systemd or launchd timer): <home>/.local/bin, <home>/.claude/local (the old local install),
// /usr/local/bin, /opt/homebrew/bin, then hints (e.g. the executable of a running claude
// session), in that order. A hint is taken only when it is an executable file that is not a
// script runtime (node, bun, deno: a session run by node has node as its executable).
func LocateFrom(home string, hints ...string) (string, error) {
	if p, err := lookPath("claude"); err == nil {
		return p, nil
	}
	return Fallback(home, hints...)
}

// Fallback is LocateFrom without PATH: the usual install places, then hints.
func Fallback(home string, hints ...string) (string, error) {
	names := []string{"claude"}
	if runtime.GOOS == "windows" {
		names = []string{"claude.exe", "claude.cmd", "claude"}
	}
	var cands []string
	for _, d := range []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, ".claude", "local")} {
		for _, n := range names {
			cands = append(cands, filepath.Join(d, n))
		}
	}
	if runtime.GOOS != "windows" {
		cands = append(cands, "/usr/local/bin/claude", "/opt/homebrew/bin/claude")
	}
	for _, p := range cands {
		if isExec(p) {
			return p, nil
		}
	}
	for _, h := range hints {
		switch strings.TrimSuffix(strings.ToLower(filepath.Base(h)), ".exe") {
		case "", ".", "node", "nodejs", "bun", "deno":
			continue
		}
		if filepath.IsAbs(h) && isExec(h) {
			return h, nil
		}
	}
	return "", ErrNotFound
}

func isExec(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	return runtime.GOOS == "windows" || fi.Mode()&0o111 != 0
}

func (e *Exec) base() []string {
	if e.Environ != nil {
		return e.Environ
	}
	return os.Environ()
}

func (e *Exec) timeout() time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	return DefaultTimeout
}

// output is what one finished command left.
type output struct {
	stdout, stderr []byte
	exit           int
}

// run starts claude with args. A nil f leaves CLAUDE_CONFIG_DIR as inherited. A non-zero
// exit is reported in output.exit, not as an error; errors are timeouts and start failures.
func (e *Exec) run(ctx context.Context, f *Folder, args ...string) (output, error) {
	env := e.base()
	if f != nil {
		env = f.Environ(env)
	}
	return e.runEnv(ctx, env, args...)
}

// runEnv is run with the child's whole environment given.
func (e *Exec) runEnv(ctx context.Context, env []string, args ...string) (output, error) {
	return e.runIn(ctx, env, e.Dir, args...)
}

// runIn is runEnv with the working directory given.
func (e *Exec) runIn(ctx context.Context, env []string, dir string, args ...string) (output, error) {
	if e.Path == "" {
		return output{}, ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Path, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdin = nil // /dev/null
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	out := output{stdout: so.Bytes(), stderr: se.Bytes()}
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		return out, fmt.Errorf("claude %s: %w", strings.Join(args, " "), ctx.Err())
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		out.exit = ee.ExitCode()
		return out, nil
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return out, ErrNotFound
	}
	return out, fmt.Errorf("claude %s: %w", strings.Join(args, " "), err)
}

// jsonLine returns the JSON document in out: the whole text when it is one document, else the
// last line that is one. Claude may print warnings before the JSON line.
func jsonLine(out []byte) (json.RawMessage, bool) {
	t := bytes.TrimSpace(out)
	if len(t) == 0 {
		return nil, false
	}
	if (t[0] == '{' || t[0] == '[') && json.Valid(t) {
		return json.RawMessage(t), true
	}
	lines := bytes.Split(t, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		l := bytes.TrimSpace(lines[i])
		if len(l) > 0 && (l[0] == '{' || l[0] == '[') && json.Valid(l) {
			return json.RawMessage(l), true
		}
	}
	return nil, false
}

func firstLine(b ...[]byte) string {
	for _, x := range b {
		for _, l := range strings.Split(string(x), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				return l
			}
		}
	}
	return ""
}

// message pulls the human message out of a JSON outcome object.
func message(obj map[string]json.RawMessage) string {
	for _, k := range []string{"message", "error", "msg"} {
		if raw, ok := obj[k]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil && s != "" {
				return s
			}
		}
	}
	return ""
}

func newError(args []string, o output) *Error {
	e := &Error{Args: args, ExitCode: o.exit}
	if raw, ok := jsonLine(o.stdout); ok {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil {
			_ = json.Unmarshal(obj["outcome"], &e.Outcome)
			_ = json.Unmarshal(obj["failureCode"], &e.FailureCode)
			e.Message = message(obj)
		}
	}
	if e.Message == "" {
		e.Message = firstLine(o.stderr, o.stdout)
	}
	if e.Message == "" {
		e.Message = "no output"
	}
	return e
}

// result parses the --json outcome of a plugin command. A non-zero exit, or an outcome other
// than "ok", is an *Error. A zero exit with no outcome field counts as ok.
func result(args []string, o output) (Result, error) {
	raw, ok := jsonLine(o.stdout)
	var res Result
	res.Raw = raw
	if ok {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil {
			_ = json.Unmarshal(obj["outcome"], &res.Outcome)
			_ = json.Unmarshal(obj["command"], &res.Command)
			_ = json.Unmarshal(obj["failureCode"], &res.FailureCode)
			_ = json.Unmarshal(obj["alreadyInGoalState"], &res.AlreadyInGoalState)
			res.Message = message(obj)
		}
	}
	if res.AlreadyInGoalState || res.FailureCode == "already_in_goal_state" {
		// Claude exits 1 for "already enabled" and the like; the goal holds, so it is a success.
		res.AlreadyInGoalState = true
		return res, nil
	}
	if o.exit != 0 {
		return res, newError(args, o)
	}
	if res.Outcome == "" {
		res.Outcome = "ok"
	}
	if !res.OK() {
		return res, newError(args, o)
	}
	return res, nil
}

// Version implements Runner.
func (e *Exec) Version(ctx context.Context) (VersionInfo, error) {
	args := []string{"--version"}
	o, err := e.run(ctx, nil, args...)
	if err != nil {
		return VersionInfo{}, err
	}
	if o.exit != 0 {
		return VersionInfo{}, newError(args, o)
	}
	raw := firstLine(o.stdout)
	v, _, _ := strings.Cut(raw, " ")
	if v == "" {
		return VersionInfo{}, &Error{Args: args, Message: "empty version output"}
	}
	return VersionInfo{Path: e.Path, Version: v, Raw: raw}, nil
}

// AuthStatus implements Runner. A logged-out folder may make claude exit non-zero while still
// printing JSON; the JSON is used whenever it parses. It runs without ANTHROPIC_API_KEY,
// ANTHROPIC_AUTH_TOKEN and CLAUDE_CODE_OAUTH_TOKEN, which would otherwise be reported instead
// of the folder's own login.
func (e *Exec) AuthStatus(ctx context.Context, f Folder) (AuthStatus, error) {
	args := []string{"auth", "status", "--json"}
	o, err := e.runEnv(ctx, f.authEnviron(runtime.GOOS, e.base()), args...)
	if err != nil {
		return AuthStatus{}, err
	}
	raw, ok := jsonLine(o.stdout)
	if !ok {
		if o.exit != 0 {
			return AuthStatus{}, newError(args, o)
		}
		return AuthStatus{}, &Error{Args: args, Message: "unreadable output: " + firstLine(o.stdout, o.stderr)}
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil || probe["loggedIn"] == nil {
		if o.exit != 0 {
			return AuthStatus{}, newError(args, o)
		}
		return AuthStatus{}, &Error{Args: args, Message: "unreadable output: no loggedIn field"}
	}
	var st AuthStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		return AuthStatus{}, &Error{Args: args, ExitCode: o.exit, Message: "unreadable output: " + err.Error()}
	}
	st.Raw = raw
	return st, nil
}

// AuthLogin implements Runner: stdin, stdout and stderr are the terminal's, and there is no
// timeout beyond ctx, because the person is typing.
func (e *Exec) AuthLogin(ctx context.Context, f Folder, tty Stdio) error {
	if e.Path == "" {
		return ErrNotFound
	}
	args := []string{"auth", "login", "--claudeai"}
	cmd := exec.CommandContext(ctx, e.Path, args...)
	cmd.Env = f.authEnviron(runtime.GOOS, e.base()) // the login lands in the folder, as AuthStatus
	cmd.Stdin = tty.In
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	cmd.Stdout, cmd.Stderr = tty.Out, tty.Err
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &Error{Args: args, ExitCode: ee.ExitCode(), Message: "login did not finish"}
		}
		if errors.Is(err, exec.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("claude auth login: %w", err)
	}
	return nil
}

func (e *Exec) plugin(ctx context.Context, f Folder, args ...string) (Result, error) {
	args = append(args, "--json")
	o, err := e.run(ctx, &f, args...)
	if err != nil {
		return Result{}, err
	}
	return result(args, o)
}

// PluginMarketplaceAdd implements Runner.
func (e *Exec) PluginMarketplaceAdd(ctx context.Context, f Folder, source string) (Result, error) {
	return e.plugin(ctx, f, "plugin", "marketplace", "add", source)
}

// PluginMarketplaceRemove implements Runner. Removing a marketplace that is not configured
// (failureCode "not_configured") counts as done.
func (e *Exec) PluginMarketplaceRemove(ctx context.Context, f Folder, name string) (Result, error) {
	res, err := e.plugin(ctx, f, "plugin", "marketplace", "remove", name)
	return absentIsDone(res, err, "not_configured")
}

// absentIsDone turns a failure with the given failureCode into an already-in-goal-state result.
func absentIsDone(res Result, err error, code string) (Result, error) {
	var ce *Error
	if errors.As(err, &ce) && ce.FailureCode == code {
		res.AlreadyInGoalState, res.FailureCode, res.Message = true, code, ce.Message
		return res, nil
	}
	return res, err
}

// PluginMarketplaceUpdate implements Runner.
func (e *Exec) PluginMarketplaceUpdate(ctx context.Context, f Folder, name string) (Result, error) {
	return e.plugin(ctx, f, "plugin", "marketplace", "update", name)
}

// PluginInstall implements Runner.
func (e *Exec) PluginInstall(ctx context.Context, f Folder, id string) (Result, error) {
	return e.plugin(ctx, f, "plugin", "install", id)
}

// PluginUpdate implements Runner.
func (e *Exec) PluginUpdate(ctx context.Context, f Folder, id string) (Result, error) {
	return e.plugin(ctx, f, "plugin", "update", id)
}

// PluginUninstall implements Runner. A plugin that is not installed (failureCode
// "not_installed") counts as done.
func (e *Exec) PluginUninstall(ctx context.Context, f Folder, id string) (Result, error) {
	res, err := e.plugin(ctx, f, "plugin", "uninstall", id)
	return absentIsDone(res, err, "not_installed")
}

// PluginUninstallScoped implements Runner. Installs in the project and local scopes are only
// found from their project folder, so the command runs there.
func (e *Exec) PluginUninstallScoped(ctx context.Context, f Folder, id, scope, projectPath string) (Result, error) {
	if scope == "" || scope == "user" {
		return e.PluginUninstall(ctx, f, id)
	}
	args := []string{"plugin", "uninstall", id, "--scope=" + scope, "--json"}
	dir := e.Dir
	if projectPath != "" {
		dir = projectPath
	}
	o, err := e.runIn(ctx, f.Environ(e.base()), dir, args...)
	if err != nil {
		return Result{}, err
	}
	res, err := result(args, o)
	return absentIsDone(res, err, "not_installed")
}

// PluginEnable implements Runner.
func (e *Exec) PluginEnable(ctx context.Context, f Folder, id string) (Result, error) {
	return e.plugin(ctx, f, "plugin", "enable", id)
}

// PluginDisable implements Runner.
func (e *Exec) PluginDisable(ctx context.Context, f Folder, id string) (Result, error) {
	return e.plugin(ctx, f, "plugin", "disable", id)
}

const rawKey = "\x00raw"

// rows runs a --json list command and returns its rows as generic objects. The document may
// be an array or an object wrapping one array ({"plugins": [...]}).
func (e *Exec) rows(ctx context.Context, f Folder, args ...string) ([]map[string]json.RawMessage, error) {
	args = append(args, "--json")
	o, err := e.run(ctx, &f, args...)
	if err != nil {
		return nil, err
	}
	if o.exit != 0 {
		return nil, newError(args, o)
	}
	raw, ok := jsonLine(o.stdout)
	if !ok {
		if len(bytes.TrimSpace(o.stdout)) == 0 {
			return nil, nil
		}
		return nil, &Error{Args: args, Message: "unreadable output: " + firstLine(o.stdout)}
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return nil, &Error{Args: args, Message: "unreadable output"}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		arr = nil
		for _, k := range keys {
			var a []json.RawMessage
			if json.Unmarshal(obj[k], &a) == nil {
				arr = a
				break
			}
		}
	}
	var out []map[string]json.RawMessage
	for _, r := range arr {
		var m map[string]json.RawMessage
		if json.Unmarshal(r, &m) != nil {
			continue
		}
		m[rawKey] = r
		out = append(out, m)
	}
	return out, nil
}

func str(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		var s string
		if json.Unmarshal(m[k], &s) == nil && s != "" {
			return s
		}
	}
	return ""
}

// PluginList implements Runner.
func (e *Exec) PluginList(ctx context.Context, f Folder) ([]PluginInfo, error) {
	rows, err := e.rows(ctx, f, "plugin", "list")
	if err != nil {
		return nil, err
	}
	var out []PluginInfo
	for _, m := range rows {
		p := PluginInfo{
			ID:          str(m, "id", "plugin"),
			Version:     str(m, "version"),
			Scope:       str(m, "scope"),
			ProjectPath: str(m, "projectPath", "project_path"),
			InstallPath: str(m, "installPath", "install_path"),
		}
		if p.ID == "" {
			if n := str(m, "name"); n != "" {
				p.ID = n
				if mk := str(m, "marketplace"); mk != "" {
					p.ID = n + "@" + mk
				}
			}
		}
		_ = json.Unmarshal(m["enabled"], &p.Enabled)
		out = append(out, p)
	}
	return out, nil
}

// PluginMarketplaceList implements Runner.
func (e *Exec) PluginMarketplaceList(ctx context.Context, f Folder) ([]MarketplaceInfo, error) {
	rows, err := e.rows(ctx, f, "plugin", "marketplace", "list")
	if err != nil {
		return nil, err
	}
	var out []MarketplaceInfo
	for _, m := range rows {
		// 2.1.296 prints {"name","source":"directory","path":"<dir>","installLocation":"<dir>"}
		// (a flat row); older shapes nest {"source":{"source":"directory","path":"..."}}.
		mi := MarketplaceInfo{Name: str(m, "name", "id"), Source: str(m, "path")}
		if mi.Source == "" {
			mi.Source = str(m, "source")
			if mi.Source == "directory" || mi.Source == "github" || mi.Source == "git" || mi.Source == "url" {
				mi.Source = ""
			}
		}
		if mi.Source == "" {
			// "source": {"source": "directory", "path": "..."}
			var obj map[string]json.RawMessage
			if json.Unmarshal(m["source"], &obj) == nil {
				mi.Source = str(obj, "path", "repo", "url")
			}
		}
		if mi.Source == "" {
			mi.Source = str(m, "path", "installLocation")
		}
		out = append(out, mi)
	}
	return out, nil
}

// Agents implements Runner.
func (e *Exec) Agents(ctx context.Context, f Folder) ([]Agent, error) {
	rows, err := e.rows(ctx, f, "agents")
	if err != nil {
		return nil, err
	}
	var out []Agent
	for _, m := range rows {
		out = append(out, Agent{
			ID:     str(m, "id", "sessionId", "session_id"),
			Status: str(m, "status", "state"),
			Raw:    m[rawKey],
		})
	}
	return out, nil
}
