// Package hubsrc is the board's hub source (docs/spec/terminal.md T3): it reads the hub's
// read API and its event stream with the board token and produces board.Views, merging the
// local source in when the hub is out of reach. It only reads: nothing here posts to the
// hub, and the token is used for the Authorization header and nowhere else (not in a URL, a
// log line or an error).
package hubsrc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mraj602/watchyard/internal/board"
)

// Defaults of T3.
const (
	FetchTimeout  = 10 * time.Second
	PingTimeout   = 60 * time.Second
	MaxReply      = 4 << 20
	SessionsLimit = 300
	// MaxSessionPages and MaxSessions bound what one fetch (and the folded state) holds.
	MaxSessionPages = 10
	MaxSessions     = 2000
	saveLastViewAt  = 2 * time.Second
)

// Options configures a Source.
type Options struct {
	// URL is the hub URL (hub.url) and Token the board token issued for it. Both required.
	URL   string
	Token string
	// AllowHTTP lets a plain http: URL through for a non-loopback host; the caller sets it
	// only for a URL `wy` already accepted (hub.insecure_lan, or Tailscale). Loopback hosts
	// never need it.
	AllowHTTP bool
	// Transport carries the requests; the command layer passes client.DialTransport(hub.dial)
	// so a forward-mode machine dials as `wy flush` does. nil is a plain transport.
	Transport http.RoundTripper
	// Machine is this machine; ThisSession marks one row (wy board --session).
	Machine     board.Machine
	ThisSession string
	// StateDir holds board/last-view.json. Empty: nothing is saved or loaded.
	StateDir string
	// Local is the local source, merged in while the hub is offline or unpaired. May be nil.
	Local board.Source
	// OnUnpaired is called once when the hub answers 401 (the command layer deletes the
	// token file). May be nil.
	OnUnpaired func()
	// Build replaces DefaultBuild (which hands the reads to board.BuildHub).
	Build BuildFunc
	// IncludeEnded fetches ended and gone sessions too (include_ended=true) and keeps them
	// for board.EndedWithin, so the TUI's `e` toggle has rows to show.
	IncludeEnded bool
	// WarpColour maps a seat colour to Warp's colour name; nil is board's built-in table.
	// wy passes internal/warp's, the table the tab configs use.
	WarpColour func(hex string) string
	// Drains and Offs: board.Options.Drains and Offs.
	Drains func() map[string]time.Time
	Offs   func() map[string]bool
	// Loc is the zone of the reset times shown; nil is time.Local.
	Loc *time.Location
	// Now, Backoff, PingTimeout and FetchTimeout are test seams (zero: the T3 values).
	Now          func() time.Time
	Backoff      func(failures int) time.Duration
	PingTimeout  time.Duration
	FetchTimeout time.Duration
	// Logf receives one-line notes; it never sees a token. nil: none.
	Logf func(format string, args ...any)
}

// Source is the hub board.Source.
type Source struct {
	o       Options
	base    *url.URL
	fetchHC *http.Client
	streamC *http.Client

	once    sync.Once
	refresh chan struct{} // Refresh -> merger (cap 1)
	refetch chan struct{} // merger -> hub loop (cap 1)
	kick    chan struct{} // a usage event: refetch accounts (cap 1)
	msgs    chan func(*state)
}

var (
	errUnauthorized = errors.New("hubsrc: the hub refused the board token (401)")
	errReset        = errors.New("hubsrc: stream reset")
	errRefetch      = errors.New("hubsrc: refetch requested")
	errPingTimeout  = errors.New("hubsrc: no ping from the hub")
	errClosed       = errors.New("hubsrc: stream closed by the hub")
)

