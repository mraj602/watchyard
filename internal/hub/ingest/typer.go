package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/hub/usage"
	"github.com/mraj602/watchyard/internal/record"
)

// typer turns stored records into typed rows inside one write transaction.
type typer struct {
	tx   *store.Tx
	dev  store.Device
	ch   *Changes
	prev map[string]string // session -> state before this batch ("" = none)
	sp   int
	now  int64    // hub time of the batch (ms)
	logs []string // lines to log after the commit
}

// typeOne types one record inside a savepoint and marks the record row. A failure is not
// fatal: the record stays stored with typed = 0 and type_error set (record.md 5).
func (t *typer) typeOne(r *record.Record, ts time.Time, machineName string) error {
	if r.V > record.Version || !known(r.Kind) {
		return nil // stored raw, typed = 0, nothing wrong
	}
	t.sp++
	// Session changes made inside a savepoint that rolls back must not be reported.
	saved := t.snapshot()
	err := t.tx.Savepoint(fmt.Sprintf("rec%d", t.sp), func() error {
		if _, err := t.tx.EnsureMachine(r.Machine, machineName, ts.UnixMilli()); err != nil {
			return err
		}
		return t.dispatch(r, ts)
	})
	if err != nil {
		t.restore(saved)
		return t.tx.MarkTyped(r.ID, false, truncate(err.Error(), 500))
	}
	return t.tx.MarkTyped(r.ID, true, "")
}

func known(kind string) bool {
	switch kind {
	case record.KindHookEvent, record.KindLimitSample, record.KindWebUsage, record.KindTokenRows,
		record.KindSessionList, record.KindHistoryImport, record.KindSlotInfo,
		record.KindMachineInfo, record.KindDirectPush:
		return true
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "")
	}
	return s
}

type chSnap struct {
	sessions map[string]SessionChange
	seats    map[string]bool
	prev     map[string]string
}

func (t *typer) snapshot() chSnap {
	s := chSnap{sessions: map[string]SessionChange{}, seats: map[string]bool{}, prev: map[string]string{}}
	for k, v := range t.ch.Sessions {
		s.sessions[k] = v
	}
	for k, v := range t.ch.Seats {
		s.seats[k] = v
	}
	for k, v := range t.prev {
		s.prev[k] = v
	}
	return s
}

func (t *typer) restore(s chSnap) { t.ch.Sessions, t.ch.Seats, t.prev = s.sessions, s.seats, s.prev }

func (t *typer) dispatch(r *record.Record, ts time.Time) error {
	at := ts.UnixMilli()
	var slotID int64
	if r.Slot != "" && r.Kind != record.KindWebUsage {
		id, err := t.tx.EnsureSlot(r.Machine, r.Slot, at)
		if err != nil {
			return err
		}
		slotID = id
	}
	seat, err := t.seatOf(r, slotID, at)
	if err != nil {
		return err
	}
	c := &ctx{r: r, ts: ts, at: at, slotID: slotID, seat: seat}
	switch r.Kind {
	case record.KindHookEvent:
		return t.hookEvent(c)
	case record.KindSessionList:
		return t.sessionList(c)
	case record.KindLimitSample:
		return t.limitSample(c)
	case record.KindWebUsage:
		return t.webUsage(c)
	case record.KindTokenRows:
		return t.tokenRows(c)
	case record.KindHistoryImport:
		return t.historyImport(c)
	case record.KindSlotInfo:
		return t.slotInfo(c)
	case record.KindMachineInfo:
		return t.machineInfo(c)
	case record.KindDirectPush:
		return t.directPush(c)
	}
	return nil
}

// ctx is one record being typed.
type ctx struct {
	r      *record.Record
	ts     time.Time
	at     int64
	slotID int64
	seat   string
}

