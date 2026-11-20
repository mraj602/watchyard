package peers

import "github.com/mraj602/watchyard/internal/keys"

// The peer frames on the link (link.md 6.2-6.4), shared by the hub's relay and the agent.

// PeerMsgUp is the device→hub `peer_msg`: the signed envelope.
type PeerMsgUp struct {
	T   string   `json:"t"`
	Doc keys.Doc `json:"doc"`
}

// PeerAck is the hub's ephemeral answer to a peer_msg. Detail is the reason word of a
// refusal, or "offline" for a message queued while the target machine is not connected.
type PeerAck struct {
	V      int    `json:"v"`
	Msg    string `json:"msg"`
	Status string `json:"status"` // queued | refused
	Detail string `json:"detail,omitempty"`
	// Exp is when the queued message expires (unix seconds).
	Exp int64 `json:"exp,omitempty"`
}

// PeerResultUp is the receiving agent's verdict (link.md 6.2 peer_result).
type PeerResultUp struct {
	T       string `json:"t"`
	Msg     string `json:"msg"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	HumanAt string `json:"human_at,omitempty"`
}

// PeerLogUp is a same-machine delivery's metadata (link.md 6.2 peer_log). With Event set it is
// instead an event of this machine about one thread (Event, Thread, Detail, At; the message
// fields empty): EventThreadResumeRefused.
type PeerLogUp struct {
	T           string `json:"t"`
	Event       string `json:"event,omitempty"`
	Msg         string `json:"msg"`
	Thread      string `json:"thread"`
	FromSession string `json:"from_session"`
	ToSession   string `json:"to_session"`
	Kind        Kind   `json:"kind"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
	Size        int    `json:"size"`
	At          string `json:"at"`
}

// EventThreadResumeRefused is the peer_log event of a machine that refused the owner's
// peer_thread_resume (peers.md 6.3) and keeps its loop-guard stop; Detail is the reason,
// ResumeRefusedReason.
const EventThreadResumeRefused = "thread_resume_refused"

// ResumeRefusedReason is why a machine refuses a relayed thread resume: peers.hub_may_widen
// is false (keys.md 5.4).
const ResumeRefusedReason = "hub_may_widen"

// PeersListUp asks for the session directory.
type PeersListUp struct {
	T  string `json:"t"`
	ID string `json:"id"`
}

// WireSession is one session of the hub's directory (peers.md 3.2, without `can`, which the
// asking agent works out with its own matrix and threads). Handle is the canonical handle,
// made with the seat's label (AccountLabel; the account uuid's first 8 characters when the
// seat has none), so every machine shows the same one. Aliases are other handles that still
// name the session: the one with its slot folder's name as the label (what its own machine
// shows when no label is set there), when that differs.
type WireSession struct {
	Session      string   `json:"session"`
	Handle       string   `json:"handle"`
	Name         string   `json:"name"`
	Account      string   `json:"account"`
	AccountLabel string   `json:"account_label"`
	Aliases      []string `json:"aliases,omitempty"`
	Machine      string   `json:"machine"` // the machine's name
	MachineID    string   `json:"machine_id"`
	Project      string   `json:"project"`
	State        string   `json:"state"`
	Device       *string  `json:"device"`
	// Tags are the session's tags at the hub (peers.md 3.4), sorted, omitted when none.
	Tags []string `json:"tags,omitempty"`
}

// PeersReply is the hub's `peers` answer: other machines' sessions and the certificates of
// their agents' devices. Mine are the asking machine's own sessions as the hub names them,
// so its agent shows, and puts in its envelopes, the handles every other machine sees
// (peers.md 3.2). A hub older than this field sends none.
type PeersReply struct {
	V        int                 `json:"v"`
	ID       string              `json:"id"`
	Sessions []WireSession       `json:"sessions"`
	Mine     []WireSession       `json:"mine,omitempty"`
	Certs    map[string]keys.Doc `json:"certs"`
	// Tag is the outcome of the peer_tag this reply answers (peers.md 3.4); absent for a
	// peers_list.
	Tag *TagAnswer `json:"tag,omitempty"`
}

// PeerTagUp adds or removes one tag of a session on the sending machine (link.md 6.2,
// peers.md 3.4). The hub answers `peers` with the same id and `tag` set.
type PeerTagUp struct {
	T       string `json:"t"`
	ID      string `json:"id"`
	Session string `json:"session"`
	Op      string `json:"op"` // add | rm
	Tag     string `json:"tag"`
}

// QueuedPeerMsg is the body of a queued `peer_msg` (link.md 6.4).
type QueuedPeerMsg struct {
	V    int      `json:"v"`
	Doc  keys.Doc `json:"doc"`
	Cert keys.Doc `json:"cert"`
	// Accounts is the sender's owner-signed machine_accounts document (keys.md 4.3): the
	// receiver refuses a sender account it does not list. A hub older than it sends none.
	Accounts *keys.Doc `json:"accounts,omitempty"`
}

// PeerStatusBody is the body of a queued `peer_status`: a sent message's final state.
type PeerStatusBody struct {
	V      int    `json:"v"`
	Msg    string `json:"msg"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	At     string `json:"at"`
}

// ThreadResumeBody is the body of a queued `peer_thread_resume` (link.md 6.4, peers.md 6.3):
// the owner resumed a thread the loop guard stopped. By is the device that asked at the hub.
type ThreadResumeBody struct {
	V      int    `json:"v"`
	Thread string `json:"thread"`
	By     string `json:"by"`
	At     string `json:"at"`
}
