package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/brand"
	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
	"github.com/mraj602/watchyard/internal/version"
)

// Limits from record.md 1.7 and 2.
const (
	MaxBatchBytes   = 8 << 20
	MaxBatchRecords = 1000
	MaxRecordBytes  = record.MaxEncodedBytes
)

// MaxClockSkew is how far ahead of the hub clock a record ts may be before it is clamped.
const MaxClockSkew = 5 * time.Minute

// Reject reasons (record.md 5).
const (
	BadID           = "bad_id"
	BadEnvelope     = "bad_envelope"
	MachineMismatch = "machine_mismatch"
	IDConflict      = "id_conflict"
	TooLarge        = "too_large"
)

// Rejected is one rejected record.
type Rejected struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Reply is the api.yaml IngestReply. Every id of the batch is in Accepted or Rejected.
type Reply struct {
	Accepted   []string   `json:"accepted"`
	Duplicates []string   `json:"duplicates"`
	Rejected   []Rejected `json:"rejected"`
	// Hub says what this hub reads (machines.md 7.2); clients cache record_v_max.
	Hub *HubInfo `json:"hub,omitempty"`
}

// HubInfo is IngestReply.hub.
type HubInfo struct {
	Version    string `json:"version"`
	RecordVMax int    `json:"record_v_max"`
}

// Options are what one ingest call knows besides the body.
type Options struct {
	// Backlog is X-Wy-Backlog (nil = not sent), or the drain's more-adjusted count.
	Backlog *int64
	// BacklogOldest is X-Wy-Backlog-Oldest or the drain hello's `oldest`, client ms; 0 = none.
	BacklogOldest int64
	// More: the client said more records are queued after this batch (a drain batch's
	// `more`). An upload whose X-Wy-Backlog is above its record count counts too.
	More bool
	// Reach is set by the hub's own pull ("pull"); "" keeps the stored value.
	Reach string
}

// Contact is a sign of life from a machine that is not a batch: a pull session's `hello`
// (machines.md 3.5 hub rule 2).
type Contact struct {
	Backlog       *int64
	BacklogOldest int64  // client ms, 0 = none
	Reach         string // "pull" for the hub's pull
	WyVersion     string // "0.5.0", "" = unknown
	RecordV       int    // highest record v the machine writes, 0 = unknown
}

// SessionChange is what happened to one session in a batch (api.yaml StreamSession).
type SessionChange struct {
	Change   string // new, state, update, ended
	Previous string // state before the batch, "" if none
}

// Changes is everything a batch changed, for the live stream.
type Changes struct {
	Sessions map[string]SessionChange
	Seats    map[string]bool // usage changed
	Machines map[string]bool
	// More says, per machine that sent a batch, whether it reported more records queued
	// behind it (true) or not (false); the alert engine's catch-up hold (push.md 3.3) reads
	// it. Absent for a machine that only appears through a record's envelope or a contact.
	More map[string]bool
	// Notices are the switch notices of the batch (record.md 4.12), for the alert engine
	// to turn into session_moved and switch_digest alerts (push.md 3.9).
	Notices []model.Notice
}

// Empty reports whether nothing changed.
func (c *Changes) Empty() bool {
	return len(c.Sessions) == 0 && len(c.Seats) == 0 && len(c.Machines) == 0 && len(c.Notices) == 0
}

// Ingester stores and types record batches.
type Ingester struct {
	Store *store.Store
	// OnChange is called after each committed batch that changed something.
	OnChange func(Changes)
	// Now is the hub clock; nil = time.Now.
	Now func() time.Time
	// Logf gets one line per notable outcome (a direct_push duplicate). Nil discards.
	Logf func(format string, args ...any)
}

func (g *Ingester) logf(format string, args ...any) {
	if g.Logf != nil {
		g.Logf(format, args...)
	}
}

func (g *Ingester) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// ErrBadBatch means the batch as a whole is malformed (400); ErrTooLarge means it is over
// the size or record limit (413).
var (
	ErrBadBatch = errors.New("bad batch")
	ErrTooLarge = errors.New("batch too large")
	// ErrFrozen means the hub is frozen for a move (machines.md 8.1): nothing was stored, the
	// client keeps its records (503, Retry-After 300).
	ErrFrozen = errors.New("hub is frozen for a move")
	// ErrNoMachine means the device is bound to no machine (record.md 5): only a machine-bound
	// token may write records (403 machine_mismatch, nothing stored).
	ErrNoMachine = errors.New("this token is bound to no machine")
)

// FrozenRetryAfter is the Retry-After (seconds) of a frozen hub's 503.
const FrozenRetryAfter = 300