// seatOf resolves the record's seat (identity.md 5): the envelope seat, or for a seat
// known only by email and org the matching seat; records from a slot also move its
// slot_login, and without a seat take the seat the slot had at ts.
func (t *typer) seatOf(r *record.Record, slotID, at int64) (string, error) {
	var seat string
	if s := r.Seat; s != nil {
		switch {
		case s.Account != "" && s.Org != "":
			id, err := t.tx.UpsertSeat(store.SeatKey{Account: s.Account, Org: s.Org, Email: s.Email}, at)
			if err != nil {
				return "", err
			}
			seat = id
		case s.Org != "":
			id, ok, err := t.tx.SeatByEmailOrg(s.Email, s.Org)
			if err != nil {
				return "", err
			}
			if ok {
				seat = id
			}
		}
	}
	if slotID == 0 {
		return seat, nil
	}
	if seat != "" {
		if err := t.tx.SlotLogin(slotID, &seat, at, r.ID); err != nil {
			return "", err
		}
		t.ch.Seats[seat] = true
		return seat, nil
	}
	id, ok, err := t.tx.SeatAt(slotID, at)
	if err != nil || !ok {
		return "", err
	}
	return id, nil
}

// touch records the state of a session before the batch first changed it.
func (t *typer) touch(id string, created bool) error {
	if _, ok := t.prev[id]; ok {
		return nil
	}
	f, ok, err := t.tx.GetFold(id)
	if err != nil {
		return err
	}
	t.prev[id] = ""
	if ok {
		t.prev[id] = f.State
	}
	if created {
		t.ch.Sessions[id] = SessionChange{Change: "new"}
	} else {
		t.ch.Sessions[id] = SessionChange{Change: "update", Previous: t.prev[id]}
	}
	return nil
}

// finish turns per-session "before" states into change kinds.
func (t *typer) finish() {
	for id, c := range t.ch.Sessions {
		if c.Change == "new" {
			continue
		}
		f, ok, err := t.tx.GetFold(id)
		if err != nil || !ok || f.State == c.Previous {
			continue
		}
		if f.State == "ended" || f.State == "gone" {
			c.Change = "ended"
		} else {
			c.Change = "state"
		}
		t.ch.Sessions[id] = c
	}
}

