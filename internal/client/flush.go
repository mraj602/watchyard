package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/outbox"
)

// Flush tuning from outbox.md section 5.
const (
	DefaultBudget = 30 * time.Second
	MaxBackoff    = 5 * time.Minute
	baseBackoff   = time.Second
	// maxDropsPerRun bounds how many single records a run may give up on after the hub
	// answered 400/413 for them alone, so a hub that refuses everything (for a reason that
	// is not about the records) cannot make the client discard the whole outbox.
	maxDropsPerRun = 10
	// loneDropWindow: a lone 400/413 with nothing accepted before it in the run is dropped
	// only if the previous such drop is older than this (or there is none). Two in a row
	// within the window mean the hub (or a proxy in front of it) refuses everything, so the
	// outbox is kept and the flusher backs off.
	loneDropWindow = time.Hour

	// StatusFile is written next to the cursor for `wy doctor`.
	StatusFile = "flush.status.json"
	// StampFile's mtime is the last flush start (debouncing by `wy hook`).
	StampFile = "flush.stamp"
)

// FlushOptions configures one flush run.
type FlushOptions struct {
	Outbox *outbox.Outbox
	Client *Client

	// Wait keeps retrying failed batches with jittered backoff until Budget is used up
	// (used by `wy flush --wait` and `wy connect`). Without it a failure stops the run and
	// leaves the backoff in cursor.json for the next trigger.
	Wait bool
	// Force ignores a stored backoff and a stored "token rejected" state (a flush started
	// by hand). The hook-started flush leaves it false.
	Force bool

	// Linger, when set, keeps a drained run alive until flush.stamp is Linger old, reading
	// the outbox once more after that (outbox.md section 5, step 8). `wy hook` skips its own
	// flush while the stamp is younger than its debounce, so the flush it started must pick
	// up what those later hooks wrote. The hook-started flush sets it to that debounce.
	Linger time.Duration

	Budget     time.Duration // default DefaultBudget
	MaxRecords int           // default MaxBatchRecords
	MaxBytes   int           // default MaxBatchBytes
	CapBytes   int64         // outbox.max_mb in bytes; 0 skips the cap
	KeepDays   int           // outbox.keep_days; 0 skips pruning

	// StateDir holds hub.json (machines.md 7.2), the cache of what the hub reads. Empty
	// means the folder above the outbox (<state>, since the outbox is <state>/outbox).
	StateDir string

	// Test seams.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	Rand  func() float64 // in [0,1)
	Logf  func(format string, args ...any)
}

// FlushResult says what a run did.
type FlushResult struct {
	Batches    int // requests that got a 200
	Sent       int // records put in answered batches
	Accepted   int // includes Duplicates
	Duplicates int
	Rejected   int
	Dropped    int // records given up on after a lone 400/413
	Drained    bool
	Skipped    string // why nothing was tried: another flusher, backoff, rejected token
	Err        error
	// Held is the number of records kept back because their v is above what the hub reads
	// (machines.md 7.2); the run sent everything before them.
	Held int
}

