// Package hook implements `wy hook`: read the hook JSON from stdin, apply the content
// rules, add machine, slot, seat and term, append one hook_event record to the outbox,
// and start a background flush. It always exits 0 and never prints a decision, so it
// cannot change what Claude Code does. Budget: 20 ms p95.
//
// Contract: docs/spec/record.md 4.1, docs/spec/states.md section 2.
//
// Depends on: record, outbox, identity, config, paths, leases.
package hook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/clientenv"
	"github.com/mraj602/watchyard/internal/detach"
	"github.com/mraj602/watchyard/internal/leases"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/record"
)

const (
	// ErrorLogName is the file in <state> that receives one line per failure.
	ErrorLogName = "hook-errors.log"
	// ErrorLogMax is the size at which the log is started over.
	ErrorLogMax = 64 << 10

	maxStdin    = 8 << 20
	stdinWait   = 3 * time.Second
	flushEvery  = 2 * time.Second
	authEvery   = 10 * time.Minute
	authMaxAge  = 12 * time.Hour
	refreshArg  = "--refresh-auth"
	flushStamp  = "flush.stamp"  // in <state>/outbox/, shared with the flusher (outbox.md section 2)
	refreshName = "refresh-auth" // <state>/identity/refresh-auth-<slot>.stamp, one per slot
)

// Run is `wy hook [event]`. It never fails the caller: errors go to hook-errors.log and
// nothing is printed. args are the arguments after "hook".
func Run(args []string, stdin io.Reader, getenv func(string) string) {
	start := time.Now()
	var env *clientenv.Env
	event := ""
	defer func() {
		if r := recover(); r != nil {
			logError(env, getenv, event, fmt.Errorf("panic: %v", r))
		}
	}()
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		return // usage is printed by the command; a help flag is never a payload
	}
	if len(args) > 0 && args[0] == refreshArg {
		refreshAuth(getenv)
		return
	}
	if len(args) > 0 {
		event = args[0]
	}

	in, err := readStdin(stdin)
	if err != nil {
		logError(nil, getenv, event, err)
		return
	}
	env, err = clientenv.Load(getenv)
	if err != nil {
		logError(nil, getenv, event, err)
		return
	}
	if env.ConfigErr != nil {
		logError(env, getenv, "config", env.ConfigErr)
	}
	in = withEventName(in, event)

	rec, err := record.NewHookEvent(env.Meta(Term(getenv, env.Machine.ID)), in, record.HookOptions{LastMessages: env.Config.Privacy.LastMessage})
	if err != nil {
		logError(env, getenv, event, err)
		return
	}
	if rec.Derived == nil {
		rec.Derived = record.Derived{}
	}
	peerDerived(env, in, rec.Derived, time.Now())
	rec.Derived.Set("hook_ms", time.Since(start).Milliseconds())

	ob := outbox.New(env.OutboxDir())
	ob.NoFsync = !env.Config.Outbox.Fsync
	if _, err := ob.Append(rec); err != nil {
		logError(env, getenv, event, err)
		// Still try the background work: a flush may drain what is already queued.
	}
	markLease(env, getenv, eventName(in), in, time.Now())
	maybeSlotInfo(env, getenv, eventName(in), time.Now())

	if authStale(env, eventName(in)) {
		startRefresh(env)
	}
	startFlush(env)
	recordPeers(env, in, getenv, event)
}

// markLease appends a launched or ended line to the lease ledger of `wy account next` when
// the session has a lease there (setup.md 8.5). It takes no lock and never fails the hook.
func markLease(env *clientenv.Env, getenv func(string) string, event string, in []byte, now time.Time) {
	op := ""
	switch event {
	case "SessionStart":
		op = leases.OpLaunched
	case "SessionEnd":
		op = leases.OpEnded
	default:
		return
	}
	var p struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(in, &p) != nil || p.SessionID == "" {
		return
	}
	g := leases.Ledger{Dir: filepath.Join(env.Dirs.State.Path, "next")}
	if _, err := g.Mark(p.SessionID, op, now); err != nil {
		logError(env, getenv, event, fmt.Errorf("lease ledger: %w", err))
	}
}

// readStdin reads the whole payload, giving up after a few seconds so a caller that never
// closes the pipe cannot hold Claude Code.
func readStdin(r io.Reader) ([]byte, error) { return readStdinWait(r, stdinWait) }

// readStdinWait is readStdin with its own limit.
func readStdinWait(r io.Reader, wait time.Duration) ([]byte, error) {
	type result struct {
		b   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := io.ReadAll(io.LimitReader(r, maxStdin))
		ch <- result{b, err}
	}()
	select {
	case res := <-ch:
		return res.b, res.err
	case <-time.After(wait):
		return nil, fmt.Errorf("stdin not closed after %s", wait)
	}
}

var nameKey = []byte(`"hook_event_name"`)

// withEventName makes sure the payload carries hook_event_name: when the argument names
// an event and the payload does not, it is spliced in after the opening brace.
func withEventName(in []byte, event string) []byte {
	if event == "" || bytes.Contains(in, nameKey) {
		return in
	}
	t := bytes.TrimLeft(in, " \t\r\n")
	if len(t) == 0 || t[0] != '{' {
		return in
	}
	sep := ","
	if rest := bytes.TrimLeft(t[1:], " \t\r\n"); len(rest) > 0 && rest[0] == '}' {
		sep = ""
	}
	out := make([]byte, 0, len(t)+len(event)+32)
	out = append(out, `{"hook_event_name":`...)
	out = append(out, quote(event)...)
	out = append(out, sep...)
	return append(out, t[1:]...)
}

