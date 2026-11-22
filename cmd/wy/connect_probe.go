package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/mraj602/watchyard/internal/account"
	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/record"
)

// probeToken proves the token with real records (tokens.md section 4): one slot_info per
// slot of this machine. A token bound to another machine gets machine_mismatch for them,
// which an empty batch could never show. With no slot to report it falls back to an empty
// batch (400 for a good token, 401/403 for a bad one), which stores nothing.
func probeToken(ctx context.Context, deps connectDeps, cl *client.Client) error {
	recs, err := probeRecords(deps)
	if err != nil {
		return fmt.Errorf("could not prepare the token check: %w", err)
	}
	reply, err := cl.Ingest(ctx, recs, 0)
	if err != nil {
		var se *client.StatusError
		switch {
		case errors.As(err, &se) && se.Status == http.StatusForbidden && se.Code == "machine_mismatch":
			return errMachineMismatch(deps)
		case errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden):
			return fmt.Errorf("the hub rejected this token (%d); it must be a machine or desktop token with the write scope", se.Status)
		case errors.As(err, &se) && se.Status == http.StatusBadRequest && len(recs) == 0:
			return nil // expected: the token passed, the empty batch did not
		}
		return fmt.Errorf("could not check the token: %w", err)
	}
	for _, r := range reply.Rejected {
		if r.Reason == "machine_mismatch" {
			return errMachineMismatch(deps)
		}
	}
	return nil
}

func errMachineMismatch(deps connectDeps) error {
	id := "?"
	if m, err := identity.LoadOrCreateMachine(deps.dirs.State.Path, identity.MachineOptions{}); err == nil {
		id = m.ID
	}
	return fmt.Errorf("this token belongs to another machine (this one is %s); make a token for it with `wy hub token create --kind machine --machine %s`", id, id)
}

func probeRecords(deps connectDeps) ([][]byte, error) {
	home := deps.timer.Home
	if home == "" {
		return nil, nil
	}
	var extra []string
	var labels, colours, modes map[string]string
	if f, err := config.Load(deps.dirs.Config.Path); err == nil {
		if cfg, err := f.Typed(); err == nil {
			extra, labels, colours, modes = cfg.Slots.Extra, cfg.Slots.Labels, cfg.Slots.Colours, cfg.Slots.Modes
		}
	}
	slots, err := (identity.Locator{Home: home, FS: identity.OSFS{}}).Discover(extra, deps.timer.Getenv)
	if err != nil || len(slots) == 0 {
		return nil, nil
	}
	m, err := identity.LoadOrCreateMachine(deps.dirs.State.Path, identity.MachineOptions{})
	if err != nil {
		return nil, err
	}
	store := &identity.Store{StateDir: deps.dirs.State.Path, FS: identity.OSFS{}}
	var out [][]byte
	for _, s := range slots {
		meta := record.Meta{Machine: m.ID, Slot: s.Name, Client: "wy/connect"}
		p := record.SlotInfoPayload{Folder: s.Name, Default: s.Default}
		if l, err := store.Login(s); err == nil && l.HasSeat {
			meta.Seat = &record.Seat{Account: l.Seat.Account, Org: l.Seat.Org, Email: l.Seat.Email, Src: l.Seat.Src}
			p.LoggedIn = true
		}
		// The labels, colours and modes wy setup wrote reach the hub here (setup.md 8.4, 8.5
		// Kind); an invalid hand-edited one is left out rather than failing the connect.
		if x, err := account.SlotInfoExtra(labels[s.Name], colours[s.Name], modes[s.Name]); err == nil && len(x) > 0 {
			p.Extra = x
		}
		r, err := record.NewSlotInfo(meta, p)
		if err != nil {
			return nil, err
		}
		b, err := r.Encode()
		if err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(b))
	}
	return out, nil
}