// Status is flush.status.json: the last run's outcome, for wy doctor.
type Status struct {
	Time        string `json:"time"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	AuthFailed  bool   `json:"auth_failed,omitempty"`
	TokenFP     string `json:"token_fp,omitempty"`
	LastSuccess string `json:"last_success,omitempty"`
	// RefusesRecords: the hub answered 400/413 to single records again and again with
	// nothing accepted, so nothing was dropped (`wy doctor`: "hub refuses records").
	RefusesRecords bool `json:"refuses_records,omitempty"`

	// outbox.md 8. LastContact is the last successful exchange with the hub: a 200
	// ingest reply, a 200 /health, or a drain start (a 401/403/5xx is not contact). Via is
	// "push" (wy flush) or "pull" (wy outbox drain). PullRefused is the drain `refuse` code
	// of the last pull. Held and HubTooOld say records are kept back for an older hub.
	LastContact string     `json:"last_contact,omitempty"`
	Via         string     `json:"via,omitempty"`
	PullRefused string     `json:"pull_refused,omitempty"`
	Held        int        `json:"held,omitempty"`
	HubTooOld   *HubTooOld `json:"hub_too_old,omitempty"`
}

// HubTooOld is flush.status.json's hub_too_old: the hub (Hub, its version when known) reads
// records up to RecordVMax, and a held record needs Needs.
type HubTooOld struct {
	Hub        string `json:"hub,omitempty"`
	RecordVMax int    `json:"record_v_max"`
	Needs      int    `json:"needs"`
}

// ViaPush and ViaPull are Status.Via.
const (
	ViaPush = "push"
	ViaPull = "pull"
)

// ReadStatus loads flush.status.json from the outbox folder; a missing or bad file is a
// zero Status.
func ReadStatus(dir string) Status {
	var s Status
	if b, err := os.ReadFile(filepath.Join(dir, StatusFile)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func tokenFP(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:8])
}

// Backoff is the wait after the nth consecutive failure (1-based): 1 s, 2 s, 4 s ... capped
// at five minutes, with jitter that keeps between half and all of the nominal value.
func Backoff(failures int, r float64) time.Duration {
	if failures < 1 {
		failures = 1
	}
	d := MaxBackoff
	if failures-1 < 20 {
		if x := baseBackoff << (failures - 1); x < MaxBackoff {
			d = x
		}
	}
	return d/2 + time.Duration(float64(d/2)*r)
}

type flusher struct {
	FlushOptions
	cl       *Client // o.Client with the X-Wy-Backlog-Oldest header added
	cur      outbox.Cursor
	res      FlushResult
	failures int
	backlog  int // estimate of records still queued; -1 unknown
	refuses  bool

	stateDir   string
	maxV       int // highest record v the hub reads, from hub.json and the replies
	hubVersion string
	held       int
	heldNeeds  int
	contact    bool // a 200 from the hub this run
	refreshed  bool // /health was asked for record_v_max this run
	oldest     *backlogOldest
}

// Flush uploads the outbox from the cursor onward. It takes flush.lock (a second flusher
// returns at once with Skipped set), never moves the cursor past a record the hub has not
// answered, and afterwards enforces the size cap and prunes old day files.
func Flush(ctx context.Context, o FlushOptions) FlushResult {
	if o.Budget <= 0 {
		o.Budget = DefaultBudget
	}
	if o.MaxRecords <= 0 {
		o.MaxRecords = MaxBatchRecords
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = MaxBatchBytes
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	if o.Rand == nil {
		o.Rand = rand.Float64
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	f := &flusher{FlushOptions: o, backlog: -1, oldest: &backlogOldest{}}
	f.stateDir = o.StateDir
	if f.stateDir == "" {
		f.stateDir = filepath.Dir(o.Outbox.Dir)
	}
	f.cl = f.oldest.wrap(o.Client)
	f.maxV = DefaultRecordVMax
	if c, ok := ReadHubCache(f.stateDir); ok && sameHub(c.URL, o.Client.BaseURL) {
		f.maxV, f.hubVersion = c.VMax(), c.Version
	}

	lock, ok, err := o.Outbox.TryFlushLock()
	if err != nil {
		f.res.Err = err
		return f.res
	}
	if !ok {
		f.res.Skipped = "another flush is running"
		return f.res
	}
	defer lock.Release()

	f.cur = o.Outbox.ReadCursor()
	f.failures = f.cur.Failures
	fp := tokenFP(o.Client.Token)
	if !o.Force {
		if t, err := time.Parse(time.RFC3339Nano, f.cur.RetryAfter); err == nil && o.Now().Before(t) {
			f.res.Skipped = "backing off until " + t.Local().Format("15:04:05")
			return f.res
		}
		if st := ReadStatus(o.Outbox.Dir); st.AuthFailed && st.TokenFP == fp {
			f.res.Skipped = "the hub rejected this token; fix it, or run `wy flush` by hand to retry"
			return f.res
		}
	}
	f.touchStamp()
	started := o.Now()
	deadline := started.Add(o.Budget)

	f.run(ctx, deadline)
	f.linger(ctx, deadline)

	// Housekeeping runs after failures too: a long outage is exactly when the cap matters.
	if o.CapBytes > 0 {
		if r, err := o.Outbox.EnforceCap(o.CapBytes); err != nil {
			o.Logf("size cap: %v", err)
		} else if len(r.Dropped) > 0 {
			o.Logf("outbox over its cap: dropped %v", r.Dropped)
		}
	}
	if o.KeepDays > 0 {
		if _, err := o.Outbox.Prune(o.KeepDays); err != nil {
			o.Logf("prune: %v", err)
		}
	}
	f.writeStatus(fp)
	return f.res
}

// linger is step 8 of outbox.md section 5: after a clean drain, wait until flush.stamp is
// Linger old and drain again, as long as that keeps finding records. A hook that wrote a
// record while the stamp was young did not start a flush of its own; the stamp only moves
// when a flush starts, so a last read at stamp + Linger sees every such record.
func (f *flusher) linger(ctx context.Context, deadline time.Time) {
	if f.Linger <= 0 {
		return
	}
	stamp := filepath.Join(f.Outbox.Dir, StampFile)
	for f.res.Err == nil && f.res.Drained && f.held == 0 {
		fi, err := os.Stat(stamp)
		if err != nil {
			return
		}
		until := fi.ModTime().Add(f.Linger)
		now := f.Now()
		if !now.Before(until) || until.After(deadline) {
			return
		}
		if err := f.Sleep(ctx, until.Sub(now)); err != nil {
			return
		}
		f.res.Drained = false
		f.run(ctx, deadline)
	}
}

func (f *flusher) touchStamp() {
	p := filepath.Join(f.Outbox.Dir, StampFile)
	now := f.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		if fh, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fh.Close()
			_ = os.Chtimes(p, now, now)
		}
	}
}

func (f *flusher) writeStatus(fp string) {
	prev := ReadStatus(f.Outbox.Dir)
	st := Status{Time: f.Now().UTC().Format("2006-01-02T15:04:05.000Z"), TokenFP: fp, LastSuccess: prev.LastSuccess,
		LastContact: prev.LastContact, Via: ViaPush}
	st.RefusesRecords = f.refuses
	if f.contact {
		st.LastContact = st.Time
	}
	if f.held > 0 {
		st.Held = f.held
		st.HubTooOld = &HubTooOld{Hub: f.hubVersion, RecordVMax: f.maxV, Needs: f.heldNeeds}
	}
	if f.res.Err == nil {
		st.OK = true
		if f.res.Batches > 0 || f.res.Drained {
			st.LastSuccess = st.Time
		}
	} else {
		st.Error = f.res.Err.Error()
		var se *StatusError
		if errors.As(f.res.Err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden) {
			st.AuthFailed = true
		}
	}
	writeStatusFile(f.Outbox.Dir, st)
}

// writeStatusFile stores flush.status.json (shared by the flusher and the drain).
func writeStatusFile(dir string, st Status) {
	b, _ := json.Marshal(st)
	_ = atomicfile.Write(filepath.Join(dir, StatusFile), append(b, '\n'), 0o600)
}

func (f *flusher) run(ctx context.Context, deadline time.Time) {
	maxRec := f.MaxRecords
	if f.cur.BatchMax > 0 && f.cur.BatchMax < maxRec {
		maxRec = f.cur.BatchMax
	}
	for {
		if err := ctx.Err(); err != nil {
			f.res.Err = err
			return
		}
		b, err := f.Outbox.ReadV(f.cur, maxRec, f.MaxBytes, f.maxV)
		if err != nil {
			f.res.Err = err
			return
		}
		f.held, f.heldNeeds = b.Held, b.HeldNeeds
		f.res.Held = b.Held
		if len(b.Items) == 0 {
			if b.Next.File != f.cur.File || b.Next.Offset != f.cur.Offset {
				f.cur.File, f.cur.Offset = b.Next.File, b.Next.Offset
				if err := f.saveCursor(); err != nil {
					f.res.Err = err
					return
				}
			}
			if b.Held > 0 {
				// The first record waiting is newer than the hub reads. The cache may be
				// stale (the hub was updated): ask /health once, then stop as at the end.
				if f.refreshHub(ctx) {
					continue
				}
				f.Logf("holding %d record(s): the hub reads records up to v%d, this machine writes v%d", b.Held, f.maxV, b.HeldNeeds)
				return
			}
			f.res.Drained = true
			return
		}
		f.estimateBacklog(b)
		f.oldest.set(b)

		recs := make([][]byte, len(b.Items))
		for i, it := range b.Items {
			recs[i] = it.Raw
		}
		reply, err := f.cl.Ingest(ctx, recs, max(f.backlog, len(recs)))
		if err == nil {
			f.contact = true
			f.learnHub(reply.Hub)
		}
		if err != nil {
			var se *StatusError
			if errors.As(err, &se) {
				switch {
				case se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden:
					f.res.Err = err // no retry until the token changes
					return
				case se.Status == http.StatusBadRequest || se.Status == http.StatusRequestEntityTooLarge:
					if len(b.Items) > 1 {
						maxRec = max(1, len(b.Items)/2)
						continue
					}
					if f.res.Dropped >= maxDropsPerRun {
						f.res.Err = err
						return
					}
					if f.hubRefuses(se) {
						return
					}
					f.dropLone(b, se)
					if f.res.Err != nil {
						return
					}
					continue
				}
			}
			if len(b.Items) > 1 && tooSlow(err) {
				// The hub could not answer this batch in time: smaller batches until it can.
				// Kept in the cursor, as one attempt can use up a whole run's budget.
				maxRec = max(1, len(b.Items)/2)
				f.cur.BatchMax = maxRec
				f.Logf("hub too slow for %d records; next batches carry at most %d", len(b.Items), maxRec)
			}
			if !f.retryLater(ctx, err, deadline) {
				return
			}
			continue
		}

		if f.cur.BatchMax > 0 {
			f.cur.BatchMax += max(1, f.cur.BatchMax/4) // grow back slowly: halving was a timeout
			if f.cur.BatchMax >= f.MaxRecords {
				f.cur.BatchMax = 0
			}
		}
		missing, err := f.apply(b, reply)
		if err != nil {
			f.res.Err = err
			return
		}
		if missing > 0 {
			f.Logf("hub reply left out %d of %d ids", missing, len(b.Items))
			if !f.retryLater(ctx, fmt.Errorf("hub reply left out %d of %d records", missing, len(b.Items)), deadline) {
				return
			}
			continue
		}
		f.failures = 0
		maxRec = f.MaxRecords
		if f.cur.BatchMax > 0 {
			maxRec = f.cur.BatchMax
		}
		if f.Now().After(deadline) {
			return // budget used up; the next trigger continues
		}
	}
}

// estimateBacklog keeps a rough count of queued records for the X-Wy-Backlog header.
func (f *flusher) estimateBacklog(b *outbox.Batch) {
	if f.backlog >= 0 && b.More {
		return
	}
	if !b.More {
		f.backlog = len(b.Items)
		return
	}
	if st, err := f.Outbox.Stat(); err == nil && len(b.Items) > 0 {
		var n int
		for _, it := range b.Items {
			n += len(it.Raw) + 1
		}
		avg := max(1, n/len(b.Items))
		f.backlog = int(st.UnflushedBytes) / avg
	}
}

// apply processes a 200 reply with outbox.Apply (the function the drain uses too), then
// writes the cursor. It returns how many records of the batch were not answered.
func (f *flusher) apply(b *outbox.Batch, reply *IngestReply) (missing int, err error) {
	rej := make([]outbox.Rejected, len(reply.Rejected))
	for i, r := range reply.Rejected {
		rej[i] = outbox.Rejected{ID: r.ID, Reason: r.Reason, Detail: r.Detail}
	}
	ap := f.Outbox.Apply(&f.cur, b, outbox.Answer{Accepted: reply.Accepted, Duplicates: reply.Duplicates, Rejected: rej}, f.Logf)
	f.res.Rejected += ap.Rejected
	f.res.Accepted += ap.Accepted
	f.res.Duplicates += ap.Duplicates
	f.res.Batches++
	f.res.Sent += len(b.Items) - ap.Missing
	if f.backlog > 0 {
		f.backlog = max(0, f.backlog-(len(b.Items)-ap.Missing))
	}
	if f.res.Accepted > 0 {
		f.cur.LoneDrops, f.cur.LoneDropAt = 0, "" // the hub takes records: the drops were poison
	}
	return ap.Missing, f.saveCursor()
}

// learnHub takes what a 200 reply says the hub reads (machines.md 7.2) and caches it.
func (f *flusher) learnHub(h *HubInfo) {
	if h == nil {
		return // an older hub; the cached value (default 1) stands
	}
	if h.Version != "" {
		f.hubVersion = h.Version
	}
	f.maxV = h.VMax()
	f.cacheHub()
}

func (f *flusher) cacheHub() {
	c, ok := ReadHubCache(f.stateDir)
	// A 200 reply: whatever the cache said, this hub is not frozen (Frozen is dropped).
	if ok && sameHub(c.URL, f.Client.BaseURL) && c.VMax() == f.maxV && (f.hubVersion == "" || c.Version == f.hubVersion) && c.Frozen == nil {
		return
	}
	nc := HubCache{URL: strings.TrimRight(f.Client.BaseURL, "/"), Version: f.hubVersion, RecordVMax: f.maxV, At: f.Now().UTC().Format(time.RFC3339)}
	if err := WriteHubCache(f.stateDir, nc); err != nil {
		f.Logf("hub.json: %v", err)
	}
}

// refreshHub asks /health what the hub reads, once per run. It reports whether the answer
// raised record_v_max, so the held records can go now.
func (f *flusher) refreshHub(ctx context.Context) bool {
	if f.refreshed {
		return false
	}
	f.refreshed = true
	h, err := f.cl.Health(ctx)
	if err != nil {
		return false
	}
	f.contact = true
	if h.Version != "" {
		f.hubVersion = h.Version
	}
	was := f.maxV
	f.maxV = h.VMax()
	f.cacheHub()
	return f.maxV > was
}

// hubRefuses is the guard before dropping a lone record that got 400/413: when nothing has
// been accepted in this run and the previous lone drop was recent too, the hub is refusing
// records in general, not this one. It then stops without dropping, stores a backoff and
// marks the status "hub refuses records". It returns true when the run must stop.
func (f *flusher) hubRefuses(se *StatusError) bool {
	if f.res.Accepted > 0 || f.cur.LoneDrops == 0 {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, f.cur.LoneDropAt)
	if err != nil || f.Now().Sub(at) >= loneDropWindow {
		return false
	}
	f.refuses = true
	f.failures++
	d := Backoff(f.failures, f.Rand())
	f.res.Err = fmt.Errorf("hub refuses records (answered %d to a single record again): %w", se.Status, se)
	f.Logf("%v; keeping the outbox, retry in %s", f.res.Err, d.Round(time.Millisecond))
	f.cur.Failures = f.failures
	f.cur.RetryAfter = f.Now().Add(d).UTC().Format(time.RFC3339Nano)
	if err := f.saveCursor(); err != nil {
		f.Logf("cursor: %v", err)
	}
	return true
}

// dropLone gives up on a single record the hub refused with 400/413 on its own.
func (f *flusher) dropLone(b *outbox.Batch, se *StatusError) {
	it := b.Items[0]
	reason := fmt.Sprintf("hub_%d", se.Status)
	if se.Code != "" {
		reason += "_" + se.Code
	}
	if se.Message != "" {
		reason += ": " + se.Message
	}
	if err := f.Outbox.LogRejected(it, reason); err != nil {
		f.Logf("rejected log: %v", err)
	}
	if it.Spill {
		_ = os.Remove(filepath.Join(f.Outbox.Dir, it.File))
	} else {
		f.cur.File, f.cur.Offset = b.Next.File, b.Next.Offset
	}
	f.res.Dropped++
	f.res.Rejected++
	f.cur.LoneDrops++
	f.cur.LoneDropAt = f.Now().UTC().Format(time.RFC3339Nano)
	if err := f.saveCursor(); err != nil {
		f.res.Err = err
	}
}

// retryLater handles a transient failure. In Wait mode it sleeps and returns true while
// the budget allows; otherwise it stores the backoff in the cursor and returns false.
func (f *flusher) retryLater(ctx context.Context, cause error, deadline time.Time) bool {
	f.failures++
	d := Backoff(f.failures, f.Rand())
	var se *StatusError
	if errors.As(cause, &se) && se.RetryAfter > 0 {
		d = max(d, min(se.RetryAfter, MaxBackoff))
	}
	f.Logf("flush failed (%d): %v; retry in %s", f.failures, cause, d.Round(time.Millisecond))
	if f.Wait && f.Now().Add(d).Before(deadline) {
		if err := f.Sleep(ctx, d); err != nil {
			f.res.Err = err
			return false
		}
		return true
	}
	f.res.Err = cause
	f.cur.Failures = f.failures
	f.cur.RetryAfter = f.Now().Add(d).UTC().Format(time.RFC3339Nano)
	if err := f.saveCursor(); err != nil {
		f.Logf("cursor: %v", err)
	}
	return false
}

// tooSlow tells a hub that could not answer a batch in time (a timeout, or a 5xx that does
// not ask to come back much later, as a frozen hub's 503 does) from other failures.
func tooSlow(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Status >= 500 && se.RetryAfter < time.Minute
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func (f *flusher) saveCursor() error {
	c := f.cur
	c.Updated = ""
	return f.Outbox.WriteCursor(c)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
