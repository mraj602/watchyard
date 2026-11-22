// Package peerlink is the agent's path to sessions on other machines (docs/spec/peers.md 3.1
// hub source, 8.2, 8.4, 9.3). It implements peers.Remote over the link (the hub's
// directory with `peers_list`, envelopes with `peer_msg`, same-machine audit with
// `peer_log`) and linkclient.PeerHandler (queued `peer_msg`, `peer_status` and
// `peer_thread_resume`, the hub's ephemeral answers). It does not import linkclient: the client is reached through Sender.
package peerlink

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/link"
	"github.com/mraj602/watchyard/internal/peers"
)

// Waits of the agent (peers.md 4.3, 8.2).
const (
	// AckWait is how long a send waits for the hub's verdict (peer_ack).
	AckWait = 10 * time.Second
	// ListWait bounds a directory request; ListFresh is how long an answer is reused.
	ListWait  = 5 * time.Second
	ListFresh = 5 * time.Second
	// SeenFor is how long a handled message id is remembered against replays (8.4).
	SeenFor = 48 * time.Hour
)

// Sender writes a device→hub frame on the live link (linkclient.Client.Send).
type Sender interface {
	Send(ctx context.Context, v any) error
}

// Remote is the agent's peers.Remote and linkclient.PeerHandler.
type Remote struct {
	// Env is the agent's peers environment (its Remote is not used here).
	Env peers.Env
	// Link is set once the link client exists.
	Link   Sender
	Logger *log.Logger
	// AckWait, ListWait and Fresh override AckWait, ListWait and ListFresh (tests; a
	// negative Fresh asks the hub every time).
	AckWait, ListWait, Fresh time.Duration
	// HubMayWiden is config peers.hub_may_widen (keys.md 5.4). A thread resume the hub relays
	// (peer_thread_resume, peers.md 6.3) is signed by the hub alone and widens what this
	// machine delivers, so it is taken only when this is true. The zero value refuses.
	HubMayWiden bool

	mu     sync.Mutex
	lists  map[string]chan *peers.PeersReply
	acks   map[string]chan peers.PeerAck
	finals map[string]chan peers.Final
	cache  struct {
		at    time.Time
		reply *peers.PeersReply
	}
	seenMu sync.Mutex
	// spoolMu serialises the peer_log spool: a flush rewrites the file, which would drop a
	// line another send appended meanwhile.
	spoolMu sync.Mutex
}

// New returns a Remote for env; set Link before use.
func New(env peers.Env, lg *log.Logger) *Remote {
	if lg == nil {
		lg = log.New(io.Discard, "", 0)
	}
	return &Remote{Env: env, Logger: lg, lists: map[string]chan *peers.PeersReply{}, acks: map[string]chan peers.PeerAck{},
		finals: map[string]chan peers.Final{}}
}

func (r *Remote) now() time.Time {
	if r.Env.Now != nil {
		return r.Env.Now()
	}
	return time.Now()
}

func (r *Remote) send(v any) error {
	if r.Link == nil {
		return errors.New("the agent has no link to the hub")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.Link.Send(ctx, v)
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ---- the directory ----

// directory asks the hub for its session directory, reusing an answer for ListFresh unless
// force (a trusted send always asks, so a revocation applies to it at once: peers.md 4.1a).
func (r *Remote) directory(force bool) (*peers.PeersReply, error) {
	r.mu.Lock()
	fresh := r.Fresh
	if fresh == 0 {
		fresh = ListFresh
	}
	if !force && r.cache.reply != nil && r.now().Sub(r.cache.at) < fresh {
		rep := r.cache.reply
		r.mu.Unlock()
		return rep, nil
	}
	id := newID()
	ch := make(chan *peers.PeersReply, 1)
	r.lists[id] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.lists, id)
		r.mu.Unlock()
	}()
	if err := r.send(peers.PeersListUp{T: link.TypePeersList, ID: id}); err != nil {
		return nil, err
	}
	wait := r.ListWait
	if wait <= 0 {
		wait = ListWait
	}
	select {
	case rep := <-ch:
		if rep == nil {
			return nil, errors.New("the hub does not relay peer messages")
		}
		r.mu.Lock()
		r.cache.at, r.cache.reply = r.now(), rep
		r.mu.Unlock()
		r.reconcileTrust(rep)
		return rep, nil
	case <-time.After(wait):
		return nil, errors.New("the hub did not answer")
	}
}

