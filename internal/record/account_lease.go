package record

import (
	"fmt"
	"math"
	"regexp"
	"time"
)

// KindAccountLease is one pick of `wy account next` (record.md 4.10, setup.md 8.5).
const KindAccountLease = "account_lease"

// Lane kinds of an account_lease.
const (
	LeaseAggressive = "aggressive"
	LeaseProtected  = "protected"
)

var leaseSessionID = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z_-]{0,63}$`)

// AccountLeasePayload is the payload of an account_lease record.
type AccountLeasePayload struct {
	LeaseID   string  `json:"lease_id"`
	SessionID string  `json:"session_id,omitempty"`
	Kind      string  `json:"kind"`
	C5        float64 `json:"c5"`
	CF        float64 `json:"cf"`
	Strategy  string  `json:"strategy"`
	Urgent    bool    `json:"urgent"`
	Model     string  `json:"model,omitempty"`
	Folder    string  `json:"folder,omitempty"`
	Batch     string  `json:"batch,omitempty"`
	// Drain and FinishBy: a drain pick and when its lane should finish (record.md 4.10).
	Drain    bool       `json:"drain,omitempty"`
	FinishBy *time.Time `json:"finish_by,omitempty"`
}

// Validate checks the fields record.md 4.10 requires.
func (p AccountLeasePayload) Validate() error {
	if !idPattern.MatchString(p.LeaseID) {
		return fmt.Errorf("account_lease: bad lease_id %q", p.LeaseID)
	}
	if p.SessionID != "" && !leaseSessionID.MatchString(p.SessionID) {
		return fmt.Errorf("account_lease: bad session_id %q", p.SessionID)
	}
	if p.Kind != LeaseAggressive && p.Kind != LeaseProtected {
		return fmt.Errorf("account_lease: bad kind %q", p.Kind)
	}
	for _, v := range []float64{p.C5, p.CF} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return fmt.Errorf("account_lease: bad charge %v", v)
		}
	}
	if p.Strategy == "" || len(p.Strategy) > 40 {
		return fmt.Errorf("account_lease: bad strategy %q", p.Strategy)
	}
	if len(p.Model) > 200 || len(p.Folder) > 4096 || len(p.Batch) > 64 {
		return fmt.Errorf("account_lease: a field is too long")
	}
	return nil
}

// NewAccountLease builds an account_lease record. The seat (the picked account) is required;
// the record has no slot.
func NewAccountLease(m Meta, p AccountLeasePayload) (*Record, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if m.Seat == nil || m.Seat.Account == "" || m.Seat.Org == "" {
		return nil, fmt.Errorf("account_lease: the seat is required")
	}
	m.Slot, m.Term = "", nil
	r := New(KindAccountLease, m)
	return r, r.SetPayload(p)
}
