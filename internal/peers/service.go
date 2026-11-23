package peers

import "strings"

// The trusted sender's sends (peers.md 4.1a): tag addresses only, as the recorded account,
// through the same checks as any send (4.2), from the pseudo-session ServiceSessionID.

// ServiceLine is the outcome of a trusted send to one holder: Res, or Refusal.
type ServiceLine struct {
	Target  ListEntry
	Res     SendResult
	Refusal *Refusal
}

// DoorbellHandleRefusal is the refusal of a trusted send to anything but a tag.
const DoorbellHandleRefusal = "Doorbell may send only to a tag (wy peers send tag:<tag> --stdin), never to a session by name"

// serviceSender is the trusted sender as the sending side of a message: its pseudo-session,
// the recorded account with the label every machine shows for it (the hub's canonical label
// when the hub named any session of that account, else this machine's). Its Mode is prompting
// only for the directory's view and the wire (peers.md 8.2): each frame it sends carries the
// receiving session's class instead (sendTo, Receive; 4.1a).
func (e Env) serviceSender(rec TrustRecord, all []Session) Session {
	label := ""
	if e.Remote != nil {
		for _, n := range e.Remote.Names() {
			if n.Account == rec.Account && n.Label != "" {
				label = n.Label
				break
			}
		}
	}
	if label == "" {
		for _, s := range all {
			if !s.Remote && s.Account == rec.Account && s.AccountLabel != "" {
				label = s.AccountLabel
				break
			}
		}
	}
	if label == "" {
		label = rec.AccountLabel
	}
	if label == "" {
		label = CanonicalLabel("", rec.Account)
	}
	return Session{Entry: RegistryEntry{SessionID: ServiceSessionID(e.Machine.ID)}, Account: rec.Account, AccountLabel: label,
		Handle: ServiceName(e.Machine.Name), Name: DoorbellScript, Mode: ModePrompting, MachineName: e.Machine.Name}
}

// SendService is a trusted send (peers.md 4.1a) by rec, which TrustedSender returned for the
// caller: to must be a tag address; the targets are the holders the recorded account may start
// a thread with (not blocked; --same-host keeps this machine's), in TagHolders order. Broadcast
// sends to each; one delivers to the first and passes the message on only when that holder has
// gone. A refusal of the whole send (not a tag, no hub, no holder, bad content, the approval
// revoked meanwhile) comes back as rf; per-holder outcomes as lines.
func (e Env) SendService(rec TrustRecord, to, text string, one, sameHost bool, o SendOpts) (lines []ServiceLine, rf *Refusal) {
	tag, isTag, bad := ParseTagAddress(strings.TrimSpace(to))
	if bad != nil {
		return nil, bad
	}
	if !isTag {
		return nil, Refuse(CodeNotAllowed, "%s", DoorbellHandleRefusal)
	}
	if e.Off() {
		return nil, Refuse(CodeOff, "peers are off on this machine (wy peers on turns them back on)")
	}
	if p, ok := e.matrix().(pauser); ok && p.Paused() {
		return nil, &Refusal{Code: CodePaused, Detail: "peers are paused"}
	}
	if _, rf := CheckText(text); rf != nil {
		return nil, rf
	}
	if e.Remote == nil {
		return nil, Refuse(CodeHubUnreachable, "tags are held at the hub and need the wy agent's link to it, so nothing was sent to tag %s", tag)
	}
	// Asked now, past the cache: the hub's answer carries this machine's trust state, and a
	// revocation in it deletes the record before the record is read again below.
	dir, all, remoteErr := e.directoryWith(true)
	if remoteErr != nil {
		return nil, Refuse(CodeHubUnreachable, "tags are held at the hub, which cannot be reached now, so nothing was sent to tag %s", tag)
	}
	if cur, err := e.LoadTrust(); err != nil || cur == nil || !SameApproval(rec, *cur) {
		return nil, Refuse(CodeNotASession, "not inside a Claude Code session, and not the approved Doorbell: its approval was revoked or replaced")
	}
	svc := e.serviceSender(rec, all)
	var targets []ListEntry
	for _, le := range TagTargets(e.Entries(all, &svc, false), tag, sameHost) {
		if le.Can == "start" {
			targets = append(targets, le)
		}
	}
	if len(targets) == 0 {
		where := ""
		if sameHost {
			where = " on this machine"
		}
		return nil, Refuse(CodeNoHolder, "no session %s may message%s holds tag %s", svc.AccountLabel, where, tag)
	}
	o.Service, o.Tag = ServiceDoorbell, tag
	for _, t := range targets {
		var target *Session
		for i := range all {
			if all[i].Entry.SessionID == t.Session {
				target = &all[i]
			}
		}
		line := ServiceLine{Target: t}
		if target == nil {
			line.Refusal = Refuse(CodeTargetGone, "%s has ended", t.Handle)
		} else {
			line.Res, line.Refusal = e.sendTo(&svc, *target, dir, text, o)
		}
		lines = append(lines, line)
		if !one {
			continue
		}
		if line.Refusal != nil && (line.Refusal.Code == CodeTargetGone || line.Refusal.Code == CodeUnknownTarget) {
			continue // that holder has gone: the next one
		}
		break
	}
	return lines, nil
}
