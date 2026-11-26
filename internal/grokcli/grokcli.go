// Package grokcli runs xAI's Grok Build CLI (`grok`) headlessly so a Claude Code session, or
// any orchestrator, can hand a task to Grok and read the result (docs/grok.md).
//
// It only shells out to the `grok` binary: it never reads ~/.grok (auth.json holds the login),
// never sets or logs XAI_API_KEY, and keeps the prompt out of every error message.
package grokcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds one headless run when Options.Timeout is zero.
const DefaultTimeout = 30 * time.Minute

var (
	// ErrNotFound means no grok binary was found.
	ErrNotFound = errors.New("grok binary not found (install: curl -fsSL https://x.ai/cli/install.sh | bash)")
	// ErrNotAuthenticated means grok is installed but not signed in.
	ErrNotAuthenticated = errors.New("grok is not signed in (run: grok login --device-auth)")
)

// Locate finds the grok binary: LookPath("grok"), else ~/.local/bin/grok, else ~/.grok/bin/grok.
func Locate(home string) (string, error) {
	if p, err := exec.LookPath("grok"); err == nil {
		return p, nil
	}
	for _, rel := range []string{".local/bin/grok", ".grok/bin/grok"} {
		p := filepath.Join(home, filepath.FromSlash(rel))
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", ErrNotFound
}

// Options is one headless run.
type Options struct {
	Prompt    string
	Dir       string        // working directory of the run; "" inherits
	Model     string        // grok -m; "" uses grok's default
	Effort    string        // grok --effort; "" uses the default
	MaxTurns  int           // grok --max-turns; 0 means unlimited
	Yolo      bool          // grok --always-approve: tools run without prompting
	SessionID string        // grok -r: resume this session
	Timeout   time.Duration // 0 means DefaultTimeout
}

// Result is the parsed `--output-format json` object.
type Result struct {
	Text       string  `json:"text"`
	StopReason string  `json:"stopReason"`
	SessionID  string  `json:"sessionId"`
	NumTurns   int     `json:"num_turns"`
	CostUSD    float64 `json:"total_cost_usd"`
	Tokens     int     `json:"-"`
	Usage      struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// Args builds the argument list for opts (exported for tests and `wy grok run --dry-run`).
func Args(o Options) []string {
	a := []string{"--output-format", "json", "--no-auto-update"}
	if o.Model != "" {
		a = append(a, "-m", o.Model)
	}
	if o.Effort != "" {
		a = append(a, "--effort", o.Effort)
	}
	if o.MaxTurns > 0 {
		a = append(a, "--max-turns", strconv.Itoa(o.MaxTurns))
	}
	if o.Yolo {
		a = append(a, "--always-approve")
	}
	if o.SessionID != "" {
		a = append(a, "-r", o.SessionID)
	}
	if o.Dir != "" {
		a = append(a, "--cwd", o.Dir)
	}
	// --prompt-file would avoid argv exposure, but -p keeps the call identical to the docs;
	// the prompt is never logged by this package.
	return append(a, "-p", o.Prompt)
}

// Run runs grok headlessly and parses its JSON answer. A non-zero exit with a JSON error
// object yields that message; an auth failure yields ErrNotAuthenticated.
func Run(ctx context.Context, bin string, env []string, o Options) (*Result, error) {
	if strings.TrimSpace(o.Prompt) == "" {
		return nil, errors.New("empty prompt")
	}
	to := o.Timeout
	if to <= 0 {
		to = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, Args(o)...)
	cmd.Dir = o.Dir
	if env != nil {
		cmd.Env = env
	}
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("grok timed out after %s", to)
	}
	return parse(stdout.Bytes(), stderr.String(), err)
}

func parse(out []byte, errText string, runErr error) (*Result, error) {
	out = bytes.TrimSpace(out)
	var probe struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if len(out) > 0 && json.Unmarshal(out, &probe) == nil && probe.Type == "error" {
		return nil, classify(probe.Message)
	}
	if runErr != nil {
		msg := strings.TrimSpace(errText)
		if len(msg) > 400 {
			msg = msg[:400]
		}
		if e := classify(msg); errors.Is(e, ErrNotAuthenticated) {
			return nil, e
		}
		return nil, fmt.Errorf("grok failed: %v: %s", runErr, msg)
	}
	var r Result
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("grok printed no JSON result: %w", err)
	}
	r.Tokens = r.Usage.TotalTokens
	return &r, nil
}

func classify(msg string) error {
	l := strings.ToLower(msg)
	if strings.Contains(l, "not authenticated") || strings.Contains(l, "sign in") || strings.Contains(l, "unauthorized") || strings.Contains(l, "login") {
		return ErrNotAuthenticated
	}
	return errors.New("grok: " + msg)
}

// Status is what `wy grok status` reports.
type Status struct {
	Path          string
	Version       string
	Authenticated bool
}

// Check reports the binary, its version and whether it is signed in. Sign-in is read from the
// words `grok models` prints; the credential file is never opened.
func Check(ctx context.Context, bin string, env []string) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	st := Status{Path: bin}
	run := func(args ...string) string {
		c := exec.CommandContext(ctx, bin, args...)
		if env != nil {
			c.Env = env
		}
		b, _ := c.CombinedOutput()
		return string(b)
	}
	st.Version = strings.TrimSpace(run("--version"))
	if st.Version == "" {
		return st, errors.New("grok --version printed nothing")
	}
	st.Authenticated = !strings.Contains(strings.ToLower(run("models")), "not authenticated")
	return st, nil
}
