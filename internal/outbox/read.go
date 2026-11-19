package outbox

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/atomicfile"
)

const (
	logCap         = 5 << 20 // rejected.jsonl and corrupt.jsonl rotate past this
	corruptLineCut = 4096
	// TornSpillAge: a spill file with no trailing newline that has not been touched for this
	// long belongs to a writer that crashed mid-write; it is logged as corrupt and deleted.
	TornSpillAge = 10 * time.Minute
)

// Cursor is how far the hub has answered (outbox.md section 5): every record in day files
// before File, and every byte of File before Offset. The zero Cursor means "from the
// start". RetryAfter and Failures hold the flusher's backoff state.
type Cursor struct {
	V          int    `json:"v"`
	File       string `json:"file"`
	Offset     int64  `json:"offset"`
	Updated    string `json:"updated,omitempty"`
	RetryAfter string `json:"retry_after,omitempty"`
	Failures   int    `json:"failures,omitempty"`
	// LoneDrops counts the single records the flusher gave up on (hub answered 400/413 for
	// the record alone) with no record accepted in between; LoneDropAt is the time of the
	// last one. Together they tell a poison record from a hub that refuses everything.
	LoneDrops  int    `json:"lone_drops,omitempty"`
	LoneDropAt string `json:"lone_drop_at,omitempty"`
	// BatchMax, when above 0, caps the records per batch below the flusher's maximum: a
	// batch the hub could not answer in time (timeout or 5xx) halves it, each accepted batch
	// raises it by a quarter, and it is cleared once it reaches the maximum again (outbox.md 4).
	BatchMax int `json:"batch_max,omitempty"`
}

// Item is one complete, parsable line of the outbox.
type Item struct {
	ID     string
	TS     string // the record's ts, "" if absent
	V      int    // the record's v, 0 if absent or not an integer
	Raw    []byte // the line without its newline
	File   string // base name
	Offset int64  // start of the line in File
	End    int64  // offset just past its newline
	Spill  bool   // from a spill file (not covered by the cursor)
}

// Batch is what Read returns.
type Batch struct {
	Items   []Item
	Next    Cursor // the cursor to store once every item of day files has been answered
	Corrupt int    // lines skipped (and logged to corrupt.jsonl)
	More    bool   // true if a limit stopped the read before the end of the outbox
	// Held is set by ReadV: the number of records at and after the first one whose v is above
	// maxV (all of them are held back, in order, behind it); HeldNeeds is the highest v among
	// them. Zero when nothing is held or when a limit stopped the read before reaching it.
	Held      int
	HeldNeeds int
}

// ReadCursor returns the stored cursor, or the zero Cursor if there is none or it cannot be
// parsed (the worst case is resending records, which the hub dedupes).
func (o *Outbox) ReadCursor() Cursor {
	var c Cursor
	b, err := os.ReadFile(filepath.Join(o.Dir, "cursor.json"))
	if err != nil || json.Unmarshal(b, &c) != nil {
		return Cursor{V: 1}
	}
	if c.V == 0 {
		c.V = 1
	}
	return c
}

// WriteCursor stores c atomically: tmp file, fsync, rename, fsync of the folder.
func (o *Outbox) WriteCursor(c Cursor) error {
	c.V = 1
	if c.Updated == "" {
		c.Updated = o.now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return o.writeFileAtomic("cursor.json", append(b, '\n'))
}

func (o *Outbox) writeFileAtomic(name string, data []byte) error {
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return err
	}
	// A unique name: corrupt.marks.json is written by every process that reads the outbox,
	// not only the flusher, and two writers must not share one temp file.
	tmp := filepath.Join(o.Dir, atomicfile.TempName(name))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = syncFile(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(o.Dir, name)); err != nil {
		return err
	}
	return syncDir(o.Dir)
}

