# Outbox

Status: spec v1. Implemented in `internal/outbox`; flushed by
`internal/client`.

The outbox is where every record waits until the hub has accepted it. It must survive a
crash at any point, never block Claude Code, and cope with many `wy hook` processes writing
at the same moment (one per hook event, across every session and account on the machine).

## 1. Files

All under `<state>/outbox/` ([paths.md](paths.md)). The folder is mode `0700`, files `0600`.

| File | What |
| --- | --- |
| `YYYY-MM-DD.jsonl` | Records written on that **UTC** day, one per line, in write order. Append-only. |
| `spill-<pid>-<ulid>.jsonl` | One record that could not get the day file's lock in time (section 3, step 4). |
| `cursor.json` | How far the hub has acknowledged (section 5). |
| `flush.lock` | Held by the one running flusher. Empty file. |
| `cap.stamp` | Mtime of the last size-cap check made by `wy flush` when no hub is configured (at most every 10 minutes). |
| `flush.stamp` | Its mtime is the last flush start, for debouncing. Empty file. `wy hook` and the flusher both touch it; the hook starts a flush only if it is older than 2 s. |
| `flush.status.json` | The last flush run's outcome for `wy doctor`: `time`, `ok`, `error`, `auth_failed` ("token rejected"), `refuses_records` ("hub refuses records"), `last_success`, `token_fp`. |
| `rejected.jsonl` | Records the hub rejected, with the reason. Kept for `wy doctor`; capped at 5 MB, then rotated to `rejected.1.jsonl`. |
| `corrupt.jsonl` | Lines that could not be parsed, with file and offset. Same cap. Each corrupt line is logged once: `corrupt.marks.json` remembers, per day file, the end offset of the last one logged, so a failed flush that re-reads the line does not log it again (compaction forgets the file's mark). A spill file with no trailing newline and untouched for 10 minutes (`TornSpillAge`) is a crashed writer's leftover: it is copied here and deleted. |

## 2. Line format

- One record ([record.md](record.md)) encoded as compact JSON, UTF-8, followed by one `\n`.
  The JSON encoder escapes newlines inside strings, so a record is always exactly one line.
- At most 1 MiB per line including the `\n`. Larger records are refused by the writer, and
  the producer (only `wy scan`) splits them.
- No header, no checksums: JSON parse failure is the integrity check, and the record id
  makes resending safe.

## 3. Writing (one record, many concurrent writers)

**Decision: an exclusive advisory lock on the day file itself, held only for one write.**
`O_APPEND` alone is not enough: POSIX only promises atomic appends up to `PIPE_BUF` for
pipes, not regular files, and our lines can exceed 4 KiB (Stop events with metadata,
`session_list`, `token_rows`). Linux local file systems do serialise appends in practice, but
NFS homes, macOS and Windows give weaker promises. The lock costs a few microseconds and also
lets a writer repair a torn last line safely. A separate lock file is not needed, because the
lock is taken on the file being appended to.

Steps for `outbox.Append(record)`:

1. Encode the record and append `\n`. If over 1 MiB, return an error.
2. `path` = `<state>/outbox/<day>.jsonl`, where `<day>` is the UTC date of now, or the
   newest existing day file's date if that is later (a clock that jumps backwards keeps
   writing to the newest file, so files never go back in time behind the cursor). Finding
   the newest file is one `readdir` of a small folder.
3. Open `path` with `O_RDWR | O_APPEND | O_CREATE | O_CLOEXEC`, mode `0600` (`O_RDWR`, not
   `O_WRONLY`: step 6 reads the last byte).
4. Take an exclusive lock: `flock(fd, LOCK_EX | LOCK_NB)` on Unix, `LockFileEx` with
   `LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY` on Windows (locking one byte at
   offset 2^62, far beyond the data, because Windows locks are mandatory and the lock-free
   readers of section 4 must still read the file), retrying every 1 ms.
   If the lock is not taken within **50 ms**, give up on the day file and write the record
   to a new `spill-<pid>-<ulid>.jsonl` instead (created with `O_EXCL`, no lock needed),
   then go to step 8. This keeps `wy hook` from ever waiting long on a stuck process.
5. Compare `fstat(fd)` with `stat(path)` (device and inode on Unix, file index on Windows).
   If they differ, the file was replaced by compaction (section 6): unlock, close, go to 3.
6. If the file is not empty, read its last byte (`pread` at size − 1). If it is not `\n`, a
   previous writer was cut off mid-line (power loss): write a single `\n` first (in the same `write()` as the line, so the two cannot be
   separated), so the torn
   fragment becomes its own (corrupt) line instead of spoiling this record.
7. `write()` the whole line. On a short write, keep writing the rest (still under the lock,
   so nothing can interleave).
