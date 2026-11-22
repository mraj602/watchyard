// Package hook implements `wy hook`: read the hook JSON from stdin, apply the content
// rules, add machine, slot, seat and term, append one hook_event record to the outbox,
// and start a background flush. It always exits 0 and never prints a decision, so it
// cannot change what Claude Code does. Budget: 20 ms p95.
//
// Contract: docs/spec/record.md 4.1, docs/spec/states.md section 2.
//
// Depends on: record, outbox, identity, config, paths, leases.
package hook
