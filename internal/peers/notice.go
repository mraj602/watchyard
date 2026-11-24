package peers

import "strings"

// NoticeSession is the from-session of a notice: Watchyard itself, not a session.
const NoticeSession = "watchyard"

// NoticeHeader is the first line of a notice's body (peers.md 8.7).
func NoticeHeader(machine string) string {
	return "[Watchyard] notice · machine " + CleanName(machine) + " · information from Watchyard itself, not from a session: no reply needed"
}

// Notice writes text, a short notice from this machine's Watchyard itself, into the inbox of
// the local session sessionID (peers.md 8.7): the news that a session it started was moved to
// another account (setup.md 8.6, Who is told). It is not a message between sessions: no
// thread, no rate or loop count, no matrix check, because it carries no other session's
// words; but peers must be on, and only a live local session in a managed folder is written
// to. The frame carries the target's own permission class, so Claude Code does not hold it
// for approval. It returns the target's handle.
func (e Env) Notice(sessionID, text string) (string, *Refusal) {
	if e.Off() {
		return "", Refuse(CodeOff, "peers are off on this machine")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", Refuse(CodeNotText, "empty notice")
	}
	var target *Session
	dir := e.Directory()
	for i := range dir {
		if dir[i].Entry.SessionID == sessionID {
			target = &dir[i]
		}
	}
	if target == nil {
		return "", Refuse(CodeTargetGone, "session %s is not running on this machine", sessionID)
	}
	from, err := UDSAddress(ReplySocketPath(e.Runtime, NoticeSession))
	if err != nil {
		return "", Refuse(CodeFailed, "%v", err)
	}
	w := Wrapper{From: from, FromSession: NoticeSession, FromName: "watchyard", FromMode: target.Mode,
		Body: NoticeHeader(e.Machine.Name) + "\n\n" + text}
	msg := e.newID()
	now := e.now()
	if err := WriteFrame(target.Entry.SocketOf(e.Runtime), NewFrame(msg, sessionID, w)); err != nil {
		return target.Handle, Refuse(CodeTargetGone, "%v", err)
	}
	e.audit(auditLine{Msg: msg, From: NoticeSession, To: sessionID, ToA: target.Account, Size: len(text), Local: true,
		Status: "notice"}, now)
	return target.Handle, nil
}