8. Unlock. Then `fdatasync(fd)` (Linux), `fsync(fd)` (macOS; Go's `File.Sync` issues `F_FULLFSYNC` there, so the code calls `syscall.Fsync`; not `F_FULLFSYNC`, which costs
   tens of ms), `FlushFileBuffers` (Windows). When the file was just created, also fsync the
   `outbox/` folder once so the new name is durable.
9. Close. Return the record id.

Durability: after step 8 returns, the record survives a crash of `wy`, of Claude Code and
of the OS. `config.toml: outbox.fsync = false` skips step 8's sync for slow disks; the
record then survives a process crash but maybe not a power cut.

Budget: steps 1–9 must take under 5 ms p95 on an SSD, so `wy hook` fits in 20 ms.

Other writers: the desktop app does not write the files itself. It runs
`wy outbox append` with records on stdin (one JSON value per line, or any run of JSON
values; `v`, `id` and `ts` are filled in when missing; one id is printed per record), so there is one implementation of
these rules.

## 4. Reading

- Readers never take the day-file lock. They read only up to the last `\n`; bytes after it
  belong to a write in progress (or a torn line, repaired by the next writer).
- A line that does not parse as JSON, or parses but has no `id`, is copied to
  `corrupt.jsonl` as `{"file", "offset", "line"}` (line cut to 4 KiB) and skipped.
- Order: day files in name order, then spill files in name order. Within a file, line order.
  The hub does not rely on this order (it orders by `ts` per session), but flushing in this
  order keeps hook events roughly in sequence.
- `wy status` reads the last 7 days of day files (flushed or not) to build the
  local view.

## 5. Flushing and the cursor

Only one flusher runs per machine: it takes `flush.lock` with a non-blocking exclusive lock
and exits at once if another process holds it.

`cursor.json`:

```json
{"v": 1, "file": "2026-10-12.jsonl", "offset": 183402, "updated": "2026-10-12T14:03:24.120Z"}
```

Meaning: every record in day files before `file`, and every byte of `file` before `offset`,
has been answered by the hub (accepted or rejected). Spill files are not covered by the
cursor: a spill file is deleted as soon as its one record is answered.

Flush loop:

1. Read records from the cursor onward, up to **500 records (or `batch_max`, step 4) or 4 MiB**, whichever first, plus
   any spill files.
2. `POST /v1/ingest` with the batch (gzip when over 1 KiB).
3. On `200`: the reply lists every id as accepted or rejected. Append the rejected ones with
   their reason to `rejected.jsonl`. Delete answered spill files. Write the new cursor
   (just past the last answered line) atomically: write `cursor.json.tmp`, fsync, rename,
   fsync the folder. If the reply misses an id that was sent, treat the batch as failed
   (step 4) and log it; this is a hub bug.
4. On a network error, `429`, or `5xx`: stop, keep the cursor, and retry on the next trigger
   with backoff (1 s, 2 s, 4 s … up to 5 min, reset after a success). The backoff state is
   kept in `cursor.json` (`retry_after`, `failures`).
   When the failure says the hub could not answer the batch in time (a timeout, or a `5xx`
   whose `Retry-After` is under a minute, so not a frozen hub) and the batch had several
   records, the batch limit is halved and kept in `cursor.json` as `batch_max`: one attempt
   can use a whole run's budget, so the smaller size must outlive the run. Each accepted
   batch raises `batch_max` by a quarter (at least 1); it is removed once it reaches the
   500-record limit again. Without this, a hub that takes longer than the client timeout
   for one full batch (a slow host, a large backlog) never receives anything again.
5. On `401`/`403`: stop and record the error for `wy doctor` ("token rejected"). Do not
   retry until the token changes or `wy flush` is run by hand.
6. On `400`/`413` for a batch of several records, halve the batch and retry, down to one
   record. A **lone** record that still gets 400/413 is a poison record: log it to
   `rejected.jsonl` and drop it (the cursor moves past it), **but only when the hub has
   accepted other records in the same run, or when no other lone drop happened in the last
   hour.** `cursor.json` carries `lone_drops` and `lone_drop_at` (consecutive lone drops with
   no record accepted in between; cleared by any accepted record). When a lone record gets
   400/413 with nothing accepted in this run and the previous lone drop was less than an hour
   ago, the hub or a proxy in front of it is refusing everything: nothing more is dropped,
   a backoff is stored (as in step 4) and `flush.status.json` gets `refuses_records: true`
   (`wy doctor`: "hub refuses records"). At most 10 records are dropped per run in any case.
7. Repeat until the outbox is drained or 30 seconds have passed.
8. A flush started by `wy hook` (`--from-hook`), once drained, waits until `flush.stamp` is
   2 seconds old and reads the outbox again, repeating while that finds records (within the
   30 seconds). The hooks that wrote while the stamp was younger started no flush of their
   own (below), so without this their records (say a Stop a second after a SubagentStop)
   would wait for the timer. The timer's flush and `wy flush` by hand do not wait.

A crash between the hub's reply and the cursor write just resends the batch; the hub reports
the ids as accepted duplicates.

