// Package peerrelay is the hub's half of peer messages across machines (docs/spec/peers.md
// 8.3, 9.2, 10, 11): it answers `peers_list` with the session directory, checks and
// queues `peer_msg` envelopes for the target machine, turns `peer_result` and expiry into
// `peer_status` for the sender, records `peer_log` lines, and relays the owner's thread
// resume (`peer_thread_resume`, peers.md 6.3) to both machines, recording a machine that
// refuses it. It stores metadata only: the message text exists at the hub only sealed, inside
// link_queue, while it is queued.
package peerrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	hublink "github.com/mraj602/watchyard/internal/hub/link"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
	"github.com/mraj602/watchyard/internal/peers"
)

// Limits of the hub (peers.md 9.2).
const (
	// HubPerDay is the hub-wide message limit.
	HubPerDay = 2000
	// DirectoryWindow is how far back a session's last event may be for the directory.
	DirectoryWindow = 7 * 24 * time.Hour
)

// Reason words only the hub uses (peers.md 8.3, link.md 5.5).
const (
	ReasonForbidden       = "forbidden"
	ReasonAccountMismatch = "account_mismatch"
	ReasonQueueFull       = "queue_full"
)

// Relay implements hublink.PeerRelay.
type Relay struct {
	Store *store.Store
	Keys  *hubkeys.Service
	// Link queues frames and says who is connected; set once the hub link is made.
	Link *hublink.Hub
	Now  func() time.Time
	Log  *log.Logger

	mu    sync.Mutex
	rates *peers.RateState
	day   struct {
		start time.Time
		n     int
	}
}

// New returns a relay; set Link before the hub serves.
func New(st *store.Store, ks *hubkeys.Service, now func() time.Time, lg *log.Logger) *Relay {
	if now == nil {
		now = time.Now
	}
	if lg == nil {
		lg = log.New(io.Discard, "", 0)
	}
	return &Relay{Store: st, Keys: ks, Now: now, Log: lg, rates: peers.NewRateState()}
}

// Up handles one peer frame from a machine (hublink.PeerRelay).
func (r *Relay) Up(ctx context.Context, device string, u link.Up) (string, any, error) {
	switch u.T {
	case link.TypePeersList:
		var q peers.PeersListUp
		_ = json.Unmarshal(u.Raw, &q)
		rep, err := r.directory(ctx, device, q.ID)
		if err != nil {
			return "", nil, err
		}
		return link.TypePeers, rep, nil
	case link.TypePeerTag:
		var q peers.PeerTagUp
		_ = json.Unmarshal(u.Raw, &q)
		ans, err := r.tag(ctx, device, q)
		if err != nil {
			return "", nil, err
		}
		rep, err := r.directory(ctx, device, q.ID)
		if err != nil {
			return "", nil, err
		}
		rep.Tag = &ans
		return link.TypePeers, rep, nil
	case link.TypePeerMsg:
		var m peers.PeerMsgUp
		if err := json.Unmarshal(u.Raw, &m); err != nil {
			return link.TypePeerAck, peers.PeerAck{V: 1, Status: "refused", Detail: ReasonForbidden}, nil
		}
		ack, err := r.relay(ctx, device, m.Doc)
		if err != nil {
			return "", nil, err
		}
		return link.TypePeerAck, ack, nil
	case link.TypePeerResult:
		var m peers.PeerResultUp
		if err := json.Unmarshal(u.Raw, &m); err == nil {
			if err := r.result(ctx, device, m); err != nil {
				return "", nil, err
			}
		}
		return "", nil, nil
	case link.TypePeerTrust:
		var m peers.PeerTrustUp
		if err := json.Unmarshal(u.Raw, &m); err == nil {
			if err := r.trust(ctx, device, m); err != nil {
				return "", nil, err
			}
		}
		return "", nil, nil
	case link.TypePeerLog:
		var m peers.PeerLogUp
		if err := json.Unmarshal(u.Raw, &m); err == nil {
			record := r.peerLog
			if m.Event != "" {
				record = r.threadResumeRefused
			}
			if err := record(ctx, device, m); err != nil {
				return "", nil, err
			}
		}
		return "", nil, nil
	}
	return "", nil, nil
}

// ---- the directory (peers.md 3) ----

