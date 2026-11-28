package peers

import (
	"errors"
	"slices"
	"time"
)

// Incoming is a message from another machine whose envelope and sender certificate checked
// out (CheckIncoming): what the receiving agent delivers (peers.md 8.4 steps 2-6).
type Incoming struct {
	Env Envelope
	// MachineName is the sending machine's name from its certificate, for the header line.
	MachineName string
	// Open decrypts the sealed text (Envelope.Open with this machine's box key).
	Open func() (string, error)
}

// ReceiveResult is what the receiver reports in peer_result (link.md 6.4): delivered,
// rejected (with a reason word), target_gone or failed. HumanAt is the newest human prompt
// in the target session, so the hub and the sender can reset their loop guards.
type ReceiveResult struct {
	Status, Detail string
	HumanAt        time.Time
}

// Receive applies the receiver's own checks to a remote message and writes it into the
// target's inbox. The receiver trusts neither the hub nor the sender: it checks the matrix
// it accepted itself, its own thread state and loop guard, and the content rules again.
func (e Env) Receive(in Incoming) ReceiveResult {
	env := in.Env
	reject := func(reason string) ReceiveResult { return ReceiveResult{Status: CodeRejected, Detail: reason} }
	if e.Off() {
		return reject(CodeOff)
	}
	unlock := lockState(e.State)
	defer unlock()
	now := e.now()
	dir := e.Directory()
	var target *Session
	live := map[string]bool{}
	for i := range dir {
		live[dir[i].Entry.SessionID] = true
		if dir[i].Entry.SessionID == env.To.Session {
			target = &dir[i]
		}
	}
	if target == nil {
		return ReceiveResult{Status: CodeTargetGone, Detail: CodeTargetGone}
	}
	res := ReceiveResult{HumanAt: newestHuman(target, target)}
	// Addressed under an account the session left (the sender's directory has not seen the
	// move, made by wy move or by hand): still this session.
	if env.To.Account != "" && target.Account != "" && env.To.Account != target.Account && !slices.Contains(target.PastAccounts, env.To.Account) {
		res.Status, res.Detail = CodeRejected, "account_mismatch"
		return res
	}
	from := Party{Session: env.From.Session, Account: env.From.Account, Label: env.From.Label}
	threads := LoadThreads(e.ThreadsPath())
	threads.Expire(now, func(id string) bool { return live[id] })
	th := threads.FindOpen(env.From.Session, target.Entry.SessionID)
	m := e.matrix()
	mth := th
	if env.Service() {
		// The trusted sender (peers.md 4.1a, 8.4): never a reply, so the reply rule never lets
		// it through, and a thread it did not open is refused.
		if th != nil && th.A != env.From.Session {
			res.Status, res.Detail = CodeRejected, CodeNotAllowed
			return res
		}
		mth = nil
	}
	if d := m.Allowed(from, party(*target), mth); !d.Allowed {
		res.Status, res.Detail = CodeRejected, d.Reason
		if res.Detail == "" {
			res.Detail = CodeNotAllowed
		}
		return res
	}
	mode := env.From.Mode
	if env.Service() {
		// The trusted sender is framed in the receiving class (peers.md 4.1a, 8.4 step 3); when
		// that is not known nothing is written, so the sender never counts a hold as delivered.
		if !ModeKnown(target.File, target.HasFile) {
			res.Status, res.Detail = CodeRejected, CodeHeld
			return res
		}
		mode = target.Mode
	}
	if th == nil {
		th = threads.Adopt(env.Thread, env.From.Session, target.Entry.SessionID, env.From.Account, target.Account, env.From.Session, now)
	}
	human := res.HumanAt
	if t, err := time.Parse(time.RFC3339Nano, env.From.HumanAt); err == nil && t.After(human) {
		human = t
	}
	if LoopGuard(th, human, m.MaxTurns(), now) {
		_ = threads.Save(e.ThreadsPath())
		res.Status, res.Detail = CodeRejected, CodeLoopGuard
		return res
	}
	text, err := in.Open()
	if err != nil {
		res.Status, res.Detail = CodeRejected, ReasonBadSignature
		return res
	}
	clean, rf := CheckText(text)
	if rf != nil {
		res.Status, res.Detail = CodeRejected, rf.Code
		return res
	}
	addr, err := UDSAddress(ReplySocketPath(e.Runtime, env.From.Session))
	if err != nil {
		res.Status, res.Detail = CodeFailed, CodeFailed
		return res
	}
	handle, label := env.From.Handle, env.From.Label
	if label == "" {
		label = env.From.Account
	}
	// The reply line must name the sender the way this machine resolves it: the hub's
	// canonical handle when the last directory answer has the sender (a sender older than
	// canonical handles signs its own machine's label), else the envelope's, which a current
	// sender takes from the same hub (peers.md 3.2, 8.5). The cache only: asking the hub here
	// would wait on the link this message arrived on.
	if e.Remote != nil {
		if n, ok := e.Remote.Names()[env.From.Session]; ok && !n.Mine && n.Handle != "" {
			handle = n.Handle
			if n.Label != "" {
				label = n.Label
			}
		}
	}
	header := Header(handle, label, in.MachineName, th.ID)
	if env.Service() {
		handle = ServiceName(in.MachineName)
		header = ServiceHeader(in.MachineName, label, env.Tag, th.ID)
	}
	w := Wrapper{
		From: addr, FromSession: env.From.Session, Hop: HopTokens(env.Hop), FromName: handle, FromMode: mode,
		Body: header + "\n\n" + clean,
	}
	if _, err := e.writeTo(*target, NewFrame(env.Msg, target.Entry.SessionID, w), w); err != nil {
		res.Status, res.Detail = CodeFailed, CodeFailed
		if errors.Is(err, ErrTargetGone) {
			res.Status, res.Detail = CodeTargetGone, CodeTargetGone
		}
		return res
	}
	th.RecordMessageFrom(env.From.Session, now)
	_ = threads.Save(e.ThreadsPath())
	_ = MarkDelivered(e.State, target.Entry.SessionID, now)
	e.audit(auditLine{Msg: env.Msg, Thread: th.ID, From: env.From.Session, To: target.Entry.SessionID, FromA: env.From.Account,
		ToA: target.Account, Size: len(clean), Status: "delivered", FromKind: env.From.Kind, Tag: env.Tag}, now)
	res.Status = "delivered"
	if mode != "" && mode != target.Mode {
		res.Detail = "held_likely"
	}
	return res
}

