package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/mover"
)

// laneNotice is one carried-out move, for the notice to the session that started it.
type laneNotice struct {
	Parent, Session, Name string
	From, To, ToDir       string
	At                    time.Time
	Move                  mover.Move
	Old, New              string // peers handles before and after (filled by tell)
}

// noticeWait is how long tell waits for the moved sessions to show up again under their new
// account (their new handles), at most.
var noticeWait = 30 * time.Second

// tell sends each parent one notice for all its lanes that moved in this pass (setup.md 8.6,
// Who is told): no polling on its side, one short line per lane. Peers must be on; a parent
// that has ended is skipped. The handles come from `wy peers list --all` (the agent's, so the
// hub's canonical names), before the move from w and after it once the session runs again.
func (w *moveWorld) tell(e moveEnv, ns []laneNotice) {
	byParent := noticesByParent(ns)
	if len(byParent) == 0 {
		return
	}
	pe, err := peersEnv()
	if err != nil || !pe.Enabled {
		fmt.Fprintln(e.stdout, "parent not told: peers are off (wy peers enable)")
		return
	}
	want := map[string]string{} // session -> its new folder
	for _, n := range ns {
		want[n.Session] = n.ToDir
	}
	handles := w.handlesAfter(pe, want)
	parents := make([]string, 0, len(byParent))
	for p := range byParent {
		parents = append(parents, p)
	}
	sort.Strings(parents)
	for _, p := range parents {
		lanes := byParent[p]
		for i := range lanes {
			lanes[i].Old, lanes[i].New = w.handles[lanes[i].Session], handles[lanes[i].Session]
		}
		text := noticeText(lanes)
		h, rf := pe.Notice(p, text)
		if rf != nil {
			fmt.Fprintf(e.stdout, "parent %s not told: %s\n", short(p), rf.Detail)
			continue
		}
		fmt.Fprintf(e.stdout, "told %s: %s\n", h, text)
	}
}

// noticesByParent groups the moves by the session that started each one: the parent recorded
// in its lease (wy account next --exec --parent, or the launching session). A move with no
// recorded parent tells nobody; no other session is ever told.
func noticesByParent(ns []laneNotice) map[string][]laneNotice {
	byParent := map[string][]laneNotice{}
	for _, n := range ns {
		if n.Parent != "" && n.Parent != n.Session {
			byParent[n.Parent] = append(byParent[n.Parent], n)
		}
	}
	return byParent
}

// handlesAfter waits (at most noticeWait) until every session of want runs again in its new
// folder and returns their handles then ("" for one that did not show up).
func (w *moveWorld) handlesAfter(pe peerEnv, want map[string]string) map[string]string {
	out := map[string]string{}
	final := map[string]bool{}
	deadline := time.Now().Add(noticeWait)
	for {
		if es, _, rf := peersListEntries(pe, true); rf == nil {
			for _, x := range es {
				if _, ok := want[x.Session]; ok && x.Handle != "" && !strings.EqualFold(x.Handle, w.handles[x.Session]) {
					out[x.Session] = x.Handle
					// The first name after a restart can be this machine's own until the hub
					// lists the session again under its machine name: wait for that one.
					final[x.Session] = sameMachine(x.Handle, w.handles[x.Session])
				}
			}
		}
		done := len(out) == len(want)
		for _, ok := range final {
			done = done && ok
		}
		if done || time.Now().After(deadline) {
			return out
		}
		time.Sleep(time.Second)
	}
}

// sameMachine: the two handles name the same machine (the part after the last dot), or the old
// one is not known.
func sameMachine(a, b string) bool {
	if b == "" {
		return true
	}
	return strings.EqualFold(a[strings.LastIndex(a, ".")+1:], b[strings.LastIndex(b, ".")+1:])
}

// noticeText is the notice for one parent: one line per lane that moved.
func noticeText(lanes []laneNotice) string {
	var b strings.Builder
	if len(lanes) > 1 {
		fmt.Fprintf(&b, "%d of the sessions you started moved to other accounts:\n", len(lanes))
	}
	for i, n := range lanes {
		if i > 0 {
			b.WriteString("\n")
		}
		name := n.Name
		if name == "" {
			name = short(n.Session)
		}
		fmt.Fprintf(&b, "Your lane %s (%s) moved %s -> %s at %s", name, short(n.Session), n.From, n.To, n.At.Local().Format("15:04"))
		if n.Move.Window != "" && !n.Move.HitAt.IsZero() && n.Move.HitAt.After(n.At) {
			fmt.Fprintf(&b, " (%s would have hit at %s)", n.Move.Window, n.Move.HitAt.Local().Format("15:04"))
		}
		switch {
		case n.New != "" && n.Old != "":
			fmt.Fprintf(&b, "; its handle is now %s (the old handle %s still works).", n.New, n.Old)
		case n.New != "":
			fmt.Fprintf(&b, "; its handle is now %s.", n.New)
		default:
			b.WriteString("; it is restarting there (wy peers list shows its new handle).")
		}
	}
	// Parents asked whether a question sent before the move was lost (e2e): it was not.
	b.WriteString(" Its conversation goes on; messages sent to it before the move still reach it.")
	return b.String()
}