// Directory is the hub's session directory: every live session with its handle and the agent
// device of its machine ("" when none).
func (r *Relay) Directory(ctx context.Context) ([]peers.WireSession, error) {
	ss, err := r.Store.PeerSessions(ctx, r.Now().Add(-DirectoryWindow).UnixMilli())
	if err != nil {
		return nil, err
	}
	agents, err := r.Store.AgentDevices(ctx)
	if err != nil {
		return nil, err
	}
	tags, err := r.Store.PeerTags(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]peers.WireSession, 0, len(ss))
	count := map[string]int{}
	for _, s := range ss {
		w := peers.WireSession{Session: s.ID, Account: s.Account, AccountLabel: s.AccountLabel, Machine: s.MachineName,
			MachineID: s.MachineID, Project: s.Project, State: s.State}
		if w.Project == "" {
			w.Project = s.Cwd
		}
		w.Name = peers.HandlePart(s.Name)
		if w.Name == "" {
			w.Name = peers.HandlePart(path.Base(w.Project))
		}
		if w.Name == "" || w.Name == "." || w.Name == "/" {
			w.Name = "session-" + short(s.ID)
		}
		// The canonical handle: the seat's label, the same for every machine that shows the
		// session (peers.md 3.2). The slot folder's name, which the session's own machine
		// shows when no label is set there, stays an alias.
		label := peers.CanonicalLabel(s.AccountLabel, s.Account)
		machine := peers.HandlePart(s.MachineName)
		base := func() string { return peers.MakeHandle(w.Name, label, machine) }
		count[base()]++
		if n := count[base()]; n > 1 {
			w.Name += fmt.Sprintf("-%d", n)
		}
		w.Handle = base()
		if fl := peers.FolderLabel(s.SlotFolder); fl != "" && fl != label {
			w.Aliases = []string{peers.MakeHandle(w.Name, fl, machine)}
		}
		if d, ok := agents[s.MachineID]; ok {
			w.Device = &d
		}
		w.Tags = tags[s.ID]
		out = append(out, w)
	}
	return out, nil
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// directory answers peers_list: the other machines' sessions, the asking machine's own (mine:
// their canonical handles, so its agent uses the names everyone else sees), and the
// certificate of each agent device the others run on.
func (r *Relay) directory(ctx context.Context, device, id string) (peers.PeersReply, error) {
	rep := peers.PeersReply{V: 1, ID: id, Sessions: []peers.WireSession{}, Certs: map[string]keys.Doc{}}
	var mine string
	_ = r.Store.Write(ctx, func(tx *store.Tx) error {
		_, mine, _, _ = tx.DeviceMachine(device)
		return nil
	})
	all, err := r.Directory(ctx)
	if err != nil {
		return rep, err
	}
	// The asking device's own certificate too, so its agent can tell (and say) that the
	// other machines will refuse it while it has no owner signature (peers.md 3.2 blocked).
	if c, err := r.Keys.Cert(ctx, device); err == nil {
		rep.Certs[device] = c.Doc
	}
	for _, s := range all {
		if s.MachineID == mine {
			if mine != "" {
				rep.Mine = append(rep.Mine, s)
			}
			continue
		}
		rep.Sessions = append(rep.Sessions, s)
		if s.Device != nil {
			if _, ok := rep.Certs[*s.Device]; !ok {
				if c, err := r.Keys.Cert(ctx, *s.Device); err == nil {
					rep.Certs[*s.Device] = c.Doc
				}
			}
		}
	}
	if mine != "" {
		rows, err := r.Store.MachinePeerTrust(ctx, mine)
		if err != nil {
			return rep, err
		}
		for _, t := range rows {
			if rep.Trust == nil {
				rep.Trust = map[string]peers.TrustState{}
			}
			st := peers.TrustState{ApprovedAt: isoMS(t.ApprovedAt)}
			if t.RevokedAt != 0 {
				v := isoMS(t.RevokedAt)
				st.RevokedAt = &v
			}
			rep.Trust[t.Sender] = st
		}
	}
	return rep, nil
}

func isoMS(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339) }

// ---- the trusted sender (peers.md 4.1a) ----

