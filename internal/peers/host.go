package peers

import "time"

// AgentHost lets a coding agent other than Claude Code take part in peers (docs/grok.md).
// Its sessions are listed beside Claude's, are subject to the same matrix, thread and loop
// rules, and receive a message through Deliver instead of an inbox socket.
type AgentHost interface {
	// Sessions are the live sessions the host knows on this machine.
	Sessions() []HostedSession
	// Deliver hands text to session id: it is injected at once when the session is idle and
	// queued behind the running turn when it is busy (queued reports which).
	Deliver(id, text string) (queued bool, err error)
}

// HostedSession is one session of an AgentHost.
type HostedSession struct {
	ID      string
	Name    string
	Dir     string // working folder
	Status  string // "idle" or "busy"
	Account string // the pseudo-account the matrix names it by (e.g. "grok")
	Label   string // its display label (e.g. "grok")
	Bypass  bool   // its tools run without asking, like a bypassPermissions Claude session
	Last    time.Time
}

// hostSessions lists every host's sessions as directory entries.
func (e Env) hostSessions() []Session {
	var out []Session
	for _, h := range e.Hosts {
		for _, hs := range h.Sessions() {
			if !safeID.MatchString(hs.ID) {
				continue
			}
			mode := ModePrompting
			if hs.Bypass {
				mode = ModeBypass
			}
			ent := RegistryEntry{SessionID: hs.ID, CWD: hs.Dir, Name: hs.Name, Status: hs.Status, Kind: "host"}
			s := Session{Entry: ent, Account: hs.Account, AccountLabel: hs.Label, Project: e.tildeDir(hs.Dir),
				MachineName: e.Machine.Name, Mode: mode, Host: h}
			s.Name = HandlePart(hs.Name)
			if s.Name == "" {
				s.Name = HandlePart(hs.Label) + "-" + hs.ID[:min(8, len(hs.ID))]
			}
			out = append(out, s)
		}
	}
	return out
}

// HostAccounts maps each host's pseudo-account to its label, for the matrix commands.
func (e Env) HostAccounts() map[string]string {
	out := map[string]string{}
	for _, h := range e.Hosts {
		for _, hs := range h.Sessions() {
			if hs.Account != "" {
				out[hs.Account] = hs.Label
			}
		}
	}
	return out
}

// writeTo delivers a frame to a local target: its inbox socket, or its host's Deliver.
// queued is true when a host held the message behind a running turn.
func (e Env) writeTo(target Session, f Frame, w Wrapper) (queued bool, err error) {
	if target.Host != nil {
		return target.Host.Deliver(target.Entry.SessionID, w.String())
	}
	return false, WriteFrame(target.Entry.SocketOf(e.Runtime), f)
}
