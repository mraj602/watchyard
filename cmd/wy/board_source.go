package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/mraj602/watchyard/internal/board"
	"github.com/mraj602/watchyard/internal/board/hubsrc"
	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/warp"
)

// Source selection of `wy board` (docs/spec/terminal.md T3):
//
//   - auto (default): the hub source when hub.url is set and <state>/board/token.json holds a
//     token for that URL; it merges this machine's local fold in while the hub is out of
//     reach. Otherwise the local fold, with hub.state none (no hub.url) or unpaired (no
//     token), and the footer says to run `wy board connect`.
//   - hub: the hub source only when it is usable: no hub.url or no token is errBoardNoHub
//     (exit 4), a hub that does not answer is errBoardHubDown (exit 5).
//   - local: the local fold, whatever the hub.
//
// Both sources deliver ended and gone sessions of the last 2 days (board.EndedWithin), so
// the TUI's `e` toggle has something to show; the TUI hides them until then. The Warp
// colour of a seat comes from internal/warp, the table the tab configs use.

// boardProbe checks that the hub answers the board token (`--source hub`): nil, or an error
// wrapping errBoardHubDown (unreachable) or errBoardNoHub (token refused). A test seam.
var boardProbe = probeBoardHub

func selectBoardSource(sel boardSelection) (board.Source, error) {
	cfg := sel.Config
	m, err := identity.LoadOrCreateMachine(sel.StateDir, identity.MachineOptions{})
	if err != nil {
		return nil, err
	}
	machine := board.Machine{ID: m.ID, Name: m.Name}
	hubURL := cfg.Hub.URL
	local := func(state string) board.Source {
		return board.NewLocalSource(board.LocalOptions{
			Options: board.Options{
				Machine: machine, SessionID: sel.Session, IncludeEnded: true,
				Hub: board.HubInfo{State: state, URL: hubURL}, WarpColour: warp.Nearest, Drains: boardDrains(sel.ConfigPath), Offs: boardOffs(sel.ConfigPath),
			},
			Env:       sel.Env,
			OutboxDir: filepath.Join(sel.StateDir, "outbox"),
			Labels:    cfg.Slots.Labels,
			Colours:   cfg.Slots.Colours,
		})
	}
	if sel.Mode == config.TerminalSourceLocal {
		st := board.HubNone
		if hubURL != "" {
			if _, err := board.LoadToken(sel.StateDir, hubURL); err != nil {
				st = board.HubUnpaired
			}
		}
		return local(st), nil
	}
	if hubURL == "" {
		if sel.Mode == config.TerminalSourceHub {
			return nil, fmt.Errorf("%w (hub.url is not set)", errBoardNoHub)
		}
		return local(board.HubNone), nil
	}
	tok, err := board.LoadToken(sel.StateDir, hubURL)
	if err != nil {
		if !errors.Is(err, board.ErrNoToken) {
			return nil, err
		}
		if sel.Mode == config.TerminalSourceHub {
			return nil, errBoardNoHub
		}
		return local(board.HubUnpaired), nil
	}
	// http: is fine for what `wy connect` accepts (loopback, Tailscale, hub.insecure_lan).
	// Anything else must not see the token: not even in the --source hub probe.
	_, _, uerr := client.CheckURL(hubURL, cfg.Hub.InsecureLAN)
	if uerr != nil {
		if sel.Mode == config.TerminalSourceHub {
			return nil, fmt.Errorf("%w (%v)", errBoardNoHub, uerr)
		}
		return local(board.HubOffline), nil
	}
	tr, err := client.DialTransport(cfg.Hub.Dial)
	if err != nil {
		return nil, fmt.Errorf("hub.dial: %w", err)
	}
	if sel.Mode == config.TerminalSourceHub {
		ctx, cancel := context.WithTimeout(context.Background(), hubsrc.FetchTimeout)
		perr := boardProbe(ctx, hubURL, tok.Token, tr, sel.StateDir, true)
		cancel()
		if perr != nil {
			return nil, perr
		}
	}
	state := sel.StateDir
	src, err := hubsrc.New(hubsrc.Options{
		URL: hubURL, Token: tok.Token, AllowHTTP: true, Transport: tr,
		Machine: machine, ThisSession: sel.Session, StateDir: state,
		Local:        local(board.HubLive),
		OnUnpaired:   func() { _, _ = board.RemoveTokenIf(state, tok.Token) },
		IncludeEnded: true, WarpColour: warp.Nearest, Drains: boardDrains(sel.ConfigPath), Offs: boardOffs(sel.ConfigPath),
	})
	if err != nil {
		return nil, err
	}
	return src, nil
}

// probeBoardHub is one GET /v1/overview with the board token, no redirects.
func probeBoardHub(ctx context.Context, hubURL, token string, tr http.RoundTripper, stateDir string, allowHTTP bool) error {
	u, err := hubsrc.CheckURL(hubURL, allowHTTP)
	if err != nil {
		return fmt.Errorf("%w: %v", errBoardNoHub, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String()+"/v1/overview", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "wy-board")
	hc := &http.Client{Transport: tr, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w (%s)", errBoardHubDown, u.Host)
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		_, _ = board.RemoveTokenIf(stateDir, token)
		return fmt.Errorf("%w (the hub refused the board token)", errBoardNoHub)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%w (%s answered %d)", errBoardHubDown, u.Host, resp.StatusCode)
	}
	return nil
}

// boardDrains reads the owner's drains (next.accounts.<label>.drain_until) at each board
// build: until when, by account label.
// boardOffs reads this machine's account modes for the board's Off note, at each build.
func boardOffs(configPath string) func() map[string]bool {
	if configPath == "" {
		return nil
	}
	return func() map[string]bool {
		f, err := config.Load(configPath)
		if err != nil {
			return nil
		}
		c, err := f.Typed()
		if err != nil {
			return nil
		}
		out := map[string]bool{}
		for k, a := range c.Next.Accounts {
			if a.Mode != "" {
				out[k] = a.Mode == config.NextModeOff
			}
		}
		return out
	}
}

func boardDrains(configPath string) func() map[string]time.Time {
	if configPath == "" {
		return nil
	}
	return func() map[string]time.Time {
		f, err := config.Load(configPath)
		if err != nil {
			return nil
		}
		c, err := f.Typed()
		if err != nil {
			return nil
		}
		out := map[string]time.Time{}
		for k, a := range c.Next.Accounts {
			if t, err := time.Parse(time.RFC3339, a.DrainUntil); err == nil && a.DrainUntil != "" {
				out[k] = t
			}
		}
		return out
	}
}
