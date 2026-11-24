package ingest

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/record"
)

// switchMove keeps one move of the Smart Account Switch log (record.md 4.13). A move of a
// session that another machine owns is skipped (section 5.1): the machine that sent it must
// be the one that ran the session.
func (t *typer) switchMove(c *ctx) error {
	var p record.SwitchMovePayload
	if err := json.Unmarshal(c.r.Payload, &p); err != nil {
		return fmt.Errorf("switch_move payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if s, ok, err := t.tx.GetSession(p.SessionID); err != nil {
		return err
	} else if ok && s.MachineID != "" && s.MachineID != c.r.Machine {
		return fmt.Errorf("switch_move: session belongs to another machine")
	}
	at, _ := time.Parse(time.RFC3339, p.At)
	return t.tx.InsertSwitchMove(store.SwitchMoveInsert{RecordID: c.r.ID, Machine: c.r.Machine, At: at.UnixMilli(),
		SessionID: p.SessionID, SessionName: p.SessionName, Kind: p.Kind, Interactive: p.Interactive, From: p.From, To: p.To,
		Auto: p.Auto, Reason: p.Reason, Window: p.Window, P5: p.P5, P7: p.P7, Cap5: p.Cap5, Cap7: p.Cap7, Burn5: p.Burn5,
		CrossOrg: p.CrossOrg, Error: p.Error})
}
