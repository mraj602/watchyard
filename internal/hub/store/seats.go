package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
)

// seat.look_src values: who set a seat's label and colour (desktop.md C10, schema v5).
const (
	LookUI     = "ui"     // PATCH /v1/accounts/{seat}: always wins
	LookSlot   = "slot"   // a slot_info label: applies only while look_src is NULL or 'slot'
	LookImport = "import" // rows imported by earlier versions: still loads, nothing writes it now
)

var (
	seatLabelRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)
	seatColourRE = regexp.MustCompile(`^#[0-9a-f]{6}$`)
)

// ValidSeatLabel and ValidSeatColour are the rules of setup.md 3 (and api.yaml SeatPatch).
func ValidSeatLabel(s string) bool  { return seatLabelRE.MatchString(s) }
func ValidSeatColour(s string) bool { return seatColourRE.MatchString(s) }

// ErrBadSeatPatch is returned (wrapped) for an invalid SeatPatch.
var ErrBadSeatPatch = errors.New("invalid seat patch")

// SeatPatch is api.yaml SeatPatch. LabelSet and ColourSet are true when the key was
// present; a nil value with the flag set clears label or colour.
type SeatPatch struct {
	LabelSet  bool
	Label     *string
	ColourSet bool
	Colour    *string
	Order     *int64
	Hidden    *bool
	ModeSet   bool    // the mode key was present (setup.md 8.5, Kind)
	Mode      *string // protected, aggressive, off; nil with ModeSet clears it (not chosen)
	// DrainSet: the drain_until key was present; DrainUntil the owner's drain end (ms), nil
	// to stop it (setup.md 8.5, Owner drain). NowMS: the hub's clock, for "in the future".
	DrainSet   bool
	DrainUntil *int64
	NowMS      int64
}

// Seat pick modes (seat.pick_mode, schema v20; setup.md 8.5, Kind).
const (
	PickProtected  = "protected"
	PickAggressive = "aggressive"
	// PickOff (schema v25, seat.pick_off): never picked, nothing moved into it.
	PickOff = "off"
)

// ValidPickMode reports whether s is a seat pick mode a slot_info may carry.
func ValidPickMode(s string) bool { return s == PickProtected || s == PickAggressive }

// MaxDrainAhead is how far ahead a drain may end.
const MaxDrainAhead = 8 * 24 * 60 * 60 * 1000

// Validate applies the label and colour rules. It needs at least one key.
func (p SeatPatch) Validate() error {
	if !p.LabelSet && !p.ColourSet && p.Order == nil && p.Hidden == nil && !p.ModeSet && !p.DrainSet {
		return fmt.Errorf("%w: no field to change", ErrBadSeatPatch)
	}
	if p.Label != nil && !ValidSeatLabel(*p.Label) {
		return fmt.Errorf("%w: label must match ^[a-z0-9][a-z0-9-]{0,23}$", ErrBadSeatPatch)
	}
	if p.Colour != nil && !ValidSeatColour(*p.Colour) {
		return fmt.Errorf("%w: colour must be #rrggbb in lower case", ErrBadSeatPatch)
	}
	if p.Order != nil && (*p.Order < 0 || *p.Order > 1_000_000) {
		return fmt.Errorf("%w: order out of range", ErrBadSeatPatch)
	}
	if p.Mode != nil && !ValidPickMode(*p.Mode) && *p.Mode != PickOff {
		return fmt.Errorf("%w: mode must be protected, aggressive, off or null", ErrBadSeatPatch)
	}
	if p.DrainUntil != nil {
		if *p.DrainUntil <= p.NowMS || *p.DrainUntil > p.NowMS+MaxDrainAhead {
			return fmt.Errorf("%w: drain_until must be in the next 8 days", ErrBadSeatPatch)
		}
		if p.Mode != nil && *p.Mode == PickOff {
			return fmt.Errorf("%w: an account that is off cannot drain", ErrBadSeatPatch)
		}
	}
	return nil
}

