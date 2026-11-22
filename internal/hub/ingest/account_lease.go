package ingest

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
)

// accountLease stores one pick of `wy account next` (record.md 4.10) once per lease id.
func (t *typer) accountLease(c *ctx) error {
	var p record.AccountLeasePayload
	if err := json.Unmarshal(c.r.Payload, &p); err != nil {
		return fmt.Errorf("account_lease payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if c.seat == "" {
		return errors.New("account_lease without seat")
	}
	return t.tx.InsertAccountLease(store.AccountLease{ID: p.LeaseID, Seat: c.seat, Machine: c.r.Machine, TS: c.at,
		SessionID: p.SessionID, Kind: p.Kind, C5: p.C5, CF: p.CF, Strategy: p.Strategy, Urgent: p.Urgent,
		Model: p.Model, RecordID: c.r.ID})
}
