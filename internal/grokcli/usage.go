package grokcli

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// SessionUsage is the part of `grok usage <session>` Watchyard shows.
type SessionUsage struct {
	SessionID    string
	Model        string
	Turns        int
	Calls        int
	InputTokens  int
	OutputTokens int
	CachedTokens int
	TotalTokens  int
	// CostTicks is grok's costUsdTicks; ticks are 1e-10 USD.
	CostTicks int64
}

// CostUSD converts ticks to dollars (an estimate: grok's own accounting).
func (u SessionUsage) CostUSD() float64 { return float64(u.CostTicks) / 1e10 }

// UsageOf runs `grok usage <id>` (per-session, works without Grok's background service).
func UsageOf(ctx context.Context, bin string, env []string, id string) (SessionUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, bin, "usage", id)
	if env != nil {
		c.Env = env
	}
	b, err := c.Output()
	if err != nil {
		return SessionUsage{}, errors.New("grok usage " + id + " failed")
	}
	var v struct {
		SessionID string `json:"sessionId"`
		Session   struct {
			In     int    `json:"inputTokens"`
			Out    int    `json:"outputTokens"`
			Cached int    `json:"cachedReadTokens"`
			Total  int    `json:"totalTokens"`
			Calls  int    `json:"modelCalls"`
			Ticks  int64  `json:"costUsdTicks"`
			Turns  int    `json:"turnCount"`
			Model  string `json:"primaryModelId"`
		} `json:"session"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return SessionUsage{}, errors.New("grok usage printed something unexpected")
	}
	s := v.Session
	return SessionUsage{SessionID: v.SessionID, Model: s.Model, Turns: s.Turns, Calls: s.Calls, InputTokens: s.In,
		OutputTokens: s.Out, CachedTokens: s.Cached, TotalTokens: s.Total, CostTicks: s.Ticks}, nil
}

// PlanUsage runs `grok usage --json` and returns its compact text. It needs Grok's background
// service; without it grok says so and the error carries that one line.
func PlanUsage(ctx context.Context, bin string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, bin, "usage", "--json")
	if env != nil {
		c.Env = env
	}
	b, err := c.CombinedOutput()
	out := strings.TrimSpace(string(b))
	if err != nil {
		line, _, _ := strings.Cut(strings.TrimPrefix(out, "Error: "), "\n")
		if line == "" {
			line = "grok usage failed"
		}
		return "", errors.New(line)
	}
	var buf strings.Builder
	var raw any
	if json.Unmarshal(b, &raw) == nil {
		if cb, e := json.Marshal(raw); e == nil {
			buf.Write(cb)
			return buf.String(), nil
		}
	}
	return out, nil
}