// New checks the options and returns a Source. It makes no request.
func New(o Options) (*Source, error) {
	if o.Token == "" {
		return nil, errors.New("hubsrc: no token")
	}
	u, err := CheckURL(o.URL, o.AllowHTTP)
	if err != nil {
		return nil, err
	}
	if o.Build == nil {
		o.Build = DefaultBuild
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Backoff == nil {
		o.Backoff = defaultBackoff
	}
	if o.PingTimeout <= 0 {
		o.PingTimeout = PingTimeout
	}
	if o.FetchTimeout <= 0 {
		o.FetchTimeout = FetchTimeout
	}
	if o.Transport == nil {
		o.Transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Source{
		o: o, base: u,
		fetchHC: &http.Client{Transport: o.Transport, Timeout: o.FetchTimeout, CheckRedirect: noRedirect},
		streamC: &http.Client{Transport: o.Transport, CheckRedirect: noRedirect},
		refresh: make(chan struct{}, 1), refetch: make(chan struct{}, 1), kick: make(chan struct{}, 1),
		msgs: make(chan func(*state), 64),
	}, nil
}

// CheckURL is T3's http rule: https is always fine; plain http only for a loopback host or
// when allowHTTP is set. It returns the parsed URL (scheme and host only).
func CheckURL(raw string, allowHTTP bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, fmt.Errorf("hubsrc: %q is not a hub URL", raw)
	}
	if u.Scheme == "http" && !allowHTTP && !isLoopback(u.Hostname()) {
		return nil, fmt.Errorf("hubsrc: %s is plain http to a non-loopback host", u.Host)
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func defaultBackoff(failures int) time.Duration {
	d := time.Second << uint(min(max(failures, 0), 6))
	return min(d, 60*time.Second)
}

// Refresh refetches everything now (and refolds the local source).
func (s *Source) Refresh() {
	select {
	case s.refresh <- struct{}{}:
	default:
	}
}

// state is owned by the merger goroutine.
type state struct {
	url      string
	hubState string // "" until known
	since    *time.Time
	ov       *wireOverview
	seats    []wireSeat
	sessions []wireSession
	local    *board.View
	lastHub  *board.View
	unsaved  bool
	savedAt  time.Time
	// askedSeats are seat ids whose first session event already caused an accounts refetch.
	askedSeats map[string]bool
}

// Start begins reading and returns the channel of Views: the first as soon as the hub's
// state is known (live from a first fetch, offline from a failed one), closed when ctx ends.
// Start may be called once.
func (s *Source) Start(ctx context.Context) (<-chan board.View, error) {
	var started bool
	s.once.Do(func() { started = true })
	if !started {
		return nil, errors.New("hubsrc: already started")
	}
	out := make(chan board.View, 1)
	var local <-chan board.View
	if s.o.Local != nil {
		lc, err := s.o.Local.Start(ctx)
		if err != nil {
			s.logf("local source: %v", err)
		} else {
			local = lc
		}
	}
	st := &state{url: s.base.String()}
	if s.o.StateDir != "" {
		st.lastHub = LoadLastView(s.o.StateDir, st.url)
	}
	go s.hubLoop(ctx)
	go s.merge(ctx, st, local, out)
	return out, nil
}

func (s *Source) logf(format string, args ...any) {
	if s.o.Logf != nil {
		s.o.Logf(format, args...)
	}
}

// merge is the one goroutine that owns state and writes to out.
func (s *Source) merge(ctx context.Context, st *state, local <-chan board.View, out chan board.View) {
	defer close(out)
	send := func() {
		v, ok := s.compose(st)
		if !ok {
			return
		}
		// Latest wins: replace a View the reader has not taken yet.
		for {
			select {
			case out <- v:
				return
			default:
			}
			select {
			case <-out:
			default:
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-s.msgs:
			f(st)
		drain:
			for {
				select {
				case f := <-s.msgs:
					f(st)
				default:
					break drain
				}
			}
			send()
		case v, ok := <-local:
			if !ok {
				local = nil
				continue
			}
			st.local = &v
			if st.hubState != board.HubLive {
				send()
			}
		case <-s.refresh:
			select {
			case s.refetch <- struct{}{}:
			default:
			}
			if s.o.Local != nil {
				s.o.Local.Refresh()
			}
		}
	}
}

// compose builds the View for the current state; false while nothing is known yet.
func (s *Source) compose(st *state) (board.View, bool) {
	now := s.o.Now()
	hub := board.HubInfo{State: st.hubState, URL: st.url}
	switch st.hubState {
	case board.HubLive:
		hub.OfflineSince = nil
		v := s.o.Build(BuildInput{
			Now: now, Loc: s.o.Loc, Machine: s.o.Machine, ThisSession: s.o.ThisSession, Hub: hub, Source: board.SourceHub,
			IncludeEnded: s.o.IncludeEnded, WarpColour: s.o.WarpColour, Drains: s.o.Drains, Offs: s.o.Offs,
			Seats: st.seats, Sessions: st.sessions, MachineCount: len(liveMachines(st.ov)),
		})
		st.lastHub, st.unsaved = &v, true
		s.maybeSave(st, now, false)
		return v, true
	case board.HubOffline:
		s.maybeSave(st, now, true)
		var last, local board.View
		if st.lastHub != nil {
			last = *st.lastHub
		}
		if st.local != nil {
			local = *st.local
		}
		since := now
		if st.since != nil {
			since = *st.since
		}
		last.Hub.URL = st.url
		return board.MergeOffline(last, local, since, s.boardOptions(now)), true
	case board.HubUnpaired:
		var v board.View
		if st.local != nil {
			v = *st.local
		} else {
			v = board.BuildHub(board.HubData{}, s.boardOptions(now))
		}
		v.Source, v.Hub = board.SourceLocal, hub
		return v, true
	}
	return board.View{}, false
}

// boardOptions are the builder options of this source at now.
func (s *Source) boardOptions(now time.Time) board.Options {
	return board.Options{Now: now, Loc: s.o.Loc, Machine: s.o.Machine, SessionID: s.o.ThisSession,
		IncludeEnded: s.o.IncludeEnded, WarpColour: s.o.WarpColour, Drains: s.o.Drains, Offs: s.o.Offs}
}

func (s *Source) maybeSave(st *state, now time.Time, force bool) {
	if s.o.StateDir == "" || st.lastHub == nil || !st.unsaved {
		return
	}
	if !force && now.Sub(st.savedAt) < saveLastViewAt {
		return
	}
	st.savedAt = now
	st.unsaved = false
	if err := SaveLastView(s.o.StateDir, *st.lastHub); err != nil {
		s.logf("last-view.json: %v", err)
	}
}

func liveMachines(ov *wireOverview) []wireMachine {
	if ov == nil {
		return nil
	}
	var out []wireMachine
	for _, m := range ov.Machines {
		if m.RemovedAt == nil {
			out = append(out, m)
		}
	}
	return out
}

func (s *Source) post(ctx context.Context, f func(*state)) {
	select {
	case s.msgs <- f:
	case <-ctx.Done():
	}
}

// ---- the hub connection

type snapshot struct {
	ov       wireOverview
	seats    []wireSeat
	sessions []wireSession
}

func (s *Source) hubLoop(ctx context.Context) {
	go s.accountsWorker(ctx)
	var (
		fails     int
		needFetch = true
		wasLive   bool
		lastID    string
	)
	fail := func(err error) (stop bool) {
		if ctx.Err() != nil {
			return true
		}
		if errors.Is(err, errUnauthorized) {
			s.logf("%v", err)
			if s.o.OnUnpaired != nil {
				s.o.OnUnpaired()
			}
			s.post(ctx, func(st *state) { st.hubState, st.since = board.HubUnpaired, nil })
			return true
		}
		fails++
		s.logf("hub: %v", err)
		if !wasLive || fails >= 2 {
			at := s.o.Now().UTC()
			s.post(ctx, func(st *state) {
				if st.hubState != board.HubOffline {
					st.hubState, st.since = board.HubOffline, &at
				}
			})
		}
		t := time.NewTimer(s.o.Backoff(fails - 1))
		defer t.Stop()
		select {
		case <-ctx.Done():
			return true
		case <-t.C:
		case <-s.refetch:
			needFetch = true
		}
		return false
	}
	for ctx.Err() == nil {
		if needFetch {
			snap, err := s.fetchAll(ctx)
			if err != nil {
				if fail(err) {
					return
				}
				continue
			}
			needFetch, fails, wasLive = false, 0, true
			lastID = strconv.FormatInt(snap.ov.StreamID, 10)
			s.post(ctx, func(st *state) {
				ov := snap.ov
				st.ov, st.seats, st.sessions = &ov, snap.seats, snap.sessions
				st.hubState, st.since = board.HubLive, nil
			})
		}
		err := s.stream(ctx, &lastID, &fails, &wasLive)
		switch {
		case errors.Is(err, errReset), errors.Is(err, errRefetch):
			needFetch = true
		default:
			if err == nil || errors.Is(err, errClosed) {
				// An established stream ended: the next attempt is the first reconnect.
				fails = 1
				t := time.NewTimer(s.o.Backoff(0))
				select {
				case <-ctx.Done():
					t.Stop()
					return
				case <-t.C:
				case <-s.refetch:
					needFetch = true
				}
				continue
			}
			if fail(err) {
				return
			}
		}
	}
}

// get decodes one JSON reply of the hub, capped at MaxReply.
func (s *Source) get(ctx context.Context, p string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base.String()+p, nil)
	if err != nil {
		return err
	}
	s.auth(req)
	req.Header.Set("Accept", "application/json")
	resp, err := s.fetchHC.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := statusErr(resp); err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxReply+1))
	if err != nil {
		return err
	}
	if len(b) > MaxReply {
		return fmt.Errorf("hubsrc: %s: reply over %d bytes", p, MaxReply)
	}
	return json.Unmarshal(b, into)
}

func (s *Source) auth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+s.o.Token)
	req.Header.Set("User-Agent", "wy-board")
}

