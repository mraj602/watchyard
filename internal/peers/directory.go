package peers

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Session is one live session in the directory (peers.md 3.1): of this machine (the local
// source), or of another machine from the hub's directory (Remote, through Env.Remote).
type Session struct {
	Entry        RegistryEntry
	Account      string
	AccountLabel string
	Handle       string
	// Aliases are other handles that name the session too (peers.md 3.3): the ones with an
	// account label other than the canonical one that a machine shows for it.
	Aliases     []string
	Name        string
	Project     string
	Mode        Mode // from the session file; prompting when there is none
	File        SessionFile
	HasFile     bool
	MachineName string
	// Remote: the session runs on another machine; Device is that machine's agent device
	// ("" when it runs none, so it cannot be reached) and State the hub's session state.
	Remote bool
	Device string
	State  string
	// Blocked is why messages to this remote session cannot go now although the matrix may
	// allow them (Reachability: certificates; "" when they can).
	Blocked string
	// Tags are the session's tags at the hub (peers.md 3.4), sorted; none without a hub.
	Tags []string
}

// ListEntry is one line of `wy peers list --json` (peers.md 3.2).
type ListEntry struct {
	Session string `json:"session"`
	Handle  string `json:"handle"`
	// Aliases: other handles of the same session that wy peers send accepts (peers.md 3.3).
	Aliases      []string `json:"aliases,omitempty"`
	Name         string   `json:"name"`
	Account      string   `json:"account"`
	AccountLabel string   `json:"account_label"`
	Machine      string   `json:"machine"`
	Project      string   `json:"project"`
	State        string   `json:"state"`
	Local        bool     `json:"local"`
	Can          string   `json:"can"` // start | reply | none
	Device       *string  `json:"device"`
	// MayAsk marks a pair whose delivery Claude Code will likely hold for approval because
	// the two sessions' permission classes differ (peers.md 8.5).
	MayAsk bool `json:"may_ask_approval,omitempty"`
	// Blocked is why a session on another machine cannot be reached now although `can`
	// says the matrix allows it (link keys not owner-certified, a revoked machine; peers.md
	// 3.2). A send to it is refused with the same reason and the fix.
	Blocked string `json:"blocked,omitempty"`
	// Tags are the session's tags (peers.md 3.4), omitted when none.
	Tags []string `json:"tags,omitempty"`
	// SameHost: the session runs on this machine under this OS user (what --same-host keeps).
	SameHost bool `json:"same_host,omitempty"`
}

// AccountLabel is the label shown for an account on this machine: the slots.labels entry for
// the slot, else the folder's name without its dot ("claude-2"). With a hub, the hub's seat
// label wins for the handle and this one stays an alias (nameLocal).
func (e Env) accountLabel(slotName string) string {
	if l := HandlePart(e.SlotLabels[slotName]); l != "" {
		return l
	}
	return HandlePart(strings.TrimPrefix(filepath.Base(slotName), "."))
}

// Directory lists the live local sessions: every managed slot's registry entries whose pid
// is alive with the same start time and whose socket accepts a connection (peers.md 3.1).
// Handles are `<name>@<account label>.<machine name>`, made unique by appending -2, -3 to
// the name part (so the `@label.machine` suffix stays addressable).
func (e Env) Directory() []Session {
	var out []Session
	login := e.loginReader()
	for _, slot := range e.Slots() {
		label := e.accountLabel(slot.Name)
		acct := ""
		if l, err := login(slot); err == nil && l.HasSeat {
			acct = l.Seat.Account
		}
		for _, ent := range ReadRegistry(e.fs(), slot) {
			if e.Proc != nil {
				live, err := e.Proc.StartTime(ent.PID)
				if err != nil || !ProcStartMatches(ent.ProcStartString(), live) {
					continue
				}
			}
			if !e.alive(ent.SocketOf(e.Runtime)) {
				continue
			}
			s := Session{Entry: ent, Account: acct, AccountLabel: label, Project: e.tildeDir(ent.CWD), MachineName: e.Machine.Name}
			s.Name = HandlePart(ent.Name)
			if s.Name == "" {
				s.Name = HandlePart(filepath.Base(ent.CWD))
			}
			if s.Name == "" || s.Name == "." || s.Name == "/" {
				s.Name = "session-" + strconv.Itoa(ent.PID)
			}
			s.File, s.HasFile = ReadSessionFile(e.State, ent.SessionID)
			s.Mode = ModeClass(s.File, s.HasFile)
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Entry, out[j].Entry
		if a.Slot.Dir != b.Slot.Dir {
			return a.Slot.Dir < b.Slot.Dir
		}
		return a.PID < b.PID
	})
	e.nameLocal(out)
	return out
}

// nameLocal gives the local sessions their handles (peers.md 3.2). A session the hub named
// takes the hub's canonical handle, so this machine shows and sends the handle every other
// machine sees; the others are named here with the hub's label for their account when the
// hub knows exactly one, else with this machine's label, and made unique by -2, -3 on the
// name part. Whenever the canonical label differs from this machine's own label for the
// slot, the handle with the local label stays an alias.
func (e Env) nameLocal(out []Session) {
	machine := HandlePart(e.Machine.Name)
	var names map[string]HubName
	if e.Remote != nil {
		names = e.Remote.Names()
	}
	hubLabel := map[string]string{}
	mixed := map[string]bool{}
	for _, n := range names {
		if n.Account == "" || n.Label == "" {
			continue
		}
		if l, ok := hubLabel[n.Account]; ok && l != n.Label {
			mixed[n.Account] = true
		}
		hubLabel[n.Account] = n.Label
	}
	taken := map[string]bool{}
	for i := range out {
		n, ok := names[out[i].Entry.SessionID]
		if ok && n.Mine {
			out[i].Tags = NormTags(n.Tags)
		}
		if !ok || !n.Mine || n.Handle == "" || taken[strings.ToLower(n.Handle)] {
			continue
		}
		local := out[i].AccountLabel
		out[i].Name, out[i].Handle = n.Name, n.Handle
		if n.Label != "" {
			out[i].AccountLabel = n.Label
		}
		out[i].Aliases = addAlias(append([]string(nil), n.Aliases...), n.Handle, MakeHandle(n.Name, local, machine))
		taken[strings.ToLower(n.Handle)] = true
	}
	for i := range out {
		if out[i].Handle != "" {
			continue
		}
		local, label := out[i].AccountLabel, out[i].AccountLabel
		if l, ok := hubLabel[out[i].Account]; ok && !mixed[out[i].Account] {
			label = l
		}
		name := out[i].Name
		for k := 1; ; k++ {
			cand := name
			if k > 1 {
				cand = name + "-" + strconv.Itoa(k)
			}
			if h := MakeHandle(cand, label, machine); !taken[strings.ToLower(h)] {
				out[i].Name, out[i].Handle = cand, h
				taken[strings.ToLower(h)] = true
				break
			}
		}
		out[i].AccountLabel = label
		out[i].Aliases = addAlias(nil, out[i].Handle, MakeHandle(out[i].Name, local, machine))
	}
}

// addAlias appends each alias that differs from the handle and from the ones already there.
func addAlias(list []string, handle string, aliases ...string) []string {
	for _, a := range aliases {
		if a == "" || strings.EqualFold(a, handle) {
			continue
		}
		dup := false
		for _, b := range list {
			if strings.EqualFold(a, b) {
				dup = true
				break
			}
		}
		if !dup {
			list = append(list, a)
		}
	}
	return list
}

// localLabels maps each account a managed slot is logged in to to the labels this machine
// shows for it (several slots may share an account).
func (e Env) localLabels() map[string][]string {
	out := map[string][]string{}
	login := e.loginReader()
	for _, slot := range e.Slots() {
		if l, err := login(slot); err == nil && l.HasSeat && l.Seat.Account != "" {
			out[l.Seat.Account] = append(out[l.Seat.Account], e.accountLabel(slot.Name))
		}
	}
	return out
}

// directoryAll is the directory a send or a list works with: this machine's sessions and,
// with a hub, the other machines' (remoteErr when the hub could not be asked). The hub is
// asked first, so the local sessions take the canonical handles of that answer. A remote
// session gains an alias for every label this machine shows for its account, so a handle
// typed with this machine's own label for the account still reaches it.
func (e Env) directoryAll() (local, all []Session, remoteErr error) { return e.directoryWith(false) }

// FreshRemote is a Remote that can ask the hub now, past its cache (peerlink.Remote).
type FreshRemote interface {
	SessionsFresh() ([]Session, error)
}

// directoryWith is directoryAll; fresh asks the hub past the Remote's cache when it can.
func (e Env) directoryWith(fresh bool) (local, all []Session, remoteErr error) {
	var rs []Session
	if e.Remote != nil {
		if f, ok := e.Remote.(FreshRemote); ok && fresh {
			rs, remoteErr = f.SessionsFresh()
		} else {
			rs, remoteErr = e.Remote.Sessions()
		}
	}
	local = e.Directory()
	all = append([]Session(nil), local...)
	if len(rs) == 0 {
		return local, all, remoteErr
	}
	labels := e.localLabels()
	for _, s := range rs {
		s.Aliases = append([]string(nil), s.Aliases...)
		for _, l := range labels[s.Account] {
			s.Aliases = addAlias(s.Aliases, s.Handle, MakeHandle(s.Name, l, HandlePart(s.MachineName)))
		}
		all = append(all, s)
	}
	return local, all, remoteErr
}

// Entries builds the list entries as seen from `from` (nil at a terminal, with all true).
// Without all, only sessions `from` may start a thread with, or reply to in an open thread,
// are returned.
func (e Env) Entries(dir []Session, from *Session, all bool) []ListEntry {
	m := e.matrix()
	var threads *ThreadSet
	if from != nil {
		threads = LoadThreads(e.ThreadsPath())
	}
	var out []ListEntry
	for _, s := range dir {
		if from != nil && s.Entry.SessionID == from.Entry.SessionID {
			continue
		}
		le := ListEntry{
			Session: s.Entry.SessionID, Handle: s.Handle, Aliases: s.Aliases, Name: s.Name, Account: s.Account,
			AccountLabel: s.AccountLabel, Machine: s.MachineName, Project: s.Project,
			State: s.Entry.Status, Local: !s.Remote && from != nil && from.Account != "" && from.Account == s.Account,
			Can: "none", Tags: NormTags(s.Tags), SameHost: !s.Remote,
		}
		if s.Remote {
			le.State, le.Blocked = s.State, s.Blocked
			if s.Device != "" {
				d := s.Device
				le.Device = &d
			}
		}
		if from != nil && (!s.Remote || s.Device != "") {
			le.Can = e.can(m, threads, *from, s)
			le.MayAsk = !s.Remote && from.Mode != s.Mode
		}
		if from != nil && !all && le.Can == "none" {
			continue
		}
		out = append(out, le)
	}
	return out
}

// MatrixConfigured reports whether a matrix is in force, as opposed to NoMatrix, which denies
// everything (the list command explains an empty result with it).
func (e Env) MatrixConfigured() bool {
	return e.matrix().Allowed(Party{}, Party{}, nil).Reason != CodeNoMatrix
}

func (e Env) matrix() Matrix {
	if e.Matrix != nil {
		return e.Matrix
	}
	return DefaultMatrix(e.State)
}

// can is "start" when the matrix lets from begin a thread with to, "reply" when an open
// thread that to opened lets from answer it, else "none".
func (e Env) can(m Matrix, ts *ThreadSet, from, to Session) string {
	fp, tp := party(from), party(to)
	th := ts.FindOpen(from.Entry.SessionID, to.Entry.SessionID)
	if th == nil || th.A == from.Entry.SessionID {
		if m.Allowed(fp, tp, th).Allowed {
			return "start"
		}
		return "none"
	}
	if m.Allowed(fp, tp, th).Allowed {
		return "reply"
	}
	return "none"
}

func party(s Session) Party {
	return Party{Session: s.Entry.SessionID, Account: s.Account, Label: s.AccountLabel}
}

// Resolve finds the session a `wy peers send <to>` names (peers.md 3.3): a handle or an alias
// of one, a unique prefix of either, a session id or a unique prefix of at least 8
// characters, or `@<name>`. A canonical handle wins over an alias. Several matches are an
// ambiguity: the candidates' (canonical) handles are returned.
func Resolve(dir []Session, to string) (Session, []string, *Refusal) {
	to = strings.TrimSpace(to)
	if to == "" {
		return Session{}, nil, Refuse(CodeUnknownTarget, "no target given")
	}
	pick := func(match func(Session) bool) []Session {
		var m []Session
		for _, s := range dir {
			if match(s) {
				m = append(m, s)
			}
		}
		return m
	}
	lower := strings.ToLower(to)
	anyAlias := func(s Session, match func(string) bool) bool {
		for _, a := range s.Aliases {
			if match(strings.ToLower(a)) {
				return true
			}
		}
		return false
	}
	steps := []func() []Session{
		func() []Session { return pick(func(s Session) bool { return strings.ToLower(s.Handle) == lower }) },
		func() []Session {
			return pick(func(s Session) bool { return anyAlias(s, func(a string) bool { return a == lower }) })
		},
		func() []Session { return pick(func(s Session) bool { return s.Entry.SessionID == to }) },
		func() []Session {
			if strings.HasPrefix(to, "@") && len(to) > 1 {
				n := strings.ToLower(to[1:])
				return pick(func(s Session) bool { return strings.ToLower(s.Name) == n })
			}
			return nil
		},
		func() []Session {
			return pick(func(s Session) bool { return strings.HasPrefix(strings.ToLower(s.Handle), lower) })
		},
		func() []Session {
			return pick(func(s Session) bool { return anyAlias(s, func(a string) bool { return strings.HasPrefix(a, lower) }) })
		},
		func() []Session {
			if len(to) < 8 {
				return nil
			}
			return pick(func(s Session) bool { return strings.HasPrefix(s.Entry.SessionID, to) })
		},
	}
	for _, step := range steps {
		switch m := step(); len(m) {
		case 0:
		case 1:
			return m[0], nil, nil
		default:
			var hs []string
			for _, s := range m {
				hs = append(hs, s.Handle)
			}
			return Session{}, hs, Refuse(CodeAmbiguous, "%q matches %s", to, strings.Join(hs, ", "))
		}
	}
	return Session{}, nil, Refuse(CodeUnknownTarget, "no live session matches %q (see wy peers list)", to)
}