// trust stores a machine's peer_trust report: "set" with its record, "off" when it was turned
// off at the machine's terminal. Only a machine (or desktop) device that is not revoked may
// report, and only for its own machine. A malformed report is dropped.
func (r *Relay) trust(ctx context.Context, device string, m peers.PeerTrustUp) error {
	if m.Sender != peers.TrustDoorbell || (m.Op != "set" && m.Op != "off") {
		return nil
	}
	var approved time.Time
	if m.Op == "set" {
		t, err := time.Parse(time.RFC3339Nano, m.ApprovedAt)
		if err != nil || m.Account == "" || !strings.HasPrefix(m.Program, "/") || !strings.HasPrefix(m.Interpreter, "/") ||
			!strings.HasPrefix(m.Venv, "/") {
			r.Log.Printf("peers: device %s sent a malformed peer_trust; dropped", device)
			return nil
		}
		approved = t
	}
	now := r.Now().UnixMilli()
	return r.Store.Write(ctx, func(tx *store.Tx) error {
		kind, machine, revoked, err := tx.DeviceMachine(device)
		if err != nil || revoked || machine == "" || (kind != "machine" && kind != "desktop") {
			r.Log.Printf("peers: peer_trust from device %s, which is not a machine's; dropped", device)
			return nil
		}
		var changed bool
		event := "trust"
		if m.Op == "set" {
			changed, err = tx.PutPeerTrust(store.PeerTrustRow{Machine: machine, Sender: m.Sender, Device: device, Account: m.Account,
				Program: m.Program, Interpreter: m.Interpreter, Venv: m.Venv, ApprovedAt: approved.UnixMilli(), TestBuild: m.TestBuild}, now)
		} else {
			event = "trust_off"
			changed, err = tx.RevokePeerTrust(machine, m.Sender, "cli", now)
			if errors.Is(err, store.ErrNotFound) {
				err = nil
			}
		}
		if err != nil || !changed {
			return err
		}
		d, _ := json.Marshal(map[string]any{"machine": machine, "sender": m.Sender, "account": m.Account})
		return tx.InsertPeerAudit(store.PeerAuditRow{At: now, Actor: device, Event: event, Detail: d})
	})
}

// ---- tags (peers.md 3.4) ----

// tag applies one peer_tag: the session must be a live session of the asking device's machine
// (a machine tags only its own sessions, as it reports only its own records), the tag a slug,
// and a session holds at most peers.MaxTagsPerSession. A refusal is the answer's Error word;
// err is only a store failure.
func (r *Relay) tag(ctx context.Context, device string, q peers.PeerTagUp) (peers.TagAnswer, error) {
	ans := peers.TagAnswer{Session: q.Session, Tags: []string{}}
	if q.Op != peers.TagAdd && q.Op != peers.TagRm || !peers.ValidTag(q.Tag) {
		ans.Error = peers.TagErrBadTag
		return ans, nil
	}
	err := r.Store.Write(ctx, func(tx *store.Tx) error {
		kind, machine, revoked, err := tx.DeviceMachine(device)
		if err != nil || revoked || machine == "" || (kind != "machine" && kind != "desktop") {
			ans.Error = peers.TagErrForbidden
			return nil
		}
		s, ok, err := tx.PeerSession(q.Session)
		if err != nil {
			return err
		}
		switch {
		case !ok || !s.Live:
			ans.Error = peers.TagErrUnknown
			return nil
		case s.MachineID != machine:
			ans.Error = peers.TagErrForbidden
			return nil
		}
		if q.Op == peers.TagAdd {
			ans.Changed, err = tx.AddPeerTag(q.Session, q.Tag, device, peers.MaxTagsPerSession, tx.NowMS())
			if errors.Is(err, store.ErrTooManyTags) {
				ans.Error, err = peers.TagErrTooMany, nil
			}
		} else {
			ans.Changed, err = tx.RemovePeerTag(q.Session, q.Tag)
		}
		if err != nil {
			return err
		}
		t, err := tx.SessionPeerTags(q.Session)
		if t != nil {
			ans.Tags = t
		}
		return err
	})
	return ans, err
}

// ---- relaying (peers.md 8.3) ----

func refused(msg, detail string) peers.PeerAck {
	return peers.PeerAck{V: 1, Msg: msg, Status: "refused", Detail: detail}
}