// Outcomes of a thread resume on this machine (peers.md 6.3), as audit statuses.
const (
	ResumeResumed = "resumed"
	ResumeRefused = "refused"
	ResumeUnknown = "unknown_thread"
)

// ResumeThread applies an owner's "Resume thread" that the hub relayed (peers.md 6.3): an open
// thread's turn count goes back to 0 and its pause is cleared, as a human prompt would do. A
// thread this machine does not know, or that is closed, is left alone (ResumeUnknown). The
// caller decides first whether this machine takes a hub-signed resume at all
// (hub_may_widen); RefuseResume records a refusal. An error means threads.json could not be
// written.
func (e Env) ResumeThread(id string) (string, error) {
	unlock := lockState(e.State)
	defer unlock()
	now := e.now()
	threads := LoadThreads(e.ThreadsPath())
	th := threads.Find(id)
	if th == nil || !th.Open() || now.Sub(th.LastAt) > ThreadIdle {
		e.audit(auditLine{Thread: id, Status: ResumeUnknown, Detail: "thread_resume"}, now)
		return ResumeUnknown, nil
	}
	th.Resume()
	if err := threads.Save(e.ThreadsPath()); err != nil {
		return "", err
	}
	e.audit(auditLine{Thread: id, From: th.A, To: th.B, FromA: th.AAccount, ToA: th.BAccount, Status: ResumeResumed,
		Detail: "thread_resume"}, now)
	return ResumeResumed, nil
}

// RefuseResume records a relayed thread resume this machine did not take (reason, e.g.
// "hub_may_widen"); the thread keeps its turns until a human prompt.
func (e Env) RefuseResume(id, reason string) {
	e.audit(auditLine{Thread: id, Status: ResumeRefused, Detail: "thread_resume: " + reason}, e.now())
}

// AuditIncoming records a remote message this machine did not deliver (peers.md 10).
func (e Env) AuditIncoming(env Envelope, status, detail string, at time.Time) {
	e.audit(auditLine{Msg: env.Msg, Thread: env.Thread, From: env.From.Session, To: env.To.Session, FromA: env.From.Account,
		ToA: env.To.Account, Size: env.Size, Status: status, Detail: detail}, at)
}