func statusErr(resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return errUnauthorized
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return fmt.Errorf("hubsrc: %s answered a redirect (%d); redirects are not followed", resp.Request.URL.Path, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("hubsrc: %s answered %d", resp.Request.URL.Path, resp.StatusCode)
	}
	return nil
}

// fetchAll is T3's order: overview, then accounts, then sessions.
func (s *Source) fetchAll(ctx context.Context) (snapshot, error) {
	var snap snapshot
	if err := s.get(ctx, "/v1/overview", &snap.ov); err != nil {
		return snap, err
	}
	seats, err := s.fetchAccounts(ctx)
	if err != nil {
		return snap, err
	}
	snap.seats = seats
	base := "/v1/sessions?limit=" + strconv.Itoa(SessionsLimit)
	if s.o.IncludeEnded {
		base += "&include_ended=true"
	}
	// Follow `next` so a busy hub's tail is not silently cut at one page; the page count
	// and the total are capped (the hub sorts needs-you first, so the cut drops the quiet).
	cursor := ""
	for page := 0; page < MaxSessionPages; page++ {
		var ss struct {
			Sessions []wireSession `json:"sessions"`
			Next     *string       `json:"next"`
		}
		q := base
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		if err := s.get(ctx, q, &ss); err != nil {
			return snap, err
		}
		for _, x := range ss.Sessions {
			if s.o.IncludeEnded || !ended(x.State) {
				snap.sessions = append(snap.sessions, x)
			}
		}
		if ss.Next == nil || *ss.Next == "" || *ss.Next == cursor || len(snap.sessions) >= MaxSessions {
			break
		}
		cursor = *ss.Next
	}
	if len(snap.sessions) > MaxSessions {
		snap.sessions = snap.sessions[:MaxSessions]
	}
	return snap, nil
}