// relay checks one envelope and queues it for the target machine.
func (r *Relay) relay(ctx context.Context, device string, doc keys.Doc) (peers.PeerAck, error) {
	cur, err := r.Keys.MatrixCurrent(ctx)
	if err != nil {
		return peers.PeerAck{}, err
	}
	m := peers.NewDocMatrix(cur.Signed.Body)
	agents, err := r.Store.AgentDevices(ctx)
	if err != nil {
		return peers.PeerAck{}, err
	}
	var ack peers.PeerAck
	var audit []func(tx *store.Tx) error
	err = r.Store.Write(ctx, func(tx *store.Tx) error {
		audit = nil
		// 1. the sender: its certificate, the signature, its machine and session.
		_, machine, revoked, err := tx.DeviceMachine(device)
		if err != nil || revoked {
			ack = refused("", ReasonForbidden)
			return nil
		}
		cert, err := r.Keys.CertTx(tx, device)
		if err != nil {
			ack = refused("", ReasonForbidden)
			return nil
		}
		env, err := peers.VerifyEnvelope(doc, cert)
		if err != nil || env.From.Machine != machine {
			ack = refused(env.Msg, ReasonForbidden)
			return nil
		}
		var from store.PeerSession
		if env.Service() {
			// The trusted sender (peers.md 4.1a): no session row; the machine's peer_trust row
			// must be active and name the account the envelope claims.
			t, ok, err := tx.PeerTrust(machine, peers.TrustDoorbell)
			if err != nil {
				return err
			}
			if env.From.Machine != machine || env.From.Session != peers.ServiceSessionID(machine) {
				r.Log.Printf("peers: trusted-sender message %s from machine %s refused: not_trusted", env.Msg, machine)
				ack = refused(env.Msg, ReasonForbidden)
				return nil
			}
			if !ok || !t.Active() || t.Account != env.From.Account {
				r.Log.Printf("peers: trusted-sender message %s from machine %s refused: not_trusted", env.Msg, machine)
				ack = refused(env.Msg, ReasonForbidden)
				return nil
			}
			from = store.PeerSession{ID: env.From.Session, MachineID: machine, Account: t.Account, Live: true}
		} else {
			f, ok, err := tx.PeerSession(env.From.Session)
			if err != nil {
				return err
			}
			if !ok || f.MachineID != machine {
				ack = refused(env.Msg, ReasonForbidden)
				return nil
			}
			from = f
		}
		if old, err := tx.PeerMessage(env.Msg); err == nil {
			// A repeat of a message the hub has (the agent resent it after a reconnect).
			if old.FromDevice != device {
				ack = refused(env.Msg, ReasonForbidden)
			} else {
				ack = peers.PeerAck{V: 1, Msg: env.Msg, Status: old.Status, Detail: old.Detail, Exp: old.ExpiresAt / 1000}
				if old.Status != "queued" && old.Status != "refused" {
					ack.Status = "queued" // already relayed; the final status follows as peer_status
				}
			}
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		now := r.Now()
		nowMS := now.UnixMilli()
		row := store.PeerMessageRow{ID: env.Msg, Thread: env.Thread, FromSession: env.From.Session, FromDevice: device,
			FromAcct: env.From.Account, ToSession: env.To.Session, ToDevice: env.To.Device, ToAcct: env.To.Account,
			Kind: string(env.Kind), Size: int64(env.Size), Status: "queued", CreatedAt: nowMS, ExpiresAt: env.Exp * 1000}
		if env.Service() {
			row.FromKind, row.ViaTag = env.From.Kind, env.Tag
		}
		refuse := func(detail string) error {
			row.Status, row.Detail, row.FinalAt = "refused", detail, nowMS
			ack = refused(env.Msg, detail)
			return tx.InsertPeerMessage(row)
		}
		// The rate token is taken here, once, for the verified sender and before any other
		// check, so a machine that keeps sending refused envelopes is limited like one that
		// sends good ones (a repeat above took none; a message the checks below refuse still
		// counts, peers.md 9.2).
		if !r.takeRate(now, env.From.Session, env.To.Session) {
			return refuse(peers.CodeRateLimited)
		}
		// 2. the target: live, on to.machine, whose agent device is to.device.
		to, ok, err := tx.PeerSession(env.To.Session)
		if err != nil {
			return err
		}
		switch {
		case !ok || to.MachineID != env.To.Machine || agents[to.MachineID] != env.To.Device:
			return refuse(peers.CodeUnknownTarget)
		case !to.Live:
			return refuse(peers.CodeTargetGone)
		}
		if env.Service() {
			// Doorbell sends only to a tag: the target must hold it now.
			held, err := tx.SessionPeerTags(env.To.Session)
			if err != nil {
				return err
			}
			if !slices.Contains(held, env.Tag) {
				return refuse(peers.CodeUnknownTarget)
			}
			// Only an agent that announced it understands a trusted send takes one: an older
			// agent would treat the envelope as a session's.
			caps, err := tx.DeviceCaps(env.To.Device)
			if err != nil {
				return err
			}
			if !slices.Contains(caps, link.CapPeerService) {
				return refuse(peers.ReasonReceiverTooOld)
			}
		}
		// 3. accounts (the hub's view wins; a different claim is refused), matrix, loop
		// guard, rates.
		for _, p := range []struct{ hub, claim *string }{{&from.Account, &row.FromAcct}, {&to.Account, &row.ToAcct}} {
			switch {
			case *p.hub == "":
			case *p.claim != "" && *p.claim != *p.hub:
				row.FromAcct, row.ToAcct = coalesce(from.Account, row.FromAcct), coalesce(to.Account, row.ToAcct)
				return refuse(ReasonAccountMismatch)
			default:
				*p.claim = *p.hub
			}
		}
		// The sender's account must be one the owner bound to the sending machine
		// (keys.md 4.3): the session table is filled by that machine, so without the binding
		// it could claim any account and get past the matrix (peers.md 5.3).
		accDoc, bound, ok, err := r.Keys.AccountsTx(tx, device, cert)
		if err != nil {
			return err
		}
		if !ok || !bound.Has(row.FromAcct) {
			return refuse(peers.ReasonUnboundAccount)
		}
		if env.Exp <= now.Unix() {
			return refuse(peers.CodeExpired)
		}
		ttl := time.Duration(env.Exp-now.Unix()) * time.Second
		if ttl > peers.MaxTTL+time.Minute {
			return refuse(peers.CodeFailed)
		}
		th, open, err := tx.OpenPeerThread(env.From.Session, env.To.Session)
		if err != nil {
			return err
		}
		if !open || nowMS-th.LastAt > peers.ThreadIdle.Milliseconds() {
			if open {
				th.ClosedAt = nowMS
				if err := tx.PutPeerThread(th); err != nil {
					return err
				}
			}
			th = store.PeerThreadRow{ID: env.Thread, A: env.From.Session, B: env.To.Session, AAccount: row.FromAcct,
				BAccount: row.ToAcct, OpenedAt: nowMS, LastAt: nowMS}
		}
		row.Thread = th.ID
		pt := threadOf(th)
		fromP := peers.Party{Session: env.From.Session, Account: row.FromAcct, Label: from.AccountLabel}
		toP := peers.Party{Session: env.To.Session, Account: row.ToAcct, Label: to.AccountLabel}
		mth := &pt
		if env.Service() {
			// Never a reply (peers.md 4.1a): a thread another session opened is refused, and
			// the reply rule never applies.
			if th.A != env.From.Session {
				return refuse(peers.CodeNotAllowed)
			}
			mth = nil
		}
		if d := m.Allowed(fromP, toP, mth); !d.Allowed {
			return refuse(d.Reason)
		}
		human := pt.HumanAt // zero when none is known (not 1970)
		if t, err := time.Parse(time.RFC3339Nano, env.From.HumanAt); err == nil && t.After(human) {
			human = t
		}
		if peers.LoopGuard(&pt, human, m.MaxTurns(), now) {
			th = rowOf(th, pt)
			if err := tx.PutPeerThread(th); err != nil {
				return err
			}
			audit = append(audit, func(tx *store.Tx) error {
				return tx.InsertPeerAudit(store.PeerAuditRow{At: nowMS, Actor: "hub", Event: "loop_guard", Thread: th.ID, Msg: env.Msg})
			})
			return refuse(peers.CodeLoopGuard)
		}
		// 4. record, count in the thread, queue for the target machine.
		body, err := json.Marshal(peers.QueuedPeerMsg{V: 1, Doc: doc, Cert: cert.Doc, Accounts: &accDoc})
		if err != nil {
			return err
		}
		var qerr error
		err = tx.Savepoint("peer_relay", func() error {
			if err := tx.InsertPeerMessage(row); err != nil {
				return err
			}
			pt.RecordMessage(now)
			if err := tx.PutPeerThread(rowOf(th, pt)); err != nil {
				return err
			}
			_, qerr = r.Link.EnqueueTx(tx, env.To.Device, link.TypePeerMsg, "", ttl, body, "pm:"+env.Msg)
			return qerr
		})
		switch {
		case errors.Is(qerr, hublink.ErrQueueFull):
			row.Status, row.Detail, row.FinalAt = "failed", ReasonQueueFull, nowMS
			ack = refused(env.Msg, ReasonQueueFull)
			return tx.InsertPeerMessage(row)
		case errors.Is(qerr, hublink.ErrNoLink):
			return refuse(peers.CodeUnknownTarget)
		case err != nil:
			return err
		}
		ack = peers.PeerAck{V: 1, Msg: env.Msg, Status: "queued", Exp: env.Exp}
		if !r.Link.Connected(env.To.Device) {
			ack.Detail = "offline"
		}
		return nil
	})
	if err == nil && len(audit) > 0 {
		err = r.Store.Write(ctx, func(tx *store.Tx) error {
			for _, f := range audit {
				if err := f(tx); err != nil {
					return err
				}
			}
			return nil
		})
	}
	return ack, err
}

func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// takeRate applies 9.2 at the hub: the sender's bucket, the pair's hour and the hub's day.
func (r *Relay) takeRate(now time.Time, from, to string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Sub(r.day.start) >= 24*time.Hour {
		r.day.start, r.day.n = now, 0
	}
	if r.day.n >= HubPerDay {
		return false
	}
	if _, ok := peers.CheckRate(r.rates, now, from, peers.PairKey(from, to)); !ok {
		return false
	}
	r.rates.Prune(now)
	r.day.n++
	return true
}

func threadOf(t store.PeerThreadRow) peers.Thread {
	th := peers.Thread{ID: t.ID, A: t.A, B: t.B, AAccount: t.AAccount, BAccount: t.BAccount, OpenedAt: time.UnixMilli(t.OpenedAt),
		LastAt: time.UnixMilli(t.LastAt), Turns: int(t.Turns)}
	if t.HumanAt != 0 {
		th.HumanAt = time.UnixMilli(t.HumanAt)
	}
	if t.PausedAt != 0 {
		p := time.UnixMilli(t.PausedAt)
		th.PausedAt = &p
	}
	return th
}

func rowOf(r store.PeerThreadRow, t peers.Thread) store.PeerThreadRow {
	r.LastAt, r.Turns, r.HumanAt, r.PausedAt = t.LastAt.UnixMilli(), int64(t.Turns), 0, 0
	if !t.HumanAt.IsZero() {
		r.HumanAt = t.HumanAt.UnixMilli()
	}
	if t.PausedAt != nil {
		r.PausedAt = t.PausedAt.UnixMilli()
	}
	return r
}

// ---- results, expiry, pause ----

// finalStatuses are the statuses a receiving agent may report.
var finalStatuses = map[string]bool{"delivered": true, "rejected": true, "target_gone": true, "failed": true}

// result is peer_result: the receiving machine's verdict becomes the message's final status
// and a peer_status to the sender.
func (r *Relay) result(ctx context.Context, device string, m peers.PeerResultUp) error {
	if !finalStatuses[m.Status] {
		return nil
	}
	return r.Store.Write(ctx, func(tx *store.Tx) error {
		row, err := tx.PeerMessage(m.Msg)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.ToDevice != device || row.Local {
			r.Log.Printf("peers: %s reported a result for message %s, which is not for it; ignored", device, m.Msg)
			return nil
		}
		nowMS := r.Now().UnixMilli()
		if t, err := time.Parse(time.RFC3339Nano, m.HumanAt); err == nil {
			if err := tx.PeerThreadHuman(row.Thread, t.UnixMilli()); err != nil {
				return err
			}
		}
		final, err := tx.FinishPeerMessage(m.Msg, m.Status, m.Detail, nowMS)
		if err != nil || !final {
			return err
		}
		if m.Status == "rejected" {
			d, _ := json.Marshal(map[string]string{"device": device, "reason": m.Detail})
			if err := tx.InsertPeerAudit(store.PeerAuditRow{At: nowMS, Actor: device, Event: "rejected", Thread: row.Thread, Msg: m.Msg,
				Detail: d}); err != nil {
				return err
			}
		}
		return r.statusTx(tx, row, m.Status, m.Detail)
	})
}

// statusTx queues peer_status for a message's sender.
func (r *Relay) statusTx(tx *store.Tx, row store.PeerMessageRow, status, detail string) error {
	b, _ := json.Marshal(peers.PeerStatusBody{V: 1, Msg: row.ID, Status: status, Detail: detail,
		At: r.Now().UTC().Format(time.RFC3339Nano)})
	_, err := r.Link.EnqueueTx(tx, row.FromDevice, link.TypePeerStatus, "ps:"+row.ID, hublink.TTLPeerStatus, b, "")
	if errors.Is(err, hublink.ErrNoLink) {
		return nil // the sender's machine was revoked or removed
	}
	return err
}

// Expired is hublink.Config.PeerExpired: a queued message's row expired unsent.
func (r *Relay) Expired(tx *store.Tx, id string) error {
	return r.finishTx(tx, id, peers.CodeExpired, "")
}

func (r *Relay) finishTx(tx *store.Tx, id, status, detail string) error {
	row, err := tx.PeerMessage(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	final, err := tx.FinishPeerMessage(id, status, detail, r.Now().UnixMilli())
	if err != nil || !final {
		return err
	}
	return r.statusTx(tx, row, status, detail)
}

// MatrixChanged is called with every new matrix document (after hubkeys stored it). A pause
// drops every queued peer_msg at once: each becomes expired (paused) and its sender is told
// (peers.md 11).
func (r *Relay) MatrixChanged(ctx context.Context, d keys.Doc) error {
	raw, err := d.BodyBytes()
	if err != nil {
		return err
	}
	var b keys.MatrixBody
	if err := json.Unmarshal(raw, &b); err != nil || !b.Paused {
		return err
	}
	return r.Store.Write(ctx, func(tx *store.Tx) error {
		rows, err := tx.LinkDropType(link.TypePeerMsg)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if id, ok := strings.CutPrefix(row.Ref, "pm:"); ok && id != "" {
				if err := r.finishTx(tx, id, peers.CodeExpired, peers.CodePaused); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ---- thread resume (peers.md 6.3) ----

// ErrThreadClosed: the thread to resume is closed (a new message opens a new one).
var ErrThreadClosed = errors.New("the thread is closed")

// ResumeResult is what ResumeThread did: whether the hub's thread was stopped, and the
// agent devices the resume was queued for (one per machine of the thread's two sessions).
type ResumeResult struct {
	WasPaused bool
	Devices   []string
}

// ResumeThread is the owner's "Resume thread": the hub's own loop guard for the thread is
// reset (turns 0, paused_at cleared), a peer_audit row is written, and a peer_thread_resume
// is queued to the agent of each machine in the thread, which applies it only under
// hub_may_widen (keys.md 5.4). by is the asking device. store.ErrNotFound: no such thread.
func (r *Relay) ResumeThread(ctx context.Context, id, by string) (ResumeResult, error) {
	var res ResumeResult
	agents, err := r.Store.AgentDevices(ctx)
	if err != nil {
		return res, err
	}
	now := r.Now()
	body, err := json.Marshal(peers.ThreadResumeBody{V: 1, Thread: id, By: by, At: now.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return res, err
	}
	err = r.Store.Write(ctx, func(tx *store.Tx) error {
		res = ResumeResult{}
		th, err := tx.PeerThread(id)
		if err != nil {
			return err
		}
		if th.ClosedAt != 0 {
			return ErrThreadClosed
		}
		res.WasPaused = th.PausedAt != 0
		th.Turns, th.PausedAt = 0, 0
		if err := tx.PutPeerThread(th); err != nil {
			return err
		}
		for _, s := range []string{th.A, th.B} {
			ps, ok, err := tx.PeerSession(s)
			if err != nil {
				return err
			}
			dev := agents[ps.MachineID]
			if !ok || dev == "" || slices.Contains(res.Devices, dev) {
				continue
			}
			_, err = r.Link.EnqueueTx(tx, dev, link.TypePeerThreadResume, "tr:"+id, peers.ThreadIdle, body, "")
			switch {
			case errors.Is(err, hublink.ErrNoLink):
				continue // revoked or without keys: nothing to tell
			case err != nil:
				return err
			}
			res.Devices = append(res.Devices, dev)
		}
		d, _ := json.Marshal(map[string]any{"was_paused": res.WasPaused, "machines": len(res.Devices)})
		return tx.InsertPeerAudit(store.PeerAuditRow{At: now.UnixMilli(), Actor: by, Event: "thread_resume", Thread: id, Detail: d})
	})
	return res, err
}

// threadResumeRefused records a machine that refused the owner's peer_thread_resume and keeps
// its loop-guard stop (peers.md 6.3 step 2; a peer_log with event thread_resume_refused). Only
// the agent device of a machine one of the thread's two sessions runs on may report it; any
// other report is dropped. The hub writes a thread_resume_refused audit row (actor: that
// device; detail: the machine's name) and marks its thread paused again (paused_at, if
// clear): turns stay 0, so the hub itself does not refuse, but the thread no longer reads as
// resumed everywhere while that machine still holds its stop.
func (r *Relay) threadResumeRefused(ctx context.Context, device string, m peers.PeerLogUp) error {
	if m.Event != peers.EventThreadResumeRefused || m.Detail != peers.ResumeRefusedReason || !peers.ValidThreadID(m.Thread) {
		return nil
	}
	agents, err := r.Store.AgentDevices(ctx)
	if err != nil {
		return err
	}
	now := r.Now().UnixMilli()
	return r.Store.Write(ctx, func(tx *store.Tx) error {
		th, err := tx.PeerThread(m.Thread)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		machine := ""
		for _, s := range []string{th.A, th.B} {
			ps, ok, err := tx.PeerSession(s)
			if err != nil {
				return err
			}
			if ok && ps.MachineID != "" && agents[ps.MachineID] == device {
				machine = coalesce(ps.MachineName, ps.MachineID)
				break
			}
		}
		if machine == "" {
			r.Log.Printf("peers: device %s reported a refused resume of thread %s, which has no session on its machine; dropped", device, m.Thread)
			return nil
		}
		if th.ClosedAt == 0 && th.PausedAt == 0 {
			th.PausedAt = now
			if err := tx.PutPeerThread(th); err != nil {
				return err
			}
		}
		d, _ := json.Marshal(map[string]any{"machine": machine})
		return tx.InsertPeerAudit(store.PeerAuditRow{At: now, Actor: device, Event: peers.EventThreadResumeRefused, Thread: m.Thread, Detail: d})
	})
}

// peerLog records a same-machine delivery an agent reports (peers.md 10, local = 1).
func (r *Relay) peerLog(ctx context.Context, device string, m peers.PeerLogUp) error {
	if m.Msg == "" || m.FromSession == "" || m.ToSession == "" || (m.Kind != peers.KindStart && m.Kind != peers.KindReply) {
		return nil
	}
	switch m.Status {
	case "delivered", "refused", "rejected", "target_gone", "failed":
	default:
		return nil
	}
	if m.FromKind != "" && (m.FromKind != peers.ServiceDoorbell || !peers.ValidTag(m.Tag)) || m.FromKind == "" && m.Tag != "" {
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, m.At)
	if err != nil {
		at = r.Now()
	}
	return r.Store.Write(ctx, func(tx *store.Tx) error {
		fromAcct := ""
		if m.FromKind != "" {
			// A trusted send on its own machine: the account is the one in the machine's row.
			_, machine, _, err := tx.DeviceMachine(device)
			if err != nil || m.FromSession != peers.ServiceSessionID(machine) {
				return nil
			}
			if t, ok, err := tx.PeerTrust(machine, peers.TrustDoorbell); err == nil && ok {
				fromAcct = t.Account
			}
		}
		acct := func(id string) string {
			s, ok, err := tx.PeerSession(id)
			if err != nil || !ok {
				return ""
			}
			return s.Account
		}
		err := tx.InsertPeerMessage(store.PeerMessageRow{ID: m.Msg, Thread: m.Thread, FromSession: m.FromSession, FromDevice: device,
			FromAcct: coalesce(fromAcct, acct(m.FromSession)), ToSession: m.ToSession, ToDevice: device, ToAcct: acct(m.ToSession), Kind: string(m.Kind),
			Size: int64(max(m.Size, 0)), Local: true, Status: m.Status, Detail: m.Detail, CreatedAt: at.UnixMilli(),
			ExpiresAt: at.UnixMilli(), FinalAt: at.UnixMilli(), FromKind: m.FromKind, ViaTag: m.Tag})
		if errors.Is(err, store.ErrDuplicate) {
			return nil
		}
		return err
	})
}