// Read returns up to maxRecords records (or maxBytes of lines; at least one record is
// returned if there is one) from the cursor onward, in outbox order: day files in name
// order, then spill files in name order (outbox.md section 4). It never takes the day-file
// lock and stops at the last newline of each file. Lines that do not parse, or have no id,
// are copied to corrupt.jsonl and skipped; a spill file that holds only such a line is
// deleted (it is a single record and the copy is in corrupt.jsonl).
func (o *Outbox) Read(cur Cursor, maxRecords, maxBytes int) (*Batch, error) {
	return o.read(cur, maxRecords, maxBytes, 0, true, true)
}

// ReadV is Read for a hub that reads record v up to maxV (machines.md 7.2): the read stops
// before the first record whose v is higher, as at the end of the outbox, so the batch
// holds only a prefix and Next stays before that record. Batch.Held counts the held records
// (the too-new one and everything after it, spill files included). maxV <= 0 means no limit.
func (o *Outbox) ReadV(cur Cursor, maxRecords, maxBytes, maxV int) (*Batch, error) {
	return o.read(cur, maxRecords, maxBytes, maxV, true, true)
}

func (o *Outbox) read(cur Cursor, maxRecords, maxBytes, maxV int, withSpills, logCorrupt bool) (*Batch, error) {
	days, spills, err := o.list()
	if err != nil {
		return nil, err
	}
	b := &Batch{Next: cur}
	b.Next.V = 1
	bytesRead := 0
	hold := func(it Item) {
		b.Held++
		b.HeldNeeds = max(b.HeldNeeds, it.V)
	}
	full := func(next int) bool {
		if len(b.Items) == 0 {
			return false
		}
		return (maxRecords > 0 && len(b.Items) >= maxRecords) || (maxBytes > 0 && bytesRead+next > maxBytes)
	}

	for _, name := range days {
		if name < cur.File {
			continue
		}
		var from int64
		if name == cur.File {
			from = cur.Offset
		}
		stopped := false
		err := o.readLines(name, from, func(ln line) bool {
			if b.Held > 0 {
				if !isBlank(ln.raw) {
					if it, ok := o.parse(name, ln, false, false); ok {
						hold(it)
					}
				}
				return true
			}
			if full(len(ln.raw)) {
				stopped = true
				return false
			}
			if isBlank(ln.raw) {
				b.Next.File, b.Next.Offset = name, ln.end
				return true
			}
			if it, ok := o.parse(name, ln, false, logCorrupt); ok {
				if maxV > 0 && it.V > maxV {
					hold(it)
					return true
				}
				b.Next.File, b.Next.Offset = name, ln.end
				b.Items = append(b.Items, it)
				bytesRead += len(ln.raw) + 1
			} else {
				b.Next.File, b.Next.Offset = name, ln.end
				b.Corrupt++
			}
			return true
		})
		if err != nil {
			return nil, err
		}
		if stopped {
			b.More = true
			return b, nil
		}
	}
	if !withSpills {
		return b, nil
	}
	for _, name := range spills {
		if b.Held == 0 && logCorrupt && o.reapTornSpill(name) {
			b.Corrupt++
			continue
		}
		stopped := false
		bad := 0
		err := o.readLines(name, 0, func(ln line) bool {
			if b.Held > 0 {
				if !isBlank(ln.raw) {
					if it, ok := o.parse(name, ln, true, false); ok {
						hold(it)
					}
				}
				return true
			}
			if full(len(ln.raw)) {
				stopped = true
				return false
			}
			if isBlank(ln.raw) {
				return true
			}
			if it, ok := o.parse(name, ln, true, logCorrupt); ok {
				if maxV > 0 && it.V > maxV {
					hold(it)
					return true
				}
				b.Items = append(b.Items, it)
				bytesRead += len(ln.raw) + 1
			} else {
				b.Corrupt++
				bad++
			}
			return true
		})
		if err != nil {
			return nil, err
		}
		if bad > 0 && logCorrupt {
			os.Remove(filepath.Join(o.Dir, name))
		}
		if stopped {
			b.More = true
			return b, nil
		}
	}
	return b, nil
}