func (s *Source) fetchAccounts(ctx context.Context) ([]wireSeat, error) {
	var as struct {
		Accounts []wireSeat `json:"accounts"`
	}
	if err := s.get(ctx, "/v1/accounts", &as); err != nil {
		return nil, err
	}
	return as.Accounts, nil
}

// accountsWorker refetches /v1/accounts after usage events, at most every 250 ms.
func (s *Source) accountsWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
		select {
		case <-s.kick:
		default:
		}
		seats, err := s.fetchAccounts(ctx)
		if err != nil {
			continue // the stream loop notices a dead hub on its own
		}
		s.post(ctx, func(st *state) { st.seats = seats })
	}
}

// stream reads /v1/stream until it ends. It sets *wasLive and resets *fails once the hub
// answers 200. It returns errReset on a `reset` event, errRefetch when Refresh was called,
// errPingTimeout after PingTimeout without any line, errClosed on a clean end.
func (s *Source) stream(ctx context.Context, lastID *string, fails *int, wasLive *bool) (err error) {
	sctx, cancel := context.WithCancelCause(ctx)
	watchdog := time.AfterFunc(s.o.PingTimeout, func() { cancel(errPingTimeout) })
	defer watchdog.Stop()
	// The watcher turns a Refresh (the `r` key) into errRefetch. When the stream ends for
	// another reason at the same moment, the watcher may already have taken the signal: it is
	// put back, or the press would be lost to a stream that no longer exists.
	var took atomic.Bool
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-s.refetch:
			took.Store(true)
			cancel(errRefetch)
		case <-sctx.Done():
		}
	}()
	defer func() {
		cancel(nil)
		<-watcherDone
		if took.Load() && !errors.Is(err, errRefetch) {
			select {
			case s.refetch <- struct{}{}:
			default:
			}
		}
	}()

	q := ""
	if *lastID != "" {
		q = "?last_event_id=" + url.QueryEscape(*lastID)
	}
	req, err := http.NewRequestWithContext(sctx, http.MethodGet, s.base.String()+"/v1/stream"+q, nil)
	if err != nil {
		return err
	}
	s.auth(req)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if *lastID != "" {
		req.Header.Set("Last-Event-ID", *lastID)
	}
	resp, err := s.streamC.Do(req)
	if err != nil {
		return causeOr(sctx, err)
	}
	defer resp.Body.Close()
	if err := statusErr(resp); err != nil {
		return err
	}
	*fails, *wasLive = 0, true
	s.post(ctx, func(st *state) {
		if st.hubState != board.HubLive && st.ov != nil {
			st.hubState, st.since = board.HubLive, nil
		}
	})

	p := NewParser(resp.Body)
	for {
		ev, err := p.Next()
		if err != nil {
			if errors.Is(err, io.EOF) && context.Cause(sctx) == nil {
				return errClosed
			}
			return causeOr(sctx, err)
		}
		watchdog.Reset(s.o.PingTimeout)
		if ev.Comment {
			continue
		}
		if id := p.LastID(); id != "" {
			*lastID = id
		}
		switch ev.Type {
		case "reset":
			return errReset
		case "session":
			var se wireSessionEvent
			if json.Unmarshal([]byte(ev.Data), &se) != nil || se.Session.ID == "" {
				continue
			}
			s.post(ctx, func(st *state) {
				if st.applySession(se.Session, s.o.IncludeEnded, s.o.Now()) {
					s.kickAccounts() // a seat the board has not seen yet: fetch /v1/accounts
				}
			})
		case "usage", "seats":
			s.kickAccounts()
		case "machine":
			var me wireMachineEvent
			if json.Unmarshal([]byte(ev.Data), &me) != nil || me.Machine.ID == "" {
				continue
			}
			s.post(ctx, func(st *state) { st.applyMachine(me.Machine) })
		}
	}
}

