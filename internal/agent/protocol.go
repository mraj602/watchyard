package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mraj602/watchyard/internal/peers"
)

// Ops of the control socket (peers.md 2.3).
const (
	OpWhoami = "whoami"
	OpList   = "list"
	OpSend   = "send"
	OpOff    = "off"
	OpOn     = "on"
	OpState  = "state"
	// OpTag adds or removes a tag of the calling session, or lists its tags (peers.md 3.4).
	OpTag = "tag"
	// OpTrust approves, revokes or shows the trusted sender, from a terminal only (peers.md
	// 4.1a); OpServiceSend is a send by the approved Doorbell.
	OpTrust       = "trust"
	OpServiceSend = "service_send"
)

// Trust ops (Request.TrustOp).
const (
	TrustSet  = "set"
	TrustOff  = "off"
	TrustShow = "show"
)

// MaxLine bounds one request line.
const MaxLine = 1 << 20

// Request is one JSON line to the control socket.
type Request struct {
	Op        string `json:"op"`
	ClaudePID int    `json:"claude_pid,omitempty"`
	ConfigDir string `json:"config_dir,omitempty"` // informational: the agent derives it from the process
	All       bool   `json:"all,omitempty"`
	To        string `json:"to,omitempty"`
	Text      string `json:"text,omitempty"`
	TTLs      int    `json:"ttl_s,omitempty"`
	WaitS     int    `json:"wait_s,omitempty"`
	// TagOp (add, rm, list) and Tag are the `tag` op's arguments.
	TagOp string `json:"tag_op,omitempty"`
	Tag   string `json:"tag,omitempty"`
	// TrustOp (set, off, show), Program and Account are the `trust` op's arguments; the agent
	// resolves Program itself.
	TrustOp string `json:"trust_op,omitempty"`
	Program string `json:"program,omitempty"`
	Account string `json:"account,omitempty"`
	// One, SameHost and Stdin are a `service_send`'s options (peers.md 4.1a).
	One      bool `json:"one,omitempty"`
	SameHost bool `json:"same_host,omitempty"`
	Stdin    bool `json:"stdin,omitempty"`
}

// Error is the refusal in a reply: Code is a peers refusal code.
type Error struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// SessionInfo is the caller's session (whoami).
type SessionInfo struct {
	Session   string `json:"session"`
	PID       int    `json:"pid"`
	ConfigDir string `json:"config_dir"`
}

// State is the reply of `state`, for `wy agent status`.
type State struct {
	PID     int        `json:"pid"`
	Started string     `json:"started"`
	Version string     `json:"version,omitempty"`
	Socket  string     `json:"socket"`
	Link    LinkStatus `json:"link"`
}

// Reply is one JSON line back. A send fills Msg, Thread, Status ("delivered"), To (the
// target's handle) and HeldLikely; a refusal fills only Error.
type Reply struct {
	Error      *Error `json:"error,omitempty"`
	OK         bool   `json:"ok,omitempty"`
	Msg        string `json:"msg,omitempty"`
	Thread     string `json:"thread,omitempty"`
	Status     string `json:"status,omitempty"`
	Detail     string `json:"detail,omitempty"`
	To         string `json:"to,omitempty"`
	HeldLikely bool   `json:"held_likely,omitempty"`
	// Expires is when a queued message expires (RFC 3339); Detail is then "offline" when
	// the target's machine is not connected.
	Expires string            `json:"expires,omitempty"`
	Session *SessionInfo      `json:"session,omitempty"`
	Entries []peers.ListEntry `json:"entries,omitempty"`
	Asked   bool              `json:"asked,omitempty"`
	State   *State            `json:"state,omitempty"`
	// TagResult is the `tag` op's answer.
	TagResult *peers.TagResult `json:"tag_result,omitempty"`
	// Trust is the `trust` op's record (set, show); nil when none.
	Trust *peers.TrustRecord `json:"trust,omitempty"`
	// Lines are a `service_send`'s outcomes, one per holder sent to (each a send's reply,
	// with To set), and Exit the exit code of the whole send (peers.md 3.4).
	Lines []Reply `json:"lines,omitempty"`
	Exit  int     `json:"exit,omitempty"`
	// Warning is printed by the CLI (the trusttest build's).
	Warning string `json:"warning,omitempty"`
}