func str(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func derivedStr(r *record.Record, key string) (string, bool) {
	raw, ok := r.Derived[key]
	if !ok || string(raw) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// remoteURLPrefix is the only origin a session's remote_url may point at: the board renders
// it as a link, so anything else (javascript:, data:, other hosts) is dropped.
const remoteURLPrefix = "https://claude.ai/"

func cleanRemoteURL(u string) string {
	if strings.HasPrefix(u, remoteURLPrefix) && !strings.ContainsAny(u, " \t\r\n\"'<>") {
		return u
	}
	return ""
}

func project(cwd string) string {
	if cwd == "" {
		return ""
	}
	cwd = strings.ReplaceAll(cwd, "\\", "/")
	return path.Base(strings.TrimRight(cwd, "/"))
}

// errForeignSession is the type_error of a record that names a session of another machine
// (record.md 5.1): a machine's token speaks only for that machine's sessions.
var errForeignSession = errors.New("session belongs to another machine")

// foreign reports whether session id exists on the hub with a machine other than the
// record's (record.md 5.1), and logs it when it does. A session without a machine yet is not
// foreign: the first machine to name it takes it (ensureSession).
func (t *typer) foreign(c *ctx, id string) (bool, error) {
	s, ok, err := t.tx.GetSession(id)
	if err != nil || !ok || s.MachineID == "" || s.MachineID == c.r.Machine {
		return false, err
	}
	t.noteForeign(c, id, s.MachineID)
	return true, nil
}

func (t *typer) noteForeign(c *ctx, id, owner string) {
	t.logs = append(t.logs, fmt.Sprintf("ingest: %s record=%s from machine %s names session %s of machine %s: not applied",
		c.r.Kind, c.r.ID, c.r.Machine, id, owner))
}

// moved reports whether a hook event shows its session running in another folder of the
// same machine than the one the hub has (identity.md 5.3): `claude --resume` in a second
// account whose projects/ is shared with the first. A late record (older than the newest
// event folded in) never moves it.
func moved(s store.SessionRow, c *ctx) bool {
	return c.slotID != 0 && s.SlotID != 0 && s.SlotID != c.slotID && s.MachineID == c.r.Machine && c.at >= s.LastEventAt
}

// ensureSession creates the session from its first record, or fills what it lacks. A
// session of another machine is refused with errForeignSession.
func (t *typer) ensureSession(c *ctx, id string, info store.SessionInfo) error {
	if !sessionIDPattern.MatchString(id) {
		return fmt.Errorf("session id %q is not a Claude Code session id", id)
	}
	info.RemoteURL = cleanRemoteURL(info.RemoteURL)
	s, ok, err := t.tx.GetSession(id)
	if err != nil {
		return err
	}
	if ok && s.MachineID != "" && s.MachineID != c.r.Machine {
		t.noteForeign(c, id, s.MachineID)
		return fmt.Errorf("%w (session %s)", errForeignSession, id)
	}
	if !ok {
		if err := t.touch(id, true); err != nil {
			return err
		}
		// started stays unknown (NULL) when the record gives no start: a token_rows record's
		// ts is when the scan ran, not when the session started (states.md 5.1).
		started := info.StartedAt
		return t.tx.InsertSession(store.SessionRow{
			ID: id, SeatID: c.seat, SlotID: c.slotID, MachineID: c.r.Machine, Cwd: info.Cwd, Project: info.Project,
			Name: info.Name, Title: info.Title, Kind: info.Kind, TranscriptPath: info.TranscriptPath,
			RemoteURL: info.RemoteURL, Term: info.Term, Model: info.Model, StartedAt: started, ContextPct: info.ContextPct,
		})
	}
	if err := t.touch(id, false); err != nil {
		return err
	}
	if err := t.tx.SetSessionSeat(id, c.seat); err != nil {
		return err
	}
	if s.SlotID == 0 || s.MachineID == "" {
		if err := t.tx.SetSessionSlot(id, c.slotID, c.r.Machine); err != nil {
			return err
		}
	}
	if s.LastEventAt != 0 && c.at < s.LastEventAt {
		info.Term = "" // newest wins: a late record does not replace the terminal
	}
	return t.tx.PatchSession(id, info)
}

func (t *typer) hookEvent(c *ctx) error {
	var p struct {
		SessionID        string          `json:"session_id"`
		Event            string          `json:"hook_event_name"`
		Cwd              string          `json:"cwd"`
		TranscriptPath   string          `json:"transcript_path"`
		Source           string          `json:"source"`
		Reason           string          `json:"reason"`
		Error            json.RawMessage `json:"error"`
		NotificationType string          `json:"notification_type"`
		ToolName         string          `json:"tool_name"`
		Trigger          string          `json:"trigger"`
		SessionName      string          `json:"session_name"`
		Model            json.RawMessage `json:"model"`
	}
	if err := json.Unmarshal(c.r.Payload, &p); err != nil {
		return fmt.Errorf("hook payload: %w", err)
	}
	if p.SessionID == "" {
		return errors.New("hook_event without payload.session_id")
	}
	if p.Event == "" {
		return errors.New("hook_event without payload.hook_event_name")
	}
	info := store.SessionInfo{Cwd: p.Cwd, Project: project(p.Cwd), TranscriptPath: p.TranscriptPath, Name: p.SessionName, StartedAt: c.at}
	if c.r.Term != nil {
		b, _ := json.Marshal(c.r.Term)
		info.Term = string(b)
	}
	if u, ok := derivedStr(c.r, "remote_url"); ok {
		info.RemoteURL = u
	}
	if m := str(p.Model); m != "" {
		info.Model = m
	} else {
		var mo struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(p.Model, &mo) == nil {
			info.Model = mo.ID
		}
	}
	prev, known, err := t.tx.GetSession(p.SessionID)
	if err != nil {
		return err
	}
	if err := t.ensureSession(c, p.SessionID, info); err != nil {
		return err
	}
	if known && moved(prev, c) {
		if err := t.tx.MoveSession(p.SessionID, c.slotID, c.seat); err != nil {
			return err
		}
		if prev.SeatID != "" {
			t.ch.Seats[prev.SeatID] = true
		}
		if c.seat != "" {
			t.ch.Seats[c.seat] = true
		}
	}
	var sub string
	switch p.Event {
	case "Notification":
		sub = p.NotificationType
	case "SessionStart":
		sub = p.Source
	case "SessionEnd":
		sub = p.Reason
	case "StopFailure":
		sub = str(p.Error)
	case "PreCompact", "PostCompact":
		sub = p.Trigger
	}
	var lm *string
	if s, ok := derivedStr(c.r, "last_message"); ok {
		lm = &s
	}
	if err := t.tx.InsertHookEvent(store.HookEventRow{RecordID: c.r.ID, SessionID: p.SessionID, Event: p.Event,
		Subtype: sub, ToolName: p.ToolName, TS: c.at, LastMessage: lm}); err != nil {
		return err
	}
	in, err := hookInput(c.r.ID, c.ts, c.r)
	if err != nil {
		return err
	}
	return t.fold(p.SessionID, in)
}

type listItem struct {
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Status    string          `json:"status"`
	StartedAt json.RawMessage `json:"startedAt"`
}

func (t *typer) sessionList(c *ctx) error {
	var p record.SessionListPayload
	if err := json.Unmarshal(c.r.Payload, &p); err != nil {
		return fmt.Errorf("session_list payload: %w", err)
	}
	if c.slotID == 0 {
		return errors.New("session_list without slot")
	}
	listed := map[string]bool{}
	for _, raw := range p.Sessions {
		var it listItem
		if json.Unmarshal(raw, &it) != nil || it.SessionID == "" {
			continue
		}
		if f, err := t.foreign(c, it.SessionID); err != nil {
			return err
		} else if f {
			continue // another machine's session: not this list's to report
		}
		listed[it.SessionID] = true
		started := usage.ParseTime(it.StartedAt).UnixMilli()
		if started <= 0 || started > c.at {
			started = c.at
		}
		if err := t.ensureSession(c, it.SessionID, store.SessionInfo{Cwd: it.Cwd, Project: project(it.Cwd), Kind: it.Kind,
			Name: it.Name, StartedAt: started}); err != nil {
			return err
		}
		s, _, err := t.tx.GetSession(it.SessionID)
		if err != nil {
			return err
		}
		if s.SlotID != c.slotID {
			continue // listed by another slot than the one it belongs to: not its list
		}
		row, err := t.tx.LastTokenRowAt(it.SessionID, c.at)
		if err != nil {
			return err
		}
		if err := t.fold(it.SessionID, listInput(c.r.ID, c.ts, p.Complete, true, it.Status, listLastActive(it, c.at, row))); err != nil {
			return err
		}
	}
	if !p.Complete {
		return nil
	}
	open, err := t.tx.OpenSessionsOnSlot(c.slotID)
	if err != nil {
		return err
	}
	for _, id := range open {
		if listed[id] {
			continue
		}
		if err := t.touch(id, false); err != nil {
			return err
		}
		if err := t.fold(id, listInput(c.r.ID, c.ts, true, false, "", 0)); err != nil {
			return err
		}
	}
	return nil
}

func (t *typer) limitSample(c *ctx) error {
	var p struct {
		SessionID     string          `json:"session_id"`
		SessionName   string          `json:"session_name"`
		Model         json.RawMessage `json:"model"`
		ContextWindow struct {
			UsedPercentage *float64 `json:"used_percentage"`
		} `json:"context_window"`
	}
	if err := json.Unmarshal(c.r.Payload, &p); err != nil {
		return fmt.Errorf("limit_sample payload: %w", err)
	}
	if p.SessionID != "" {
		s, ok, err := t.tx.GetSession(p.SessionID)
		if err != nil {
			return err
		}
		if ok && s.MachineID != "" && s.MachineID != c.r.Machine {
			// Another machine's session: its name, model and context stay as they are, and
			// the samples below are not tied to it (record.md 5.1).
			t.noteForeign(c, p.SessionID, s.MachineID)
			ok, p.SessionID = false, ""
		}
		if ok {
			info := store.SessionInfo{Name: p.SessionName, ContextPct: p.ContextWindow.UsedPercentage}
			if raw, ok := c.r.Derived["context_pct"]; ok {
				var v *float64
				if json.Unmarshal(raw, &v) == nil && v != nil {
					info.ContextPct = v
				}
			}
			var mo struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(p.Model, &mo) == nil {
				info.Model = mo.ID
			}
			if err := t.touch(s.ID, false); err != nil {
				return err
			}
			if err := t.tx.PatchSession(s.ID, info); err != nil {
				return err
			}
			if err := t.tx.SetSessionSeat(s.ID, c.seat); err != nil {
				return err
			}
		}
	}
	samples := usage.StatusLineWindows(c.r.Payload)
	if len(samples) == 0 {
		return nil
	}
	if c.seat == "" {
		return errors.New("limit_sample: no seat (no envelope seat and no slot login at ts)")
	}
	return t.addSamples(c, samples, "statusline", p.SessionID)
}

func (t *typer) addSamples(c *ctx, samples []usage.Sample, source, session string) error {
	for _, s := range samples {
		var resets int64
		if !s.ResetsAt.IsZero() {
			resets = s.ResetsAt.UnixMilli()
		}
		ins, err := t.tx.AddSample(store.LimitSampleRow{SeatID: c.seat, Key: s.Key, Source: source, TS: c.at, UsedPct: s.UsedPct,
			ResetsAt: resets, UsedDollars: s.UsedDollars, LimitDollars: s.LimitDollars, SessionID: session, RecordID: c.r.ID},
			usage.Length(s.Key).Milliseconds())
		if err != nil {
			return err
		}
		if ins {
			t.ch.Seats[c.seat] = true
		}
	}
	return nil
}

func (t *typer) webUsage(c *ctx) error {
	var p record.WebUsagePayload
	if err := json.Unmarshal(c.r.Payload, &p); err != nil {
		return fmt.Errorf("web_usage payload: %w", err)
	}
	if c.seat == "" {
		return errors.New("web_usage without seat")
	}
	var org struct {
		Name         string   `json:"name"`
		Capabilities []string `json:"capabilities"`
		RateTier     string   `json:"rate_limit_tier"`
		Billing      string   `json:"billing_type"`
		Raven        *string  `json:"raven_type"`
	}
	if len(p.Org) > 0 && json.Unmarshal(p.Org, &org) == nil {
		raven := ""
		if org.Raven != nil {
			raven = *org.Raven
		}
		plan := usage.PlanOf(org.Capabilities, org.RateTier, raven, "")
		if err := t.tx.OrgInfo(c.r.Seat.Org, org.Name, plan, org.RateTier, org.Billing); err != nil {
			return err
		}
	}
	var acct struct {
		Email       string `json:"email_address"`
		FullName    string `json:"full_name"`
		DisplayName string `json:"display_name"`
	}
	if len(p.Account) > 0 && json.Unmarshal(p.Account, &acct) == nil && c.r.Seat.Account != "" {
		name := acct.DisplayName
		if name == "" {
			name = acct.FullName
		}
		if err := t.tx.AccountInfo(c.r.Seat.Account, acct.Email, name); err != nil {
			return err
		}
	}
	var stopped string
	if len(p.Error) > 0 && string(p.Error) != "null" {
		var e struct {
			Status json.RawMessage `json:"status"`
		}
		_ = json.Unmarshal(p.Error, &e)
		stopped = strings.Trim(string(e.Status), `"`)
		if stopped == "" || stopped == "null" {
			stopped = "error"
		}
	}
	if p.TabID != "" {
		if err := t.tx.WebSeat(c.r.Machine, p.TabID, c.seat, c.at, stopped); err != nil {
			return err
		}
	}
	if len(p.Usage) == 0 || p.Usage[0] != '{' {
		return nil
	}
	ins, err := t.tx.InsertWebUsage(c.seat, c.at, p.TabID, string(p.Usage), c.r.ID)
	if err != nil || !ins {
		return err
	}
	t.ch.Seats[c.seat] = true
	return t.addSamples(c, usage.WindowsOf(p.Usage), "web", "")
}

// msOf reads a time given as RFC 3339 or epoch seconds/ms.
func msOf(raw json.RawMessage) int64 {
	t := usage.ParseTime(raw)
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func (t *typer) tokenRows(c *ctx) error {
	var p record.TokenRowsPayload
	if err := json.Unmarshal(c.r.Payload, &p); err != nil {
		return fmt.Errorf("token_rows payload: %w", err)
	}
	// What the rows say about each session's transcript (states.md 5.1): first and last
	// activity and the project, so a session made here gets real times, not the scan's.
	ext := transcriptExtents(p.Rows)
	// Session facts first, so rows can take the session's seat.
	var facts []string
	for _, raw := range p.Sessions {
		var s struct {
			SessionID    string `json:"session_id"`
			OwnerAccount string `json:"owner_account"`
			OwnerOrg     string `json:"owner_org"`
			RemoteURL    string `json:"remote_url"`
			Title        string `json:"title"`
		}
		if json.Unmarshal(raw, &s) != nil || s.SessionID == "" {
			continue
		}
		if f, err := t.foreign(c, s.SessionID); err != nil {
			return err
		} else if f {
			continue
		}
		sc := *c
		if s.OwnerAccount != "" && s.OwnerOrg != "" {
			id, err := t.tx.UpsertSeat(store.SeatKey{Account: s.OwnerAccount, Org: s.OwnerOrg}, c.at)
			if err != nil {
				return err
			}
			sc.seat = id
		}
		e := ext[s.SessionID]
		if err := t.ensureSession(&sc, s.SessionID, store.SessionInfo{RemoteURL: s.RemoteURL, Title: s.Title,
			Project: project(e.cwd), StartedAt: e.first}); err != nil {
			return err
		}
		facts = append(facts, s.SessionID)
	}
	// Rows and limit hits of another machine's session are skipped (record.md 5.1).
	other := map[string]bool{}
	isOther := func(sid string) (bool, error) {
		if f, ok := other[sid]; ok {
			return f, nil
		}
		f, err := t.foreign(c, sid)
		other[sid] = f
		return f, err
	}
	seats := map[string]string{}
	seatOf := func(sid string) (string, error) {
		if s, ok := seats[sid]; ok {
			return s, nil
		}
		s, err := t.tx.SessionSeat(sid)
		seats[sid] = s
		return s, err
	}
	for i, row := range p.Rows {
		if row.Key == "" || row.SessionID == "" {
			return fmt.Errorf("rows[%d]: key and session_id are required", i)
		}
		ts, err := time.Parse(time.RFC3339Nano, row.TS)
		if err != nil {
			return fmt.Errorf("rows[%d]: bad ts %q", i, row.TS)
		}
		if f, err := isOther(row.SessionID); err != nil {
			return err
		} else if f {
			delete(ext, row.SessionID)
			continue
		}
		seat, err := seatOf(row.SessionID)
		if err != nil {
			return err
		}
		in := store.TokenRowIn{Key: row.Key, SessionID: row.SessionID, SeatID: seat, Project: row.Cwd, Model: row.Model,
			RecordID: c.r.ID, TS: ts.UnixMilli(), Input: row.Input, Output: row.Output, Cache5m: row.Cache5m,
			Cache1h: row.Cache1h, CacheRead: row.CacheRead}
		if row.Thinking != nil {
			in.Thinking = *row.Thinking
		}
		if row.Sidechain != nil {
			in.Sidechain = *row.Sidechain
		}
		if err := t.tx.MergeTokenRow(in); err != nil {
			return err
		}
	}
	// After the rows: a list-repaired session's refold reads them (typer.transcript).
	if err := t.transcripts(c, ext, facts); err != nil {
		return err
	}
	for _, raw := range p.LimitHits {
		var h struct {
			SessionID string          `json:"session_id"`
			TS        json.RawMessage `json:"ts"`
			Type      string          `json:"type"`
			Status    string          `json:"status"`
			ResetsAt  json.RawMessage `json:"resets_at"`
		}
		if json.Unmarshal(raw, &h) != nil || h.SessionID == "" {
			continue
		}
		ts := msOf(h.TS)
		if ts == 0 {
			continue
		}
		if f, err := isOther(h.SessionID); err != nil {
			return err
		} else if f {
			continue
		}
		seat, err := seatOf(h.SessionID)
		if err != nil {
			return err
		}
		if err := t.tx.InsertLimitHit(store.LimitHitRow{SessionID: h.SessionID, TS: ts, Type: h.Type, Status: h.Status,
			ResetsAt: msOf(h.ResetsAt), SeatID: seat}); err != nil {
			return err
		}
	}
	return nil
}

// extent is one session's span in a token_rows record.
type extent struct {
	first, last int64 // ms, 0 = unknown
	cwd         string
	rec         string // record with the newest row (refold only)
}

func transcriptExtents(rows []record.TokenRow) map[string]extent {
	out := map[string]extent{}
	for _, row := range rows {
		if row.SessionID == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, row.TS)
		if err != nil {
			continue
		}
		at := ts.UnixMilli()
		e, ok := out[row.SessionID]
		if !ok || at < e.first {
			e.first = at
			if row.Cwd != "" {
				e.cwd = row.Cwd
			}
		}
		if e.cwd == "" {
			e.cwd = row.Cwd
		}
		e.last = max(e.last, at)
		out[row.SessionID] = e
	}
	return out
}

// transcripts applies the rows' extents to the sessions the hub knows (the facts' sessions
// were just made or patched; other row sessions only if they exist): an earlier start, the
// project, and the transcript fold input.
func (t *typer) transcripts(c *ctx, ext map[string]extent, facts []string) error {
	ids := make([]string, 0, len(ext)+len(facts))
	seen := map[string]bool{}
	for _, id := range facts {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	rest := make([]string, 0, len(ext))
	for id := range ext {
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)
	for _, id := range rest {
		_, ok, err := t.tx.GetSession(id)
		if err != nil {
			return err
		}
		if !ok {
			continue // rows alone do not make a session
		}
		e := ext[id]
		if err := t.touch(id, false); err != nil {
			return err
		}
		if err := t.tx.PatchSession(id, store.SessionInfo{Project: project(e.cwd), StartedAt: e.first}); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		if err := t.transcript(id, tm(ext[id].last), c.r.ID); err != nil {
			return err
		}
	}
	return nil
}

func (t *typer) historyImport(c *ctx) error {
	if c.slotID == 0 {
		return errors.New("history_import without slot")
	}
	var p record.HistoryImportPayload
	if err := json.Unmarshal(c.r.Payload, &p); err != nil {
		return fmt.Errorf("history_import payload: %w", err)
	}
	var raw struct {
		DailyActivity []struct {
			Date          string `json:"date"`
			MessageCount  *int64 `json:"messageCount"`
			SessionCount  *int64 `json:"sessionCount"`
			ToolCallCount *int64 `json:"toolCallCount"`
		} `json:"dailyActivity"`
		DailyModelTokens []struct {
			Date          string           `json:"date"`
			TokensByModel map[string]int64 `json:"tokensByModel"`
		} `json:"dailyModelTokens"`
	}
	if err := json.Unmarshal(p.Raw, &raw); err != nil {
		return fmt.Errorf("history_import raw: %w", err)
	}
	for _, d := range raw.DailyActivity {
		if err := t.tx.UpsertHistoryDay(store.HistoryDayRow{SlotID: c.slotID, Day: d.Date, Model: "", SeatID: c.seat,
			RecordID: c.r.ID, Messages: d.MessageCount, Sessions: d.SessionCount, ToolCalls: d.ToolCallCount}); err != nil {
			return err
		}
	}
	for _, d := range raw.DailyModelTokens {
		for model, n := range d.TokensByModel {
			n := n
			if err := t.tx.UpsertHistoryDay(store.HistoryDayRow{SlotID: c.slotID, Day: d.Date, Model: model, SeatID: c.seat,
				RecordID: c.r.ID, Tokens: &n}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *typer) slotInfo(c *ctx) error {
	if c.slotID == 0 {
		return errors.New("slot_info without slot")
	}
	var p record.SlotInfoPayload
	if err := json.Unmarshal(c.r.Payload, &p); err != nil {
		return fmt.Errorf("slot_info payload: %w", err)
	}
	d := store.SlotDetails{RealPath: p.RealPath, Launcher: p.Launcher, ClaudeVersion: p.ClaudeVersion, Default: p.Default}
	if len(p.Sharing) > 0 && string(p.Sharing) != "null" {
		d.Sharing = string(p.Sharing)
	}
	if err := t.tx.SlotInfo(c.slotID, d); err != nil {
		return err
	}
	var oa struct {
		AccountUUID string `json:"accountUuid"`
		Email       string `json:"emailAddress"`
		OrgUUID     string `json:"organizationUuid"`
		OrgName     string `json:"organizationName"`
		RateTier    string `json:"organizationRateLimitTier"`
		Billing     string `json:"billingType"`
		DisplayName string `json:"displayName"`
	}
	_ = json.Unmarshal(p.OAuthAccount, &oa)
	var as struct {
		SubscriptionType string `json:"subscriptionType"`
		OrgName          string `json:"orgName"`
	}
	_ = json.Unmarshal(p.AuthStatus, &as)
	if !p.LoggedIn {
		// Logged out: open a period with no seat (identity.md 5.2).
		return t.tx.SlotLogin(c.slotID, nil, c.at, c.r.ID)
	}
	seat := c.seat
	acct, org := "", ""
	if c.r.Seat != nil {
		acct, org = c.r.Seat.Account, c.r.Seat.Org
	}
	if seat == "" && oa.AccountUUID != "" && oa.OrgUUID != "" {
		id, err := t.tx.UpsertSeat(store.SeatKey{Account: oa.AccountUUID, Org: oa.OrgUUID, Email: oa.Email}, c.at)
		if err != nil {
			return err
		}
		seat, acct, org = id, oa.AccountUUID, oa.OrgUUID
		if err := t.tx.SlotLogin(c.slotID, &seat, c.at, c.r.ID); err != nil {
			return err
		}
	}
	if seat == "" {
		return nil
	}
	t.ch.Seats[seat] = true
	// A slot_info label and colour apply only while no one set them in the UI (look_src
	// NULL or 'slot'; desktop.md C10, record.md 4.7).
	var look struct {
		Label  string `json:"label"`
		Colour string `json:"colour"`
	}
	_ = json.Unmarshal(c.r.Payload, &look)
	if _, err := t.tx.SeatSlotLook(seat, look.Label, look.Colour); err != nil {
		return err
	}
	if acct != "" {
		if err := t.tx.AccountInfo(acct, oa.Email, oa.DisplayName); err != nil {
			return err
		}
	}
	if org != "" {
		name := oa.OrgName
		if name == "" {
			name = as.OrgName
		}
		plan := ""
		if oa.RateTier != "" || as.SubscriptionType != "" {
			plan = usage.PlanOf(nil, oa.RateTier, "", as.SubscriptionType)
		}
		if err := t.tx.OrgInfo(org, name, plan, oa.RateTier, oa.Billing); err != nil {
			return err
		}
	}
	return nil
}
