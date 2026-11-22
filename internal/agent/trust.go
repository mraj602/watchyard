package agent

import (
	"time"

	"github.com/mraj602/watchyard/internal/peers"
)

// trustReporter is the link's half of the trusted sender (peerlink.Remote.ReportTrust).
type trustReporter interface{ ReportTrust() error }

// trust is the `trust` op (peers.md 4.1a): approve, revoke or show the trusted sender. It runs
// for a terminal only: a caller with any Claude Code-looking ancestor is refused, read from
// the peer credentials (pid), never from the request. The trusttest build turns that guard off
// (peers.TrustGuardOff) and says so in every answer.
func (s *server) trust(env peers.Env, req Request, pid int) Reply {
	if _, ok := env.Proc.(peers.ProcDetail); !ok {
		return fail(peers.CodeUnsupported, "not supported on this OS yet")
	}
	warn := peers.TrustTestWarning
	if req.TrustOp != TrustShow && !peers.TrustGuardOff {
		if c := peers.ClaudeCandidates(env.Proc, pid); len(c) > 0 {
			s.logf("trust %s refused: claude pid %d above the caller", req.TrustOp, c[0].PID)
			return fail(peers.CodeNotASession, "wy peers trust runs at a terminal that Claude Code did not start (a Claude Code process, pid %d, is above it)", c[0].PID)
		}
	}
	report := func() {
		if tr, ok := env.Remote.(trustReporter); ok && env.Remote != nil {
			if err := tr.ReportTrust(); err != nil {
				s.logf("trust: the hub was not told yet (%v); the next directory read tells it", err)
			}
		}
	}
	switch req.TrustOp {
	case TrustShow:
		rec, err := env.LoadTrust()
		if err != nil {
			return fail(peers.CodeFailed, "%v", err)
		}
		return Reply{OK: true, Trust: rec}
	case TrustSet:
		rec, err := env.ApproveDoorbell(req.Program, req.Account)
		if err != nil {
			return fail(peers.CodeFailed, "%v", err)
		}
		s.logf("trust set sender=doorbell venv=%s account=%s", rec.Venv, rec.Account)
		report()
		return Reply{OK: true, Trust: &rec, Warning: warn}
	case TrustOff:
		had, err := env.RevokeDoorbell("cli")
		if err != nil {
			return fail(peers.CodeFailed, "%v", err)
		}
		s.logf("trust off sender=doorbell had=%v", had)
		report()
		return Reply{OK: had, Warning: warn}
	}
	return fail(peers.CodeUsage, "unknown trust op %q", req.TrustOp)
}

// serviceSend is a send by the trusted sender (peers.md 4.1a). The caller is identified from
// the peer credentials and /proc only (Env.TrustedSender): a session sends as itself through
// `send`, and anything that is neither a session nor the approved Doorbell is refused
// not_a_session.
func (s *server) serviceSend(env peers.Env, req Request) Reply {
	if c, rf := env.Whoami(); rf == nil {
		return fail(peers.CodeFailed, "this command runs inside a Claude Code session (pid %d), which sends as itself", c.ClaudePID)
	}
	rec, rf := env.TrustedSender()
	if rf != nil {
		s.logf("service_send refused: %s", rf.Detail)
		return Reply{Error: &Error{Code: rf.Code, Detail: rf.Detail}}
	}
	if !req.Stdin {
		return fail(peers.CodeUsage, "Doorbell sends its text on stdin: wy peers send tag:<tag> --stdin")
	}
	lines, rf := env.SendService(rec, req.To, req.Text, req.One, req.SameHost, peers.SendOpts{TTL: time.Duration(req.TTLs) * time.Second})
	if rf != nil {
		s.logf("send from=%s kind=%s to=%s status=%s size=%d", peers.ServiceSessionID(env.Machine.ID), peers.ServiceDoorbell, req.To, rf.Code, len(req.Text))
		return Reply{Error: &Error{Code: rf.Code, Detail: rf.Detail}}
	}
	out := Reply{}
	for _, l := range lines {
		var r Reply
		switch {
		case l.Refusal != nil:
			r = Reply{To: l.Target.Handle, Error: &Error{Code: l.Refusal.Code, Detail: l.Refusal.Detail}}
			s.logf("send from=%s kind=%s tag=%s to=%s status=%s", peers.ServiceSessionID(env.Machine.ID), peers.ServiceDoorbell, tagOf(req.To), l.Target.Session, l.Refusal.Code)
		case l.Res.Status == "queued":
			r = s.waitFinal(req, l.Res)
		default:
			r = Reply{Msg: l.Res.Msg, Thread: l.Res.Thread, Status: "delivered", To: l.Res.Target.Handle, HeldLikely: l.Res.HeldLikely}
			s.logf("send msg=%s thread=%s from=%s kind=%s tag=%s to=%s status=delivered size=%d", l.Res.Msg, l.Res.Thread,
				peers.ServiceSessionID(env.Machine.ID), peers.ServiceDoorbell, tagOf(req.To), l.Res.Target.Entry.SessionID, len(req.Text))
		}
		if r.To == "" {
			r.To = l.Target.Handle
		}
		out.Lines = append(out.Lines, r)
	}
	out.Exit = linesExit(out.Lines, req.One)
	return out
}

func tagOf(to string) string {
	t, _, _ := peers.ParseTagAddress(to)
	return t
}

// linesExit is a tag send's exit code (peers.md 3.4): with one, that of the line it ended on;
// a broadcast is 0 when every line is delivered, else the code of the first that is not (8
// for queued).
func linesExit(lines []Reply, one bool) int {
	code := 0
	for _, l := range lines {
		c := 0
		switch {
		case l.Error != nil:
			c = peers.ExitCode(l.Error.Code)
		case l.Status == "queued":
			c = peers.ExitQueued
		}
		if one {
			code = c
			continue
		}
		if code == 0 {
			code = c
		}
	}
	return code
}
