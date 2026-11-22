package peers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SendResult is the outcome of a send (peers.md 4.3): delivered (same machine, or a remote
// message whose final status came in time) or queued at the hub.
type SendResult struct {
	Msg    string
	Thread string
	Target Session
	Sender Session
	// HeldLikely: the two permission classes differ, so Claude Code will probably hold the
	// message for the receiver's approval (peers.md 8.5). The send still counts as delivered.
	HeldLikely bool
	// Status is "delivered" or "queued" ("" means delivered).
	Status string
	// Expires and Offline describe a queued message; Pending waits for its final status.
	Expires time.Time
	Offline bool
	// Unconfirmed: the hub did not answer in time; the message is queued as far as the sender
	// can tell (it may still be delivered, and counts in the thread).
	Unconfirmed bool
	Pending     *Pending
}

// Line is the one status line `wy peers send` prints on success.
func (r SendResult) Line() string {
	if r.Status == "queued" {
		why := "not delivered yet"
		switch {
		case r.Offline:
			why = "its machine is offline"
		case r.Unconfirmed:
			why = "the hub has not confirmed it, so it may still be delivered; do not resend"
		}
		return fmt.Sprintf("queued for %s: %s; expires %s (msg %s)", r.Target.Handle, why, r.Expires.Local().Format("15:04"), r.Msg)
	}
	s := fmt.Sprintf("delivered to %s (msg %s, thread %s)", r.Target.Handle, r.Msg, r.Thread)
	if r.HeldLikely {
		s += ": the receiving session is in a different permission mode, so Claude Code may ask its user to approve it"
	}
	return s
}

// ReplySocketPath is `$XDG_RUNTIME_DIR/wy/r/<16 hex>.sock`, the first 16 hex of
// SHA-256(sender session id) (peers.md 8.6). Nothing listens there yet (8.6 is not built);
// a native SendMessage to it finds nobody, and the header's "Reply with: wy peers send"
// line is the reply path.
func ReplySocketPath(runtime, senderSession string) string {
	sum := sha256.Sum256([]byte(senderSession))
	return filepath.Join(runtimeDir(runtime), "wy", "r", hex.EncodeToString(sum[:])[:16]+".sock")
}

// Header is the first two lines of the message body (peers.md 8.5). The parts of a remote
// message come from the sender's envelope, so each is cleaned to one line (CleanName): a
// newline in a handle would add a header line of the sender's choosing.
func Header(handle, label, machine, thread string) string {
	handle, label, machine, thread = CleanName(handle), CleanName(label), CleanName(machine), CleanName(thread)
	return fmt.Sprintf("[Watchyard] from %s · account %s · machine %s · thread %s\nReply with: wy peers send %s \"…\"",
		handle, label, machine, thread, handle)
}

func (e Env) newID() string {
	if e.NewID != nil {
		return e.NewID()
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Send is SendWith with the default options.
func (e Env) Send(c Caller, to, text string) (SendResult, *Refusal) {
	return e.SendWith(c, to, text, SendOpts{})
}

// SendWith is the sender's pipeline of peers.md 4.2: the checks in order, then the write into
// a local target's inbox (8.5) or the envelope to the hub (8.2, through Env.Remote). A
// *Refusal is final: nothing is retried. The caller has been identified by Whoami; the
// directory is re-read here so a session that just ended is `target_gone`.
//
// Order: off; paused; target resolves and is live; content; rate limits; matrix; loop guard;
// delivery. The thread file is updated only on delivery (or once the hub queued the
// message), except that a rate token is taken when the rate check passes.
func (e Env) SendWith(c Caller, to, text string, o SendOpts) (SendResult, *Refusal) {
	if e.Off() {
		return SendResult{}, Refuse(CodeOff, "peers are off on this machine (wy peers on turns them back on)")
	}
	if p, ok := e.matrix().(pauser); ok && p.Paused() {
		return SendResult{}, &Refusal{Code: CodePaused, Detail: "peers are paused"}
	}
	// The hub is asked first, so the sender and the local sessions carry their canonical
	// handles (the envelope's from.handle, the header's reply line).
	dir, all, remoteErr := e.directoryAll()
	var sender *Session
	for i := range dir {
		if dir[i].Entry.SessionID == c.Entry.SessionID {
			sender = &dir[i]
		}
	}
	if sender == nil {
		return SendResult{}, Refuse(CodeNotASession, "this session's inbox socket is not answering")
	}
	target, _, rf := Resolve(all, to)
	if rf != nil {
		if rf.Code == CodeUnknownTarget && remoteErr != nil {
			return SendResult{}, Refuse(CodeHubUnreachable,
				"no session on this machine matches %q, and the hub cannot be asked for other machines' sessions now (%v)", to, remoteErr)
		}
		return SendResult{}, rf
	}
	if target.Entry.SessionID == sender.Entry.SessionID {
		return SendResult{}, Refuse(CodeUnknownTarget, "that is this session")
	}
	if target.Remote && target.Device == "" {
		return SendResult{}, Refuse(CodeUnknownTarget, "%s runs on %s, which has no wy agent (wy agent enable there)", target.Handle, target.MachineName)
	}
	o.Service, o.Tag = "", "" // a session never sends as the trusted sender
	return e.sendTo(sender, target, dir, text, o)
}

// sendTo runs the checks of 4.2 from the content on (content, TTL, rates, matrix, loop guard)
// and delivers: into a local target's inbox, or to the hub for a remote one. sender is a
// session, or the trusted sender's pseudo-session when o.Service is set (peers.md 4.1a): then
// the reply rule of 5.3 never applies, the thread must be one it opened, and the header names
// the program.
func (e Env) sendTo(sender *Session, target Session, dir []Session, text string, o SendOpts) (SendResult, *Refusal) {
	clean, rf := CheckText(text)
	if rf != nil {
		return SendResult{}, rf
	}
	ttl := o.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < MinTTL || ttl > MaxTTL {
		return SendResult{}, Refuse(CodeFailed, "the TTL must be between 1m and 24h")
	}
	unlock := lockState(e.State)
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	now := e.now()
	rates := LoadRates(e.RatesPath())
	if wait, ok := CheckRate(rates, now, sender.Entry.SessionID, PairKey(sender.Entry.SessionID, target.Entry.SessionID)); !ok {
		return SendResult{}, Refuse(CodeRateLimited, "too many messages; retry in %s", wait.Round(time.Second))
	}
	rates.Prune(now)
	_ = rates.Save(e.RatesPath())

	m := e.matrix()
	threads := LoadThreads(e.ThreadsPath())
	live := map[string]bool{}
	for _, s := range dir {
		live[s.Entry.SessionID] = true
	}
	threads.Expire(now, func(id string) bool { return live[id] })
	th := threads.FindOpen(sender.Entry.SessionID, target.Entry.SessionID)
	mth := th
	if o.Service != "" {
		// Doorbell never replies into a thread: only a start the matrix allows as such.
		if th != nil && th.A != sender.Entry.SessionID {
			return SendResult{}, Refuse(CodeNotAllowed, "Doorbell never replies into a thread another session opened")
		}
		mth = nil
	}
	if d := m.Allowed(party(*sender), party(target), mth); !d.Allowed {
		reason := d.Reason
		if reason == "" {
			reason = CodeNotAllowed
		}
		return SendResult{}, &Refusal{Code: reason, Detail: d.Detail}
	}
	if th == nil {
		th = threads.Start(sender.Entry.SessionID, target.Entry.SessionID, sender.Account, target.Account, now)
		if target.Remote {
			th.Remote = target.Entry.SessionID
		}
	}
	maxTurns := m.MaxTurns()
	if LoopGuard(th, newestHuman(sender, &target), maxTurns, now) {
		_ = threads.Save(e.ThreadsPath()) // keeps paused_at
		return SendResult{}, Refuse(CodeLoopGuard, "%d automated turns without a human prompt in either session; paused until you type in one of them", maxTurns)
	}
	msg := e.newID()
	kind := th.KindFor(sender.Entry.SessionID)
	if target.Remote {
		return e.sendRemote(&unlock, Outgoing{Msg: msg, Thread: th.ID, Kind: kind, From: *sender, To: target,
			HumanAt: newestHuman(sender, sender), Text: clean, TTL: ttl, Now: now, Service: o.Service, Tag: o.Tag}, *th)
	}

	from, err := UDSAddress(ReplySocketPath(e.Runtime, sender.Entry.SessionID))
	if err != nil {
		return SendResult{}, Refuse(CodeFailed, "%v", err)
	}
	header := Header(sender.Handle, sender.AccountLabel, e.Machine.Name, th.ID)
	if o.Service != "" {
		header = ServiceHeader(e.Machine.Name, sender.AccountLabel, o.Tag, th.ID)
	}
	w := Wrapper{
		From: from, FromSession: sender.Entry.SessionID, FromName: sender.Handle, FromMode: sender.Mode,
		Body: header + "\n\n" + clean,
	}
	frame := NewFrame(msg, target.Entry.SessionID, w)
	if err := WriteFrame(target.Entry.SocketOf(e.Runtime), frame); err != nil {
		code := CodeFailed
		if errors.Is(err, ErrTargetGone) {
			code = CodeTargetGone
		}
		return SendResult{}, Refuse(code, "%v", err)
	}
	th.RecordMessageFrom(sender.Entry.SessionID, now)
	_ = threads.Save(e.ThreadsPath())
	_ = MarkDelivered(e.State, target.Entry.SessionID, now)
	e.audit(auditLine{Msg: msg, Thread: th.ID, From: sender.Entry.SessionID, To: target.Entry.SessionID, FromA: sender.Account,
		ToA: target.Account, Size: len(clean), Local: true, Status: "delivered", FromKind: o.Service, Tag: o.Tag}, now)
	if e.Remote != nil {
		e.Remote.Log(LocalLog{Msg: msg, Thread: th.ID, FromSession: sender.Entry.SessionID, ToSession: target.Entry.SessionID,
			Kind: kind, Status: "delivered", Size: len(clean), At: now, FromKind: o.Service, Tag: o.Tag})
	}
	return SendResult{Msg: msg, Thread: th.ID, Target: target, Sender: *sender, HeldLikely: sender.Mode != target.Mode}, nil
}

// sendRemote hands a checked message to the hub. The state lock is released while the hub
// answers (a message from the other machine may be waiting for it on this link) and taken
// again to count the message in its thread once the hub queued it.
func (e Env) sendRemote(unlock *func(), out Outgoing, thread Thread) (SendResult, *Refusal) {
	(*unlock)()
	*unlock = nil
	p, rf := e.Remote.Send(out)
	*unlock = lockState(e.State)
	line := auditLine{Msg: out.Msg, Thread: thread.ID, From: out.From.Entry.SessionID, To: out.To.Entry.SessionID,
		FromA: out.From.Account, ToA: out.To.Account, Size: len(out.Text), FromKind: out.Service, Tag: out.Tag}
	if rf != nil {
		line.Status, line.Detail = "refused", rf.Code
		e.audit(line, out.Now)
		return SendResult{}, rf
	}
	ts := LoadThreads(e.ThreadsPath())
	th := ts.FindOpen(out.From.Entry.SessionID, out.To.Entry.SessionID)
	switch {
	case th == nil:
		th = ts.Adopt(thread.ID, thread.A, thread.B, thread.AAccount, thread.BAccount, out.To.Entry.SessionID, thread.OpenedAt)
		th.HumanAt, th.Turns = thread.HumanAt, thread.Turns
	case thread.HumanAt.After(th.HumanAt):
		// The loop guard reset the count for a new human prompt on the copy it checked.
		th.HumanAt, th.Turns, th.PausedAt = thread.HumanAt, 0, nil
	}
	th.RecordMessageFrom(out.From.Entry.SessionID, out.Now)
	_ = ts.Save(e.ThreadsPath())
	line.Status = "queued"
	e.audit(line, out.Now)
	return SendResult{Msg: out.Msg, Thread: thread.ID, Target: out.To, Sender: out.From, Status: "queued", Expires: p.Expires,
		Offline: p.Offline, Unconfirmed: p.Unconfirmed, Pending: p}, nil
}

func newestHuman(a, b *Session) time.Time {
	var t time.Time
	for _, s := range []Session{*a, *b} {
		if s.HasFile && s.File.HumanAt != nil && s.File.HumanAt.After(t) {
			t = *s.File.HumanAt
		}
	}
	return t
}

// auditLine is one line of <state>/peers/audit.jsonl: ids, sizes and statuses, never text
// (peers.md 10). A message to another machine gets a line when the hub queued (or refused)
// it and one with its final status; a message from another machine one when it is handled.
type auditLine struct {
	TS     string `json:"ts"`
	Msg    string `json:"msg"`
	Thread string `json:"thread,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	FromA  string `json:"from_account,omitempty"`
	ToA    string `json:"to_account,omitempty"`
	Size   int    `json:"size,omitempty"`
	Local  bool   `json:"local"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	// FromKind is "service:doorbell" for the trusted sender (peers.md 4.1a, 10); Tag the tag
	// it sent to.
	FromKind string `json:"from_kind,omitempty"`
	Tag      string `json:"tag,omitempty"`
}

func (e Env) audit(l auditLine, now time.Time) {
	l.TS = now.UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(l)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(e.AuditPath()), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(e.AuditPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// AuditFinal records the final status of a message this machine sent to another machine
// (peer_status, link.md 6.4).
func (e Env) AuditFinal(msg, status, detail string, at time.Time) {
	e.audit(auditLine{Msg: msg, Status: status, Detail: detail}, at)
}

// Describe turns a refusal into the one line `wy peers send` prints (peers.md 4.3).
func Describe(r *Refusal) string {
	switch r.Code {
	case CodeNotAllowed:
		return "not delivered: " + r.Detail + ". Ask the owner to change it; do not retry."
	case CodePaused:
		return "not delivered: peers are paused for every machine. Ask the owner to resume them; do not retry."
	case CodeNoMatrix:
		return "not delivered: the matrix is not configured on this machine, so nothing is allowed yet. Ask the owner to set it up; do not retry."
	case CodeOff:
		return "not delivered: " + r.Detail + "."
	case CodeLoopGuard:
		return "not delivered: " + r.Detail + "."
	case CodeRateLimited:
		return "not delivered: " + r.Detail + "."
	case CodeSecret:
		return "not delivered: " + r.Detail + ". Remove it and send again only if the user asks."
	case CodeAmbiguous:
		return "not delivered: " + r.Detail + ". Pick one handle and send again."
	}
	return "not delivered: " + strings.TrimPrefix(r.Detail, r.Code+": ")
}