// Refusal is the reply's error as a *peers.Refusal, or nil.
func (r Reply) Refusal() *peers.Refusal {
	if r.Error == nil {
		return nil
	}
	return &peers.Refusal{Code: r.Error.Code, Detail: r.Error.Detail}
}

// MaxSocketPath is the longest socket path that binds: sun_path is 104 bytes on macOS and
// 108 on Linux, and both count the trailing NUL.
func MaxSocketPath(goos string) int {
	if goos == "darwin" {
		return 103
	}
	return 107
}

// SocketPath is the control socket (peers.md 2.3): $XDG_RUNTIME_DIR/wy/agent.sock on Linux,
// <state>/agent/agent.sock on macOS. When that path is longer than the OS allows (a deep
// state folder, a long $XDG_RUNTIME_DIR), it falls back to $TMPDIR/wy-<uid>/agent.sock, and
// to /tmp/wy-<uid>/agent.sock when even that is too long. The fallback folder is 0700 and
// the server checks it (listen). The agent and every client call this with the same inputs,
// so they agree on the path.
func SocketPath(goos, runtime, state string) string {
	return socketPath(goos, runtime, state, os.TempDir(), os.Getuid())
}

func socketPath(goos, runtime, state, tmp string, uid int) string {
	var p string
	if goos == "darwin" {
		p = filepath.Join(state, "agent", "agent.sock")
	} else {
		if runtime == "" {
			runtime = fmt.Sprintf("/run/user/%d", uid)
		}
		p = filepath.Join(runtime, "wy", "agent.sock")
	}
	max := MaxSocketPath(goos)
	if len(p) <= max {
		return p
	}
	dir := fmt.Sprintf("wy-%d", uid)
	if q := filepath.Join(tmp, dir, "agent.sock"); len(q) <= max {
		return q
	}
	return filepath.Join("/tmp", dir, "agent.sock")
}

// ErrNoAgent means nothing is listening on the control socket: the caller falls back to the
// direct path (peers.md 8.5, without the agent).
var ErrNoAgent = errors.New("the wy agent is not running")

// ownUID is the uid an agent must run as for Call to talk to it; tests replace it.
var ownUID = os.Getuid

// Call sends one request and reads the reply. ErrNoAgent when the socket is missing, refuses
// or belongs to another user (nothing was sent); any other error happened after the connection was made, so the request may have
// been handled and must not be repeated by another path.
func Call(sock string, req Request, timeout time.Duration) (Reply, error) {
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOTDIR) {
			return Reply{}, ErrNoAgent
		}
		return Reply{}, err
	}
	defer c.Close()
	// The socket may sit in a shared temp folder (SocketPath's fallback), where another user
	// can listen first: never hand the request (a message's text) to anyone but ourselves.
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return Reply{}, ErrNoAgent
	}
	if _, uid, err := peers.PeerPID(uc); err != nil || uid != ownUID() {
		return Reply{}, fmt.Errorf("%w: %s is served by another user", ErrNoAgent, sock)
	}
	_ = c.SetDeadline(time.Now().Add(timeout))
	b, err := json.Marshal(req)
	if err != nil {
		return Reply{}, err
	}
	// The agent may refuse before reading (another user) and close: a failed write still
	// leaves its reply to read.
	_, werr := c.Write(append(b, '\n'))
	line, err := bufio.NewReaderSize(io.LimitReader(c, MaxLine), 64<<10).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		if werr != nil {
			return Reply{}, werr
		}
		return Reply{}, fmt.Errorf("the wy agent closed the connection: %w", err)
	}
	var rep Reply
	if err := json.Unmarshal(line, &rep); err != nil {
		return Reply{}, fmt.Errorf("bad reply from the wy agent: %w", err)
	}
	return rep, nil
}