func quote(s string) []byte {
	b := []byte{'"'}
	for _, c := range []byte(s) {
		if c == '"' || c == '\\' || c < 0x20 {
			continue // an event name has none of these; drop rather than escape
		}
		b = append(b, c)
	}
	return append(b, '"')
}

// eventName is the payload's hook_event_name, found without a full parse.
func eventName(in []byte) string {
	i := bytes.Index(in, nameKey)
	if i < 0 {
		return ""
	}
	rest := in[i+len(nameKey):]
	q := bytes.IndexByte(rest, '"')
	if q < 0 {
		return ""
	}
	rest = rest[q+1:]
	e := bytes.IndexByte(rest, '"')
	if e < 0 {
		return ""
	}
	return string(rest[:e])
}

// authStale: the cached `claude auth status` result is missing or old, there is no seat
// at all (a fresh login on macOS lives in the Keychain, identity.md section 4), or a
// session starts and the result is over an hour old (identity.md section 4).
func authStale(env *clientenv.Env, event string) bool {
	age := time.Since(env.Login.AuthStatusAt)
	switch {
	case env.Login.AuthStatusAt.IsZero(), age > authMaxAge:
		return true
	case !env.Login.HasSeat:
		return age > authEvery
	}
	return event == "SessionStart" && age > time.Hour
}

// startRefresh starts `wy hook --refresh-auth` in the background (at most once per 10
// minutes, so a missing or failing `claude` costs nothing). It inherits the environment,
// so the child finds the same slot.
func startRefresh(env *clientenv.Env) {
	stamp := filepath.Join(env.Dirs.State.Path, "identity", refreshStampName(env.Slot.Name))
	_ = os.MkdirAll(filepath.Dir(stamp), 0o700)
	if !detach.Debounce(stamp, authEvery, time.Now()) {
		return
	}
	spawn(env, []string{"hook", refreshArg})
}

// refreshStampName names the auth-refresh debounce stamp of one slot, so a refresh in one
// slot never silences the others. The name keeps a readable part and a hash of the slot
// name, which cannot collide after the readable part loses its punctuation.
func refreshStampName(slot string) string {
	var b strings.Builder
	for _, r := range slot {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	sum := sha256.Sum256([]byte(slot))
	return fmt.Sprintf("%s-%s-%x.stamp", refreshName, strings.Trim(b.String(), "._"), sum[:3])
}

// startFlush starts `wy flush` in the background, at most once per 2 seconds, debounced on
// <state>/outbox/flush.stamp (outbox.md section 5), which the flusher touches as well. The
// flush it starts lingers until the stamp is 2 s old (step 8), so a record written by a
// debounced hook still goes out at once (cmd/wy hookFlushDebounce is this flushEvery).
// --from-hook lets that flush wait for a direct push that is due soon when it fails
// (machines.md 5.4).
func startFlush(env *clientenv.Env) {
	if !detach.Debounce(filepath.Join(env.OutboxDir(), flushStamp), flushEvery, time.Now()) {
		return
	}
	spawn(env, []string{"flush", "--quiet", "--from-hook"})
}

// Replaced by tests so the test binary never re-executes itself.
var (
	executable   = os.Executable
	startProcess = detach.Start
)

func spawn(env *clientenv.Env, args []string) {
	// WY_NO_SPAWN=1 turns background children off (tests, debugging).
	if os.Getenv("WY_NO_SPAWN") == "1" {
		return
	}
	exe, err := executable()
	if err != nil {
		logError(env, nil, "spawn", err)
		return
	}
	if err := startProcess(exe, args, nil); err != nil {
		logError(env, nil, "spawn", err)
	}
}

// refreshAuth is the background child: one `claude auth status --json`, stored in the
// identity cache. Errors are logged, never shown.
func refreshAuth(getenv func(string) string) {
	env, err := clientenv.Load(getenv)
	if err != nil {
		logError(nil, getenv, "refresh-auth", err)
		return
	}
	if _, err := env.Store.RefreshAuthStatus(context.Background(), env.Slot, os.Environ(), nil); err != nil {
		logError(env, getenv, "refresh-auth", err)
	}
}

// logError appends one line to <state>/hook-errors.log, starting the file over when it
// passes ErrorLogMax. It cannot fail: this is the last resort.
func logError(env *clientenv.Env, getenv func(string) string, event string, err error) {
	var dir string
	switch {
	case env != nil:
		dir = env.Dirs.State.Path
	case getenv != nil:
		if e, lerr := clientenv.StateDir(getenv); lerr == nil {
			dir = e
		}
	}
	if dir == "" {
		return
	}
	path := filepath.Join(dir, ErrorLogName)
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fi, serr := os.Stat(path); serr == nil && fi.Size() > ErrorLogMax {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	_ = os.MkdirAll(dir, 0o700)
	f, ferr := os.OpenFile(path, flags, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	msg := err.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	line := fmt.Sprintf("%s %s: %s\n", record.FormatTime(time.Now()), event, bytes.ReplaceAll([]byte(msg), []byte("\n"), []byte(" ")))
	_, _ = f.WriteString(line)
}
