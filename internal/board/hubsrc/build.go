package hubsrc

import (
	"time"

	"github.com/mraj602/watchyard/internal/board"
)

// BuildInput is what a hub snapshot builder is given: the hub's reads, in the hub's order.
type BuildInput struct {
	Now         time.Time
	Loc         *time.Location
	Machine     board.Machine
	ThisSession string
	Hub         board.HubInfo
	Source      string
	// IncludeEnded keeps ended and gone sessions of the last board.EndedWithin (the TUI's
	// `e` toggle hides them by default).
	IncludeEnded bool
	// WarpColour maps a seat colour to Warp's colour name (board.Options.WarpColour).
	WarpColour func(hex string) string
	// Drains and Offs: board.Options.Drains and Offs.
	Drains func() map[string]time.Time
	Offs   func() map[string]bool
	// Seats are /v1/accounts in the hub's order; Sessions are /v1/sessions in the hub's
	// order; MachineCount is len(/v1/overview machines).
	Seats        []wireSeat
	Sessions     []wireSession
	MachineCount int
}

// BuildFunc turns a hub snapshot into a View. Options.Build may replace DefaultBuild.
type BuildFunc func(BuildInput) board.View

// DefaultBuild is the hub half of T2: the wire reads are handed to board.BuildHub, the
// one builder both sources share (words, text safety, order and the T2 caps all live
// there), so the hub board and the local board cannot disagree on a word.
func DefaultBuild(in BuildInput) board.View {
	d := board.HubData{
		Overview: board.HubOverview{Time: in.Now, Machines: make([]board.HubMachine, in.MachineCount)},
	}
	for _, s := range in.Seats {
		d.Seats = append(d.Seats, board.HubSeat{ID: s.ID, Email: s.Email, Label: s.Label, Colour: s.Colour,
			Order: s.Order, Hidden: s.Hidden, Windows: hubWindows(s.Windows), Mode: s.Mode, DrainUntil: s.DrainUntil})
	}
	for _, s := range in.Sessions {
		since := s.StateSince
		d.Sessions = append(d.Sessions, board.HubSession{
			ID: s.ID, State: s.State, Detail: s.Detail, NeedsYou: s.NeedsYou, StateSince: &since,
			Question: s.Question, LastMessage: s.LastMessage, Seat: s.Seat, SeatLabel: s.SeatLabel,
			SeatColour: s.SeatColour, Machine: s.Machine, MachineName: s.MachineName, Project: s.Project,
			Cwd: s.Cwd, Name: s.Name, Title: s.Title, ContextPct: s.ContextPct, LastEventAt: s.LastEventAt,
			RemoteURL: s.RemoteURL, Resume: s.Resume, FocusURL: s.FocusURL, Stale: s.Stale,
			MachineStale: s.MachineStale, Tool: s.Tool,
		})
	}
	// needs_you: nil lets the builder order it oldest waiting first from the rows, which
	// follow the live stream; the overview's list is only as fresh as the last fetch.
	v := board.BuildHub(d, board.Options{
		Now: in.Now, Loc: in.Loc, Machine: in.Machine, SessionID: in.ThisSession,
		IncludeEnded: in.IncludeEnded, Hub: in.Hub, WarpColour: in.WarpColour, Drains: in.Drains, Offs: in.Offs,
	})
	if in.Source != "" {
		v.Source = in.Source
	}
	if v.Counts.Machines == 0 {
		v.Counts.Machines = 1
	}
	return v
}

func hubWindows(ws []wireWindow) []board.HubWindow {
	var out []board.HubWindow
	for _, w := range ws {
		out = append(out, board.HubWindow{Key: w.Key, UsedPct: w.UsedPct, ResetsAt: w.ResetsAt})
	}
	return out
}