// PatchSeat applies p to the seat; touching label or colour sets look_src to 'ui', touching
// mode sets pick_mode_src to 'ui'. It returns ErrNotFound for an unknown seat.
func (tx *Tx) PatchSeat(id string, p SeatPatch) error {
	if err := p.Validate(); err != nil {
		return err
	}
	var one int
	if err := tx.queryRow(`SELECT 1 FROM seat WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if p.LabelSet {
		if _, err := tx.exec(`UPDATE seat SET label = ?, look_src = 'ui' WHERE id = ?`, p.Label, id); err != nil {
			return err
		}
	}
	if p.ColourSet {
		if _, err := tx.exec(`UPDATE seat SET colour = ?, look_src = 'ui' WHERE id = ?`, p.Colour, id); err != nil {
			return err
		}
	}
	switch {
	case p.ModeSet && p.Mode != nil && *p.Mode == PickOff:
		// Off keeps pick_mode for when it is turned on again, and ends a drain.
		if _, err := tx.exec(`UPDATE seat SET pick_off = 1, drain_until = NULL WHERE id = ?`, id); err != nil {
			return err
		}
	case p.ModeSet:
		if _, err := tx.exec(`UPDATE seat SET pick_mode = ?, pick_mode_src = 'ui', pick_off = 0 WHERE id = ?`, p.Mode, id); err != nil {
			return err
		}
	}
	if p.DrainSet {
		if p.DrainUntil != nil && !p.ModeSet {
			var off int
			if err := tx.queryRow(`SELECT pick_off FROM seat WHERE id = ?`, id).Scan(&off); err != nil {
				return err
			}
			if off == 1 {
				return fmt.Errorf("%w: the account is off; turn it on before draining it", ErrBadSeatPatch)
			}
		}
		if _, err := tx.exec(`UPDATE seat SET drain_until = ? WHERE id = ?`, p.DrainUntil, id); err != nil {
			return err
		}
	}
	if p.Order != nil {
		if _, err := tx.exec(`UPDATE seat SET sort_order = ? WHERE id = ?`, *p.Order, id); err != nil {
			return err
		}
	}
	if p.Hidden != nil {
		h := 0
		if *p.Hidden {
			h = 1
		}
		if _, err := tx.exec(`UPDATE seat SET hidden = ? WHERE id = ?`, h, id); err != nil {
			return err
		}
	}
	return nil
}

// seatHiddenSQL is the hidden state of a seat `se`, as an SQL expression (0 or 1): the owner's
// hide, or the seat is a stray free org of an account that has a paid one (identity.md 5.1).
// Every claude.ai account has a personal free org beside its Team org, and a desktop app that
// reported every org made a second seat for the same person. Such a seat is hidden while it
// has no label (nobody named it), no slot was ever logged in to it (nobody uses it in Claude
// Code) and its account has another seat whose org is not free. A label or a slot login brings
// it back, so a free org that is really used is never hidden.
const seatHiddenSQL = `(se.hidden = 1 OR (se.label IS NULL
	AND EXISTS (SELECT 1 FROM org fo WHERE fo.uuid = se.org_uuid AND fo.plan = 'free')
	AND NOT EXISTS (SELECT 1 FROM slot_login sl WHERE sl.seat_id = se.id)
	AND EXISTS (SELECT 1 FROM seat s2 JOIN org o2 ON o2.uuid = s2.org_uuid
		WHERE s2.account_uuid = se.account_uuid AND s2.id <> se.id AND coalesce(o2.plan, 'unknown') <> 'free')))`

// SeatSlotLook applies a slot_info label and colour (record.md 4.7) only while the seat's
// look_src is NULL or 'slot', and then sets look_src to 'slot'. An invalid or empty value is
// left alone. It reports whether anything was applied.
func (tx *Tx) SeatSlotLook(seatID, label, colour string) (bool, error) {
	if !ValidSeatLabel(label) {
		label = ""
	}
	if !ValidSeatColour(colour) {
		colour = ""
	}
	if label == "" && colour == "" {
		return false, nil
	}
	res, err := tx.exec(`UPDATE seat SET label = coalesce(?, label), colour = coalesce(?, colour), look_src = 'slot'
		WHERE id = ? AND (look_src IS NULL OR look_src = 'slot')`, nullStr(label), nullStr(colour), seatID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SeatSlotMode applies a slot_info mode (record.md 4.7) only while nobody has chosen for the
// seat (pick_mode and pick_mode_src NULL: an owner's Not chosen keeps pick_mode_src 'ui'), and
// then sets pick_mode_src to 'slot'. Any other value is ignored. It reports whether it was
// applied.
func (tx *Tx) SeatSlotMode(seatID, mode string) (bool, error) {
	if !ValidPickMode(mode) {
		return false, nil
	}
	res, err := tx.exec(`UPDATE seat SET pick_mode = ?, pick_mode_src = 'slot' WHERE id = ? AND pick_mode IS NULL AND pick_mode_src IS NULL`, mode, seatID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// DrainRow is a seat the owner drains (seat.drain_until set; schema v25).
type DrainRow struct {
	ID    string
	Until int64 // ms
}

// DrainingSeats lists the seats with a drain_until, ended ones too (the engine ends them).
func (tx *Tx) DrainingSeats() ([]DrainRow, error) {
	rows, err := tx.QueryContext(tx.ctx, `SELECT id, drain_until FROM seat WHERE drain_until IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DrainRow
	for rows.Next() {
		var r DrainRow
		if err := rows.Scan(&r.ID, &r.Until); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EndDrain clears a seat's drain_until when it is still until (the owner may have set
// another meanwhile). It reports whether it did.
func (tx *Tx) EndDrain(id string, until int64) (bool, error) {
	res, err := tx.exec(`UPDATE seat SET drain_until = NULL WHERE id = ? AND drain_until = ?`, id, until)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
