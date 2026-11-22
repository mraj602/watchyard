package ingest

import (
	"encoding/json"
	"fmt"

	"github.com/mraj602/watchyard/internal/record"
)

// accountVerdicts keeps the newest verdict per seat (record.md 4.11): each account whose
// login names a seat, with the run's strategy and next_free. Other accounts are skipped.
func (t *typer) accountVerdicts(c *ctx) error {
	var p record.AccountVerdictsPayload
	if err := json.Unmarshal(c.r.Payload, &p); err != nil {
		return fmt.Errorf("account_verdicts payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return err
	}
	for _, a := range p.Accounts {
		seat, ok, err := t.tx.SeatByLogin(a.Account, a.Org)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		raw, err := storedVerdict(a, p.NextFree)
		if err != nil {
			return err
		}
		if err := t.tx.PutSeatVerdict(seat, c.r.Machine, c.at, p.Strategy, raw, c.r.ID); err != nil {
			return err
		}
	}
	return nil
}

// storedVerdict is seat_verdict.verdict: the account's item without its login (the seat
// names it), with the run's next_free.
func storedVerdict(a record.AccountVerdict, nextFree *string) ([]byte, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	delete(m, "account")
	delete(m, "org")
	if nextFree != nil {
		m["next_free"] = *nextFree
	}
	return json.Marshal(m)
}