// causeOr returns why the stream context ended when we ended it, else err.
func causeOr(sctx context.Context, err error) error {
	if c := context.Cause(sctx); c != nil && !errors.Is(c, context.Canceled) {
		return c
	}
	return err
}

func (s *Source) kickAccounts() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// applySession folds one session event in. It reports whether the session names a seat the
// board has no account for yet (a new account appears with its first session).
func (st *state) applySession(ns wireSession, keepEnded bool, now time.Time) (unknownSeat bool) {
	if st.ov == nil {
		return false
	}
	if id := str(ns.Seat); id != "" {
		unknownSeat = !st.askedSeats[id]
		for _, a := range st.seats {
			if a.ID == id {
				unknownSeat = false
			}
		}
		if unknownSeat { // once per seat: a hidden seat is never in /v1/accounts
			if st.askedSeats == nil {
				st.askedSeats = map[string]bool{}
			}
			st.askedSeats[id] = true
		}
	}
	out := st.sessions[:0:0]
	for _, x := range st.sessions {
		if x.ID != ns.ID {
			out = append(out, x)
		}
	}
	if keepEnded || !ended(ns.State) {
		out = append(out, ns)
	}
	// Re-insert in the hub's order: a stable sort keeps the hub-fetched order where the
	// comparator ties.
	sort.SliceStable(out, func(i, j int) bool { return hubLess(out[i], out[j]) })
	// Ended sessions leave the board after EndedWithin; without this a long-running board
	// would keep every session the hub ever streamed. The slice is capped as well.
	kept := out[:0]
	for _, x := range out {
		if ended(x.State) && now.Sub(x.activity()) > board.EndedWithin {
			continue
		}
		kept = append(kept, x)
	}
	out = kept
	if len(out) > MaxSessions {
		out = out[:MaxSessions]
	}
	st.sessions = out
	return unknownSeat
}

func (st *state) applyMachine(m wireMachine) {
	if st.ov == nil {
		return
	}
	for i := range st.ov.Machines {
		if st.ov.Machines[i].ID == m.ID {
			st.ov.Machines[i] = m
			return
		}
	}
	st.ov.Machines = append(st.ov.Machines, m)
}