// Tag is peers.Remote: one peer_tag to the hub (peers.md 3.4, link.md 6.2). The hub answers
// with its directory after the change (a `peers` frame with the same id and `tag` set), which
// replaces the cached one, so the holders the caller looks up next are current.
func (r *Remote) Tag(session, op, tag string) (peers.TagAnswer, error) {
	id := newID()
	ch := make(chan *peers.PeersReply, 1)
	r.mu.Lock()
	r.lists[id] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.lists, id)
		r.mu.Unlock()
	}()
	if err := r.send(peers.PeerTagUp{T: link.TypePeerTag, ID: id, Session: session, Op: op, Tag: tag}); err != nil {
		return peers.TagAnswer{}, err
	}
	wait := r.ListWait
	if wait <= 0 {
		wait = ListWait
	}
	select {
	case rep := <-ch:
		if rep == nil || rep.Tag == nil {
			return peers.TagAnswer{}, errors.New("the hub does not take tags (it may be older than this wy; update it)")
		}
		r.mu.Lock()
		r.cache.at, r.cache.reply = r.now(), rep
		r.mu.Unlock()
		return *rep.Tag, nil
	case <-time.After(wait):
		return peers.TagAnswer{}, errors.New("the hub did not answer")
	}
}

// Sessions is peers.Remote: the other machines' sessions from the hub.
func (r *Remote) Sessions() ([]peers.Session, error) { return r.sessions(false) }

// SessionsFresh is Sessions asked of the hub now, never from the cache (peers.FreshRemote).
func (r *Remote) SessionsFresh() ([]peers.Session, error) { return r.sessions(true) }