Triggers: after each `wy hook` write, `wy hook` starts `wy flush --quiet` as a detached
process (new session, stdio closed) unless `flush.stamp` is younger than 2 seconds; and the
OS timer runs `wy tick` (flush + `wy agents` + `wy scan`) every minute. Without a
hub, `wy flush` is a no-op and the cursor stays at the start.

## 6. Retention, size cap and compaction

- **Flushed files** (wholly before the cursor) are deleted after `outbox.keep_days` days
  (default 7), so `wy status` keeps a local week of history.
- **No hub configured:** `wy flush` sends nothing, but still runs the size cap (not the
  pruning: there is no cursor), at most once per 10 minutes (`cap.stamp`) and only if it can
  take `flush.lock`, so the outbox of a machine that never connects stays bounded.
- **Size cap:** when the unflushed part of the outbox exceeds
  `outbox.max_mb` (default 50 MB), the flusher compacts the oldest unflushed day files,
  dropping records in this order until under the cap:
  1. `session_list` older than 1 hour (only the newest list per slot matters).
  2. `limit_sample` (keeping the newest per session per hour).
  3. `history_import` (it is re-sent by `wy scan --history`).

  `hook_event`, `token_rows`, `slot_info`, `web_usage` and kinds this version does not know
  are never dropped. `token_rows` in particular: `wy scan` commits its transcript offsets
  as soon as the rows are in the outbox, so a dropped `token_rows` record could never be
  re-read and its usage would be lost for good. (Dropping them and rolling the scan offsets
  back was considered and rejected: a record's `source` range is optional debugging data and
  covers only single-file batches.) If the never-dropped kinds alone exceed the cap, writing
  continues and `wy doctor` reports the backlog.
- **Compaction of a file** (only by the flusher, holding `flush.lock`): take the file's
  exclusive lock (waiting, since writers hold it only for one write), write the kept lines
  of the unflushed part to `<name>.compact.tmp`, fsync, **set the cursor to `offset 0` of
  that file if the cursor was in it (written and fsynced first)**, rename over the file, fsync
  the folder, unlock. The cursor goes first because a crash between the two steps must at
  worst resend records the hub already answered (it dedupes by id); in the other order the
  old offset would point into the shorter file and skip records. Writers that opened the old
  file see the inode change at step 5 and reopen.
- Today's file is never compacted.

## 7. Crash safety, case by case

| Crash point | Result |
| --- | --- |
| Before the write | Record never existed; the hook event is lost. Accepted: the hook process itself died. |
| During the write (torn line) | Next writer adds `\n`; the fragment goes to `corrupt.jsonl`. Only that record is lost. |
| After write, before fsync, power cut | Record may be lost (it was not yet durable). |
| After fsync | Record is safe. |
| During cursor write | Old cursor stays (rename is atomic). Batch is resent; hub dedupes. |
| During compaction | The tmp file is ignored and deleted next time; the original is untouched until the rename. |
| Clock jump backwards | Writers keep using the newest day file (section 3, step 2), so nothing lands behind the cursor. Record `ts` values may go backwards; the hub orders by `ts` and copes. |

## 8. Pull mode

`wy outbox drain --stdio` is the remote end of the hub pulling over SSH; the protocol is
[machines.md](machines.md) 3.5. It replaces the reserved `--since-cursor` / `--ack <file:offset>`
flags of earlier drafts: an acknowledgement is the hub's ingest reply for one batch, sent back
over the same SSH channel, so the cursor rules are exactly section 5's:

- The drainer is a flusher: it takes `flush.lock`, reads with the same `Read` (day files, then
  spill files, the same batch limits), and applies each acknowledgement with the same function
  as `wy flush` (rejected log, answered spill files deleted, cursor past the longest answered
  prefix of day-file records, written atomically). One batch is unacknowledged at a time.
- It stops before the first record whose `v` is above the hub's `record_v_max` (machines.md
  7.2), as at the end of the outbox.
- It writes `flush.status.json` with `via: "pull"`, `last_contact` and `last_success`, so
  `wy doctor` and direct push (machines.md 5.4) see when the hub last pulled.
- The size cap and pruning run from `wy tick` as before; they need `flush.lock`, so they never
  run during a drain.

`wy outbox drain --peek [--max N]` prints the next records as JSONL for people and changes
nothing.

Pull mode also adds to `flush.status.json`: `last_contact` (the last successful exchange with the hub: a `200`
ingest reply, a `200` `/health`, or a drain `start`; a `401`/`403`/`5xx` is not contact, since
the hub is not taking this machine's records), `held` and
`hub_too_old` (machines.md 7.2), `via` (`push` or `pull`), and `pull_refused` (3.5 `refuse`
code). The flusher sends `X-Wy-Backlog-Oldest` (the `ts` of the oldest unanswered record)
with `X-Wy-Backlog` whenever the backlog is more than one batch.