type line struct {
	raw        []byte // without the newline
	start, end int64
}

// readLines calls fn for every complete line of the file from offset from, up to the file
// size seen when it started, and never for bytes after the last newline.
func (o *Outbox) readLines(name string, from int64, fn func(line) bool) error {
	f, err := os.Open(filepath.Join(o.Dir, name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // compacted or deleted since the listing
		}
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if from > st.Size() {
		from = 0 // the file was compacted or replaced: resend from its start
	}
	br := bufio.NewReaderSize(io.NewSectionReader(f, from, st.Size()-from), 64<<10)
	pos := from
	for {
		raw, err := br.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return nil // a partial last line belongs to a write in progress
			}
			return err
		}
		end := pos + int64(len(raw))
		if !fn(line{raw: raw[:len(raw)-1], start: pos, end: end}) {
			return nil
		}
		pos = end
	}
}

// parse turns a line into an Item, or logs it as corrupt.
func (o *Outbox) parse(name string, ln line, spill, logCorrupt bool) (Item, bool) {
	var head struct {
		ID string          `json:"id"`
		TS string          `json:"ts"`
		V  json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(ln.raw, &head); err != nil || head.ID == "" {
		if logCorrupt {
			o.logCorrupt(name, ln)
		}
		return Item{}, false
	}
	v, _ := strconv.Atoi(string(head.V))
	return Item{ID: head.ID, TS: head.TS, V: v, Raw: ln.raw, File: name, Offset: ln.start, End: ln.end, Spill: spill}, true
}

// isBlank is true for the empty line a torn-line repair can leave: nothing to log.
func isBlank(b []byte) bool { return len(trimSpace(b)) == 0 }

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// reapTornSpill deletes a spill file whose single record never got its newline (the writer
// died mid-write) once it is older than TornSpillAge, after copying it to corrupt.jsonl.
// readLines never returns bytes after the last newline, so such a file would otherwise stay
// for ever. Younger files may still be being written and are left alone.
func (o *Outbox) reapTornSpill(name string) bool {
	path := filepath.Join(o.Dir, name)
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 || o.now().Sub(st.ModTime()) < TornSpillAge {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || data[len(data)-1] == '\n' {
		return false
	}
	o.logCorrupt(name, line{raw: data, start: 0, end: int64(len(data))})
	return os.Remove(path) == nil
}

const marksFile = "corrupt.marks.json"

// loadMarks reads corrupt.marks.json once per Outbox value. Callers hold marksMu.
func (o *Outbox) loadMarks() {
	if o.marks != nil {
		return
	}
	o.marks = map[string]int64{}
	if b, err := os.ReadFile(filepath.Join(o.Dir, marksFile)); err == nil {
		_ = json.Unmarshal(b, &o.marks)
	}
}

func (o *Outbox) saveMarks() {
	if b, err := json.Marshal(o.marks); err == nil {
		_ = o.writeFileAtomic(marksFile, append(b, '\n'))
	}
}

// corruptSeen reports whether the corrupt line at ln was already copied to corrupt.jsonl by
// an earlier read, and if not, remembers it. A failed flush does not move the cursor, so
// without the memory the same corrupt line would be logged again on every retry. The memory
// is the end offset of the last logged line per day file, kept in corrupt.marks.json (every
// flush is a new process). Day files only grow, so offsets are comparable; compaction
// forgets the file's mark. Spill files are deleted once logged and need none.
func (o *Outbox) corruptSeen(name string, ln line) bool {
	if strings.HasPrefix(name, "spill-") {
		return false
	}
	o.marksMu.Lock()
	defer o.marksMu.Unlock()
	o.loadMarks()
	if end, ok := o.marks[name]; ok && ln.start < end {
		return true
	}
	o.marks[name] = ln.end
	o.saveMarks()
	return false
}

// forgetCorruptMark drops the mark of a day file that was rewritten.
func (o *Outbox) forgetCorruptMark(name string) {
	o.marksMu.Lock()
	defer o.marksMu.Unlock()
	o.loadMarks()
	if _, ok := o.marks[name]; ok {
		delete(o.marks, name)
		o.saveMarks()
	}
}

func (o *Outbox) logCorrupt(name string, ln line) {
	if o.corruptSeen(name, ln) {
		return
	}
	cut := ln.raw
	if len(cut) > corruptLineCut {
		cut = cut[:corruptLineCut]
	}
	entry, _ := json.Marshal(struct {
		File   string `json:"file"`
		Offset int64  `json:"offset"`
		Line   string `json:"line"`
	}{name, ln.start, string(cut)})
	_ = o.appendCapped("corrupt", entry)
}

// LogRejected records a record the hub rejected, with the reason, in rejected.jsonl.
func (o *Outbox) LogRejected(it Item, reason string) error {
	entry, _ := json.Marshal(struct {
		ID     string          `json:"id"`
		Reason string          `json:"reason"`
		Time   string          `json:"time"`
		Record json.RawMessage `json:"record"`
	}{it.ID, reason, o.now().UTC().Format("2006-01-02T15:04:05.000Z"), it.Raw})
	return o.appendCapped("rejected", entry)
}

// appendCapped appends one line to <base>.jsonl, first rotating it to <base>.1.jsonl once it
// is over 5 MB. Entries are small, so one O_APPEND write per entry does not interleave.
func (o *Outbox) appendCapped(base string, entry []byte) error {
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(o.Dir, base+".jsonl")
	if st, err := os.Stat(path); err == nil && st.Size() > logCap {
		_ = os.Rename(path, filepath.Join(o.Dir, base+".1.jsonl"))
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(entry, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Stats summarises the outbox for `wy outbox stat` and `wy doctor`.
type Stats struct {
	DayFiles        int       `json:"day_files"`
	SpillFiles      int       `json:"spill_files"`
	Bytes           int64     `json:"bytes"`            // every day and spill file
	UnflushedBytes  int64     `json:"unflushed_bytes"`  // past the cursor, plus spill files
	OldestUnflushed time.Time `json:"oldest_unflushed"` // ts of the oldest unflushed record; zero if none
	Cursor          Cursor    `json:"cursor"`
}

// Stat measures the outbox without changing it.
func (o *Outbox) Stat() (Stats, error) {
	days, spills, err := o.list()
	if err != nil {
		return Stats{}, err
	}
	cur := o.ReadCursor()
	s := Stats{DayFiles: len(days), SpillFiles: len(spills), Cursor: cur}
	size := func(name string) int64 {
		st, err := os.Stat(filepath.Join(o.Dir, name))
		if err != nil {
			return 0
		}
		return st.Size()
	}
	for _, n := range days {
		sz := size(n)
		s.Bytes += sz
		switch {
		case n > cur.File:
			s.UnflushedBytes += sz
		case n == cur.File && sz > cur.Offset:
			s.UnflushedBytes += sz - cur.Offset
		}
	}
	for _, n := range spills {
		sz := size(n)
		s.Bytes += sz
		s.UnflushedBytes += sz
	}
	b, err := o.read(cur, 1, 0, 0, false, false)
	if err != nil {
		return s, err
	}
	var oldest time.Time
	consider := func(it Item) {
		if t, err := time.Parse(time.RFC3339Nano, it.TS); err == nil && (oldest.IsZero() || t.Before(oldest)) {
			oldest = t
		}
	}
	for _, it := range b.Items {
		consider(it)
	}
	for _, n := range spills {
		_ = o.readLines(n, 0, func(ln line) bool {
			if it, ok := o.parse(n, ln, true, false); ok {
				consider(it)
				return false
			}
			return true
		})
	}
	s.OldestUnflushed = oldest
	return s, nil
}