// hubInfo is what this hub reads.
func hubInfo() *HubInfo { return &HubInfo{Version: version.Version, RecordVMax: record.MaxVersion} }

var (
	idPattern      = regexp.MustCompile(`^[0-9A-Za-z_-]{16,64}$`)
	machinePattern = regexp.MustCompile(`^m_[0-9A-HJKMNP-TV-Z]{26}$`)
	// Claude Code session ids are UUIDs; the hub also builds shell commands from them.
	sessionIDPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z_-]{0,63}$`)
	clientVersion    = regexp.MustCompile(`^` + brand.CLI + `/(\S+)$`)
)

type batch struct {
	V       *int              `json:"v"`
	Client  string            `json:"client"`
	Records []json.RawMessage `json:"records"`
}

type pending struct {
	raw json.RawMessage
	rec record.Record
	ts  int64
	tsT time.Time
}

// Ingest handles one batch from device dev. backlog is the X-Wy-Backlog header (nil if
// absent). Typed-table errors never fail a record: they are kept in record.type_error.
func (g *Ingester) Ingest(ctx context.Context, dev store.Device, body []byte, backlog *int64) (Reply, error) {
	return g.IngestWith(ctx, dev, body, Options{Backlog: backlog})
}

// IngestWith is Ingest with the options: backlog age, `more`, reach (the hub's pull
// passes "pull"). It answers ErrFrozen, storing nothing, while hub.frozen is set; the flag is
// read inside the write transaction, so no batch is acknowledged after the freeze committed.
func (g *Ingester) IngestWith(ctx context.Context, dev store.Device, body []byte, opt Options) (Reply, error) {
	if dev.MachineID == "" {
		return Reply{}, ErrNoMachine
	}
	backlog := opt.Backlog
	var b batch
	if err := json.Unmarshal(body, &b); err != nil {
		return Reply{}, fmt.Errorf("%w: not a JSON batch: %v", ErrBadBatch, err)
	}
	if b.V == nil || *b.V != 1 {
		return Reply{}, fmt.Errorf("%w: v must be 1", ErrBadBatch)
	}
	if len(b.Records) == 0 {
		// api.yaml Batch: minItems 1. (`wy connect` falls back to an empty batch to check a token without
		// storing anything when it has no slot to report: a good token gets 400, a bad one 401/403.)
		return Reply{}, fmt.Errorf("%w: records must be a non-empty array", ErrBadBatch)
	}
	if len(b.Records) > MaxBatchRecords {
		return Reply{}, fmt.Errorf("%w: %d records, limit %d", ErrTooLarge, len(b.Records), MaxBatchRecords)
	}
	reply := Reply{Accepted: []string{}, Duplicates: []string{}, Rejected: []Rejected{}, Hub: hubInfo()}
	seen := map[string]bool{}
	var ok []*pending
	for i, raw := range b.Records {
		p, rej := check(raw, dev)
		if rej != nil {
			if rej.ID == "" {
				rej.Detail = strings.TrimSpace(fmt.Sprintf("record %d. %s", i, rej.Detail))
			}
			reply.Rejected = append(reply.Rejected, *rej)
			continue
		}
		if seen[p.rec.ID] {
			continue // listed once in the reply
		}
		seen[p.rec.ID] = true
		ok = append(ok, p)
	}

	now := g.now()
	// record.md 1.6: a ts more than MaxClockSkew ahead of the hub clock is clamped to the
	// receive time for ordering, so one record from a wrong clock cannot freeze a session.
	// The raw ts stays in the stored body.
	for _, p := range ok {
		if p.tsT.After(now.Add(MaxClockSkew)) {
			p.tsT = now.UTC().Truncate(time.Millisecond)
			p.ts = p.tsT.UnixMilli()
		}
	}
	more := opt.More || (backlog != nil && *backlog > int64(len(b.Records)))
	// The client counts this batch in its backlog; once the batch is answered (this
	// transaction) it is no longer queued, so the stored backlog is what is left after it.
	// Otherwise `wy machine` shows the last batch's size right after a flush that sent
	// everything (machines.md 6.1).
	if backlog != nil {
		left := max(0, *backlog-int64(len(b.Records)))
		backlog = &left
	}
	ch := Changes{Sessions: map[string]SessionChange{}, Seats: map[string]bool{}, Machines: map[string]bool{}, More: map[string]bool{}}
	var logs []string
	err := g.Store.Write(ctx, func(tx *store.Tx) error {
		if err := frozenCheck(tx); err != nil {
			return err
		}
		t := &typer{tx: tx, dev: dev, ch: &ch, prev: map[string]string{}, now: now.UnixMilli()}
		var newest int64
		inserted := 0
		for _, p := range ok {
			ins, firstDev, firstMachine, err := tx.InsertRecordOwned(store.RawRecord{
				ID: p.rec.ID, DeviceID: dev.ID, MachineID: p.rec.Machine, Kind: p.rec.Kind, V: p.rec.V,
				TS: p.ts, ReceivedAt: now.UnixMilli(), Slot: p.rec.Slot, Body: p.raw,
			})
			if err != nil {
				return err
			}
			if !ins {
				// record.md 3.1: a duplicate is the same device, or another device
				// bound to the record's machine when the stored row is of that machine too
				// (reach change, re-pairing, a hub move); anything else is id_conflict.
				sameMachine := dev.MachineID != "" && dev.MachineID == p.rec.Machine && firstMachine == p.rec.Machine
				if firstDev != dev.ID && !sameMachine {
					reply.Rejected = append(reply.Rejected, Rejected{ID: p.rec.ID, Reason: IDConflict, Detail: "id already stored from another device"})
					continue
				}
				reply.Accepted = append(reply.Accepted, p.rec.ID)
				reply.Duplicates = append(reply.Duplicates, p.rec.ID)
				continue
			}
			inserted++
			reply.Accepted = append(reply.Accepted, p.rec.ID)
			newest = max(newest, p.ts)
			if err := t.typeOne(&p.rec, p.tsT, dev.Name); err != nil {
				return err
			}
		}
		t.finish()
		logs = t.logs
		if dev.MachineID != "" {
			if _, err := tx.EnsureMachine(dev.MachineID, dev.Name, now.UnixMilli()); err != nil {
				return err
			}
			if inserted > 0 {
				// A new record from a live device of a removed machine brings it back (6.2).
				if _, err := tx.MachineRestore(dev.MachineID); err != nil {
					return err
				}
			}
			var ver string
			if m := clientVersion.FindStringSubmatch(b.Client); m != nil {
				ver = m[1]
			}
			if err := tx.MachineHeartbeat(store.Heartbeat{Machine: dev.MachineID, LastSeen: now.UnixMilli(),
				LastRecord: newest, Backlog: backlog, WyVersion: ver, Reach: opt.Reach, BacklogOldest: opt.BacklogOldest}); err != nil {
				return err
			}
			ch.Machines[dev.MachineID] = true
			ch.More[dev.MachineID] = more
		}
		return nil
	})
	if err != nil {
		return Reply{}, err
	}
	for _, l := range logs {
		g.logf("%s", l)
	}
	if g.OnChange != nil && !ch.Empty() {
		g.OnChange(ch)
	}
	return reply, nil
}

// Contact records a sign of life from dev's machine that carries no records (a pull
// `hello`, machines.md 3.5 hub rule 2): last_seen = now, backlog and its age, version and
// reach. The engine is told, so a machine_silent alert recovers on a hello alone (push.md
// 3.1). Like a batch it answers ErrFrozen while hub.frozen is set (the pull refuses a frozen
// hub before it counts contact; this closes the gap of a freeze committed in between).
// RecordV > 0 stores the highest record v the machine writes (a drain hello's record_v).
func (g *Ingester) Contact(ctx context.Context, dev store.Device, c Contact) error {
	if dev.MachineID == "" {
		return errors.New("ingest: contact from a device without a machine")
	}
	now := g.now().UnixMilli()
	err := g.Store.Write(ctx, func(tx *store.Tx) error {
		if err := frozenCheck(tx); err != nil {
			return err
		}
		if _, err := tx.EnsureMachine(dev.MachineID, dev.Name, now); err != nil {
			return err
		}
		if err := tx.MachineHeartbeat(store.Heartbeat{Machine: dev.MachineID, LastSeen: now, Backlog: c.Backlog,
			WyVersion: c.WyVersion, Reach: c.Reach, BacklogOldest: c.BacklogOldest}); err != nil {
			return err
		}
		if c.RecordV > 0 {
			return tx.SetMachineRecordV(dev.MachineID, c.RecordV)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if g.OnChange != nil {
		g.OnChange(Changes{Sessions: map[string]SessionChange{}, Seats: map[string]bool{},
			Machines: map[string]bool{dev.MachineID: true}})
	}
	return nil
}

// frozenCheck is the hub.frozen check inside a write transaction (machines.md 8.1): the
// store has one writer, so a write either committed before the freeze or sees it here.
func frozenCheck(tx *store.Tx) error {
	if err := tx.CheckNotFrozen(); err != nil {
		if errors.Is(err, store.ErrFrozen) {
			return ErrFrozen
		}
		return err
	}
	return nil
}

// check validates one envelope (record.md 5) without touching the database.
func check(raw json.RawMessage, dev store.Device) (*pending, *Rejected) {
	var idOnly struct {
		ID any `json:"id"`
	}
	_ = json.Unmarshal(raw, &idOnly)
	id, _ := idOnly.ID.(string)
	if len(raw) > MaxRecordBytes {
		return nil, &Rejected{ID: id, Reason: TooLarge, Detail: fmt.Sprintf("%d bytes, limit %d", len(raw), MaxRecordBytes)}
	}
	if !idPattern.MatchString(id) {
		return nil, &Rejected{ID: id, Reason: BadID}
	}
	p := &pending{raw: raw}
	if err := json.Unmarshal(raw, &p.rec); err != nil {
		return nil, &Rejected{ID: id, Reason: BadEnvelope, Detail: "not a JSON object of the record shape"}
	}
	r := &p.rec
	if r.V < 1 {
		return nil, &Rejected{ID: id, Reason: BadEnvelope, Detail: "v must be an integer >= 1"}
	}
	if err := r.Validate(); err != nil {
		return nil, &Rejected{ID: id, Reason: BadEnvelope, Detail: err.Error()}
	}
	if !machinePattern.MatchString(r.Machine) {
		return nil, &Rejected{ID: id, Reason: BadEnvelope, Detail: "machine must be m_ + ULID"}
	}
	if dev.MachineID != "" && r.Machine != dev.MachineID {
		return nil, &Rejected{ID: id, Reason: MachineMismatch, Detail: "this token is bound to another machine"}
	}
	if r.Kind == record.KindHookEvent {
		var sid struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(r.Payload, &sid) == nil && sid.SessionID != "" && !sessionIDPattern.MatchString(sid.SessionID) {
			return nil, &Rejected{ID: id, Reason: BadEnvelope, Detail: "payload.session_id is not a session id"}
		}
	}
	t, _ := r.Time()
	p.tsT = t.UTC()
	p.ts = t.UnixMilli()
	return p, nil
}

func allMismatch(rs []Rejected) bool {
	for _, r := range rs {
		if r.Reason != MachineMismatch {
			return false
		}
	}
	return true
}

// Handler is POST /v1/ingest. It expects auth.Require(write) in front of it.
func (g *Ingester) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dev, ok := auth.DeviceFrom(r.Context())
		if !ok {
			auth.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing token")
			return
		}
		var src io.Reader = http.MaxBytesReader(w, r.Body, MaxBatchBytes+1)
		switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
		case "", "identity":
		case "gzip":
			zr, err := gzip.NewReader(src)
			if err != nil {
				auth.WriteError(w, http.StatusBadRequest, "bad_request", "bad gzip body")
				return
			}
			defer zr.Close()
			src = zr
		default:
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "Content-Encoding must be gzip or identity")
			return
		}
		var buf bytes.Buffer
		n, err := io.Copy(&buf, io.LimitReader(src, MaxBatchBytes+1))
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) || n > MaxBatchBytes {
			auth.WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "batch over 8 MiB")
			return
		}
		if err != nil {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "could not read the body")
			return
		}
		var opt Options
		if h := r.Header.Get("X-Wy-Backlog"); h != "" {
			if v, err := strconv.ParseInt(h, 10, 64); err == nil && v >= 0 {
				opt.Backlog = &v
			}
		}
		if h := r.Header.Get("X-Wy-Backlog-Oldest"); h != "" {
			if t, err := time.Parse(time.RFC3339Nano, h); err == nil {
				opt.BacklogOldest = t.UnixMilli()
			}
		}
		reply, err := g.IngestWith(r.Context(), dev, buf.Bytes(), opt)
		switch {
		case errors.Is(err, ErrFrozen):
			w.Header().Set("Retry-After", strconv.Itoa(FrozenRetryAfter))
			auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "hub is frozen for a move")
			return
		case errors.Is(err, ErrNoMachine):
			auth.WriteError(w, http.StatusForbidden, MachineMismatch, err.Error())
			return
		case errors.Is(err, ErrTooLarge):
			auth.WriteError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
			return
		case errors.Is(err, ErrBadBatch):
			auth.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		case err != nil:
			w.Header().Set("Retry-After", "5")
			auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "could not store the batch; retry")
			return
		}
		if len(reply.Accepted) == 0 && len(reply.Rejected) > 0 && allMismatch(reply.Rejected) {
			// The whole batch is for another machine: this token is the wrong one, not the
			// records. A 403 keeps the client's outbox and cursor where they are.
			auth.WriteError(w, http.StatusForbidden, MachineMismatch, "this token is bound to another machine")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(reply)
	})
}