func (r *Remote) sessions(force bool) ([]peers.Session, error) {
	rep, err := r.directory(force)
	if err != nil {
		return nil, err
	}
	out := make([]peers.Session, 0, len(rep.Sessions))
	check := r.reachability(rep)
	for _, w := range rep.Sessions {
		if w.MachineID == r.Env.Machine.ID {
			continue
		}
		s := peers.Session{Entry: peers.RegistryEntry{SessionID: w.Session}, Account: w.Account,
			AccountLabel: peers.CanonicalLabel(w.AccountLabel, w.Account), Handle: w.Handle, Aliases: append([]string(nil), w.Aliases...),
			Name: w.Name, Project: w.Project, MachineName: w.Machine, Remote: true, State: w.State, Mode: peers.ModePrompting,
			Tags: peers.NormTags(w.Tags)}
		if w.Device != nil {
			s.Device = *w.Device
			if b := check(s.Device, w.Machine); b != nil {
				s.Blocked = b.Reason
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// Names is peers.Remote: how the hub named every session in its last answer, this machine's
// own (PeersReply.Mine) included. It never asks the hub, so it is safe while a message from
// the link is being handled; nil before the first answer.
func (r *Remote) Names() map[string]peers.HubName {
	r.mu.Lock()
	rep := r.cache.reply
	r.mu.Unlock()
	if rep == nil {
		return nil
	}
	out := make(map[string]peers.HubName, len(rep.Sessions)+len(rep.Mine))
	add := func(w peers.WireSession, mine bool) {
		out[w.Session] = peers.HubName{Handle: w.Handle, Name: w.Name, Label: peers.CanonicalLabel(w.AccountLabel, w.Account),
			Account: w.Account, Aliases: append([]string(nil), w.Aliases...), Mine: mine, Tags: peers.NormTags(w.Tags)}
	}
	for _, w := range rep.Sessions {
		add(w, w.MachineID == r.Env.Machine.ID)
	}
	for _, w := range rep.Mine {
		add(w, true)
	}
	return out
}

// reachability returns the check of peers.Reachability for the machines of one directory
// reply: whether this machine and the machine of an agent device can exchange messages
// (certificates and pins), each device checked once.
func (r *Remote) reachability(rep *peers.PeersReply) func(device, machine string) *peers.ErrBlocked {
	trust, terr := keys.PinnedTrust(r.Env.State)
	rev := revocations(r.Env.State)
	var own *keys.Doc
	if dk, err := keys.ReadDeviceKeys(r.Env.State); err == nil && dk.Device != "" {
		if d, ok := rep.Certs[dk.Device]; ok {
			own = &d
		}
	}
	seen := map[string]*peers.ErrBlocked{}
	return func(device, machine string) *peers.ErrBlocked {
		if b, ok := seen[device]; ok {
			return b
		}
		var b *peers.ErrBlocked
		if terr != nil {
			b = &peers.ErrBlocked{Reason: "this machine's pins cannot be read (" + terr.Error() + ")"}
		} else {
			var target *keys.Doc
			if d, ok := rep.Certs[device]; ok {
				target = &d
			}
			b = peers.Reachability(trust, rev, own, target, device, machine)
		}
		seen[device] = b
		return b
	}
}

// ---- sending (peers.md 8.2) ----

// codes the hub may refuse with that are refusal codes of their own on this side.
var passThrough = map[string]bool{peers.CodeNotAllowed: true, peers.CodePaused: true, peers.CodeLoopGuard: true,
	peers.CodeRateLimited: true, peers.CodeUnknownTarget: true, peers.CodeTargetGone: true, peers.CodeExpired: true}

// hubRefusal turns a refused peer_ack into the sender's refusal.
func hubRefusal(a peers.PeerAck) *peers.Refusal {
	switch a.Detail {
	case peers.CodeNotAllowed:
		return peers.Refuse(peers.CodeNotAllowed, "the hub's matrix does not allow this pair")
	case peers.CodePaused:
		return peers.Refuse(peers.CodePaused, "peers are paused")
	case peers.CodeLoopGuard:
		return peers.Refuse(peers.CodeLoopGuard, "the hub stopped this thread: too many automated turns without a human prompt in either session; paused until you type in one of them")
	case peers.CodeRateLimited:
		return peers.Refuse(peers.CodeRateLimited, "the hub's rate limit is reached; retry in a minute")
	case peers.ReasonUnboundAccount:
		return peers.Refuse(peers.CodeRejected, "%s", peers.UnboundLine)
	case peers.ReasonReceiverTooOld:
		return peers.Refuse(peers.CodeRejected, "the receiving machine runs a Watchyard older than 1.3.0, which cannot take a message from Doorbell; update wy there")
	}
	if passThrough[a.Detail] {
		return peers.Refuse(a.Detail, "the hub refused it (%s)", a.Detail)
	}
	return peers.Refuse(peers.CodeFailed, "the hub refused it (%s)", a.Detail)
}

// Send is peers.Remote: seal to the target machine's certified key, sign, hand to the hub
// and wait for its verdict.
func (r *Remote) Send(o peers.Outgoing) (*peers.Pending, *peers.Refusal) {
	rep, err := r.directory(false)
	if err != nil {
		return nil, peers.Refuse(peers.CodeHubUnreachable, "the hub cannot be reached now (%v)", err)
	}
	var to *peers.WireSession
	for i := range rep.Sessions {
		if rep.Sessions[i].Session == o.To.Entry.SessionID {
			to = &rep.Sessions[i]
		}
	}
	if to == nil || to.Device == nil {
		return nil, peers.Refuse(peers.CodeUnknownTarget, "the hub no longer lists %s", o.To.Handle)
	}
	certDoc, ok := rep.Certs[*to.Device]
	if !ok {
		return nil, peers.Refuse(peers.CodeUnknownTarget, "the hub sent no certificate for %s's machine", o.To.Handle)
	}
	trust, err := keys.PinnedTrust(r.Env.State)
	if err != nil {
		return nil, peers.Refuse(peers.CodeFailed, "this machine's pins: %v", err)
	}
	// Certificates first (this machine's own too: every receiver refuses an uncertified
	// sender), with the reason and the owner's fix in the refusal line.
	if b := r.reachability(rep)(*to.Device, to.Machine); b != nil {
		return nil, peers.Refuse(peers.CodeRejected, "%s", peers.BlockedLine(o.To.Handle, b))
	}
	rev := revocations(r.Env.State)
	cert, err := peers.CheckMachineCert(certDoc, trust, rev, *to.Device)
	if err != nil {
		return nil, peers.Refuse(peers.CodeRejected, "the certificate of %s's machine does not check (%s); not sent", o.To.Handle, peers.ReasonOf(err))
	}
	dk, err := keys.ReadDeviceKeys(r.Env.State)
	if err != nil || dk.Device == "" {
		return nil, peers.Refuse(peers.CodeFailed, "this machine has no registered device keys (wy agent enable)")
	}
	sign, err := dk.SignKey()
	if err != nil {
		return nil, peers.Refuse(peers.CodeFailed, "%v", err)
	}
	now := o.Now
	if now.IsZero() {
		now = r.now()
	}
	e := peers.Envelope{Msg: o.Msg, Thread: o.Thread, Kind: o.Kind,
		From: peers.EnvFrom{Session: o.From.Entry.SessionID, Device: dk.Device, Machine: r.Env.Machine.ID, Account: o.From.Account,
			Label: o.From.AccountLabel, Name: o.From.Name, Handle: o.From.Handle, Mode: o.From.Mode},
		To:        peers.EnvTo{Session: to.Session, Device: *to.Device, Machine: to.MachineID, Account: to.Account},
		CreatedAt: now.UTC().Format(time.RFC3339Nano), Exp: now.Add(o.TTL).Unix()}
	if !o.HumanAt.IsZero() {
		e.From.HumanAt = o.HumanAt.UTC().Format(time.RFC3339Nano)
	}
	if o.Service != "" {
		// The trusted sender (peers.md 4.1a, 8.2): its kind and the tag; never bypass, no
		// human prompt of its own.
		e.From.Kind, e.Tag, e.From.Mode, e.From.HumanAt = o.Service, o.Tag, peers.ModePrompting, ""
	}
	doc, err := peers.SealEnvelope(e, o.Text, cert, sign)
	if err != nil {
		return nil, peers.Refuse(peers.CodeFailed, "sealing: %v", err)
	}
	ackCh := make(chan peers.PeerAck, 1)
	finCh := make(chan peers.Final, 1)
	r.mu.Lock()
	r.acks[o.Msg], r.finals[o.Msg] = ackCh, finCh
	r.mu.Unlock()
	dropFinal := func() {
		r.mu.Lock()
		if r.finals[o.Msg] == finCh {
			delete(r.finals, o.Msg)
		}
		r.mu.Unlock()
	}
	defer func() {
		r.mu.Lock()
		delete(r.acks, o.Msg)
		r.mu.Unlock()
	}()
	if err := r.send(peers.PeerMsgUp{T: link.TypePeerMsg, Doc: doc}); err != nil {
		dropFinal()
		return nil, peers.Refuse(peers.CodeHubUnreachable, "the hub cannot be reached now (%v)", err)
	}
	wait := r.AckWait
	if wait <= 0 {
		wait = AckWait
	}
	var ack peers.PeerAck
	unconfirmed := false
	select {
	case ack = <-ackCh:
	case <-time.After(wait):
		// The envelope was written to the link; the hub may still queue and deliver it, so
		// this is not "not delivered": report it queued, unconfirmed (peers.md 4.3). A late
		// final status (peer_status) can still answer a --wait.
		unconfirmed = true
		ack = peers.PeerAck{Status: "queued"}
	}
	if ack.Status != "queued" {
		dropFinal()
		return nil, hubRefusal(ack)
	}
	exp := time.Unix(e.Exp, 0)
	if ack.Exp != 0 {
		exp = time.Unix(ack.Exp, 0)
	}
	return &peers.Pending{Expires: exp, Offline: ack.Detail == "offline", Unconfirmed: unconfirmed, Wait: func(d time.Duration) (peers.Final, bool) {
		defer dropFinal()
		select {
		case f := <-finCh:
			return f, true
		case <-time.After(d):
			return peers.Final{}, false
		}
	}}, nil
}

func revocations(state string) *keys.RevocationBody {
	s, err := keys.LoadAcceptedRevocations(state)
	if err != nil || s == nil {
		return nil
	}
	return &s.Body
}

// ---- same-machine audit (peers.md 9.3, 10) ----

func (r *Remote) spoolPath() string { return filepath.Join(r.Env.State, "agent", "audit-out.jsonl") }

// Log is peers.Remote: report a same-machine delivery as peer_log, spooling it while the hub
// cannot be reached; the spool goes out with the next report that gets through.
func (r *Remote) Log(l peers.LocalLog) {
	up := peers.PeerLogUp{T: link.TypePeerLog, Msg: l.Msg, Thread: l.Thread, FromSession: l.FromSession, ToSession: l.ToSession,
		Kind: l.Kind, Status: l.Status, Detail: l.Detail, Size: l.Size, At: l.At.UTC().Format(time.RFC3339Nano),
		FromKind: l.FromKind, Tag: l.Tag}
	r.sendLog(up)
}

// sendLog sends one peer_log line, or spools it while the hub cannot be reached.
func (r *Remote) sendLog(up peers.PeerLogUp) {
	if err := r.send(up); err != nil {
		r.spool(up)
		return
	}
	r.flushSpool()
}

func (r *Remote) spool(up peers.PeerLogUp) {
	r.spoolMu.Lock()
	defer r.spoolMu.Unlock()
	b, _ := json.Marshal(up)
	if os.MkdirAll(filepath.Dir(r.spoolPath()), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(r.spoolPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// flushSpool sends the spooled peer_log lines; the ones that do not go out stay.
func (r *Remote) flushSpool() {
	r.spoolMu.Lock()
	defer r.spoolMu.Unlock()
	f, err := os.Open(r.spoolPath())
	if err != nil {
		return
	}
	var lines []peers.PeerLogUp
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var up peers.PeerLogUp
		if json.Unmarshal(sc.Bytes(), &up) == nil {
			lines = append(lines, up)
		}
	}
	f.Close()
	var left []byte
	for i, up := range lines {
		if err := r.send(up); err != nil {
			for _, l := range lines[i:] {
				b, _ := json.Marshal(l)
				left = append(append(left, b...), '\n')
			}
			break
		}
	}
	if len(left) == 0 {
		_ = os.Remove(r.spoolPath())
		return
	}
	_ = atomicfile.Write(r.spoolPath(), left, 0o600)
}

// ---- frames from the hub (linkclient.PeerHandler) ----

// Ephemeral routes the hub's answers to the waiting request.
func (r *Remote) Ephemeral(t string, body []byte) {
	switch t {
	case link.TypePeers:
		var rep peers.PeersReply
		if json.Unmarshal(body, &rep) != nil {
			return
		}
		r.mu.Lock()
		ch := r.lists[rep.ID]
		r.mu.Unlock()
		if ch != nil {
			select {
			case ch <- &rep:
			default:
			}
		}
	case link.TypePeerAck:
		var a peers.PeerAck
		if json.Unmarshal(body, &a) != nil {
			return
		}
		r.mu.Lock()
		ch := r.acks[a.Msg]
		r.mu.Unlock()
		if ch != nil {
			select {
			case ch <- a:
			default:
			}
		}
	case link.TypeNotice:
		// A hub that does not relay peer frames (or refuses them) answers with a notice
		// that names no request: every waiter learns it now instead of timing out.
		var n link.Notice
		if json.Unmarshal(body, &n) != nil || (n.Code != link.NoticeUnknownType && n.Code != link.NoticeForbidden) {
			return
		}
		r.mu.Lock()
		for _, ch := range r.lists {
			select {
			case ch <- nil:
			default:
			}
		}
		for msg, ch := range r.acks {
			select {
			case ch <- peers.PeerAck{V: 1, Msg: msg, Status: "refused", Detail: n.Code}:
			default:
			}
		}
		r.mu.Unlock()
	}
}

// Queued handles a queued peer_msg (deliver it), peer_status (a sent message's final state)
// or peer_thread_resume (the owner resumed a stopped thread). An error leaves the frame
// unacked so the hub replays it: only when the result could not be reported or the state
// could not be written.
func (r *Remote) Queued(ctx context.Context, t string, seq int64, body []byte) error {
	switch t {
	case link.TypePeerStatus:
		var s peers.PeerStatusBody
		if json.Unmarshal(body, &s) != nil || s.Msg == "" {
			return nil
		}
		at, err := time.Parse(time.RFC3339Nano, s.At)
		if err != nil {
			at = r.now()
		}
		r.Env.AuditFinal(s.Msg, s.Status, s.Detail, at)
		r.mu.Lock()
		ch := r.finals[s.Msg]
		r.mu.Unlock()
		if ch != nil {
			select {
			case ch <- peers.Final{Status: s.Status, Detail: s.Detail}:
			default:
			}
		}
		return nil
	case link.TypePeerMsg:
		return r.incoming(body)
	case link.TypePeerThreadResume:
		return r.threadResume(body)
	}
	return nil
}

// threadResume is peers.md 6.3's "Resume thread" on this machine. The link client has already
// checked the frame's hub signature and opened its seal; the hub alone signs it, so a machine
// with hub_may_widen = false refuses it, tells the hub (a peer_log thread_resume_refused), and
// the thread keeps its turns here until a human prompt.
// An error (threads.json not written) leaves the frame unacked for a replay.
func (r *Remote) threadResume(body []byte) error {
	var b peers.ThreadResumeBody
	if err := json.Unmarshal(body, &b); err != nil || !peers.ValidThreadID(b.Thread) {
		r.Logger.Printf("peers: a peer_thread_resume body does not parse; dropped")
		return nil
	}
	if !r.HubMayWiden {
		r.Logger.Printf("peers: the hub asked to resume thread %s; refused: peers.hub_may_widen is false, so the thread "+
			"stays stopped until a human prompt in either session", b.Thread)
		r.Env.RefuseResume(b.Thread, peers.ResumeRefusedReason)
		// Tell the hub, so its audit and the board show that this machine kept the stop.
		r.sendLog(peers.PeerLogUp{T: link.TypePeerLog, Event: peers.EventThreadResumeRefused, Thread: b.Thread,
			Detail: peers.ResumeRefusedReason, At: r.now().UTC().Format(time.RFC3339Nano)})
		return nil
	}
	got, err := r.Env.ResumeThread(b.Thread)
	if err != nil {
		return fmt.Errorf("resuming thread %s: %w", b.Thread, err)
	}
	if got == peers.ResumeResumed {
		r.Logger.Printf("peers: thread %s resumed by the owner (asked from %s)", b.Thread, b.By)
	} else {
		r.Logger.Printf("peers: the hub asked to resume thread %s, which is not open here; nothing to do", b.Thread)
	}
	return nil
}

// seen is <state>/peers/seen.json: message ids handled, with the result reported (8.4).
type seen struct {
	V    int                  `json:"v"`
	Msgs map[string]seenEntry `json:"msgs"`
}

type seenEntry struct {
	At     time.Time          `json:"at"`
	Result peers.PeerResultUp `json:"result"`
}

func (r *Remote) seenPath() string { return filepath.Join(r.Env.State, "peers", "seen.json") }

func (r *Remote) loadSeen() *seen {
	s := &seen{V: 1, Msgs: map[string]seenEntry{}}
	if b, err := os.ReadFile(r.seenPath()); err == nil {
		_ = json.Unmarshal(b, s)
	}
	if s.Msgs == nil {
		s.Msgs = map[string]seenEntry{}
	}
	return s
}

func (r *Remote) saveSeen(s *seen) error {
	now := r.now()
	for id, e := range s.Msgs {
		if now.Sub(e.At) > SeenFor {
			delete(s.Msgs, id)
		}
	}
	if err := os.MkdirAll(filepath.Dir(r.seenPath()), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	return atomicfile.Write(r.seenPath(), b, 0o600)
}

// incoming is 8.4 for one queued peer_msg.
func (r *Remote) incoming(body []byte) error {
	var q peers.QueuedPeerMsg
	if err := json.Unmarshal(body, &q); err != nil {
		r.Logger.Printf("peers: a peer_msg body does not parse; dropped")
		return nil
	}
	// The message id before the checks, for the replay guard and the result (the hub
	// matches the result to its row; a forged id only answers for a row of this device).
	var claimed peers.Envelope
	if raw, err := q.Doc.BodyBytes(); err == nil {
		_ = json.Unmarshal(raw, &claimed)
	}
	r.seenMu.Lock()
	defer r.seenMu.Unlock()
	sn := r.loadSeen()
	if e, ok := sn.Msgs[claimed.Msg]; ok && claimed.Msg != "" {
		// A replay: the result is reported again (it may not have reached the hub), the
		// message is not delivered twice.
		return r.report(e.Result)
	}
	now := r.now()
	res := r.check(q, now)
	res.T, res.Msg = link.TypePeerResult, claimed.Msg
	if claimed.Msg != "" {
		sn.Msgs[claimed.Msg] = seenEntry{At: now, Result: res}
		if err := r.saveSeen(sn); err != nil {
			if res.Status != "delivered" {
				// Nothing was delivered, so a replay cannot duplicate it: leave it unacked.
				return fmt.Errorf("writing %s: %w", r.seenPath(), err)
			}
			// Delivered already: leaving the frame unacked would make the hub replay it and
			// deliver it twice. Log, report and ack; the cost is that a replay after a lost
			// ack goes undetected.
			r.Logger.Printf("peers: message %s delivered, but %s could not be written (%v); acking anyway", claimed.Msg, r.seenPath(), err)
		}
	}
	if res.Status != "delivered" {
		r.Env.AuditIncoming(claimed, res.Status, res.Detail, now)
	}
	if claimed.Msg == "" {
		return nil
	}
	return r.report(res)
}

func (r *Remote) report(res peers.PeerResultUp) error {
	if err := r.send(res); err != nil {
		return fmt.Errorf("reporting the result of %s: %w", res.Msg, err)
	}
	return nil
}

// check verifies the envelope and delivers it (8.4 steps 1-4).
func (r *Remote) check(q peers.QueuedPeerMsg, now time.Time) peers.PeerResultUp {
	trust, err := keys.PinnedTrust(r.Env.State)
	if err != nil {
		return peers.PeerResultUp{Status: peers.CodeRejected, Detail: peers.ReasonUncertified}
	}
	dk, err := keys.ReadDeviceKeys(r.Env.State)
	if err != nil {
		return peers.PeerResultUp{Status: peers.CodeFailed, Detail: peers.CodeFailed}
	}
	box, err := dk.BoxKey()
	if err != nil {
		return peers.PeerResultUp{Status: peers.CodeFailed, Detail: peers.CodeFailed}
	}
	env, cert, err := peers.CheckIncoming(q.Doc, q.Cert, trust, revocations(r.Env.State), dk.Device, now)
	if err != nil {
		r.Logger.Printf("peers: rejected a message: %v", err)
		return peers.PeerResultUp{Status: peers.CodeRejected, Detail: peers.ReasonOf(err)}
	}
	if env.To.Machine != "" && env.To.Machine != r.Env.Machine.ID {
		return peers.PeerResultUp{Status: peers.CodeRejected, Detail: peers.ReasonWrongDevice}
	}
	if err := peers.CheckSenderAccount(q.Accounts, trust, cert, env.From.Account); err != nil {
		r.Logger.Printf("peers: rejected a message: %v", err)
		return peers.PeerResultUp{Status: peers.CodeRejected, Detail: peers.ReasonOf(err)}
	}
	// The receiving env names sessions with the hub's last answer (Names, the cache only).
	env2 := r.Env
	env2.Remote = r
	got := env2.Receive(peers.Incoming{Env: env, MachineName: cert.Body.Name, Open: func() (string, error) { return env.Open(box) }})
	res := peers.PeerResultUp{Status: got.Status, Detail: got.Detail}
	if !got.HumanAt.IsZero() {
		res.HumanAt = got.HumanAt.UTC().Format(time.RFC3339Nano)
	}
	return res
}
