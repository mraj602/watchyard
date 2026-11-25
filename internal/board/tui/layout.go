package tui

import (
	"fmt"
	"strings"

	"github.com/mraj602/watchyard/internal/board"
)

const (
	defaultCols = 100
	compactMax  = 60 // columns at or below which the one-line layout is used
)

type glyphSet struct{ working, needs, done, failed, ended, cursor, dot, sep string }

var (
	glyphsUnicode = glyphSet{"●", "▲", "○", "■", "·", "›", "●", " · "}
	glyphsASCII   = glyphSet{"*", "!", "o", "x", ".", ">", "*", " - "}
)

func (g glyphSet) of(name string) string {
	switch name {
	case board.GlyphWorking:
		return g.working
	case board.GlyphNeeds:
		return g.needs
	case board.GlyphDone:
		return g.done
	case board.GlyphFailed:
		return g.failed
	}
	return g.ended
}

func glyphStyle(name string) sty {
	switch name {
	case board.GlyphWorking:
		return sty{fg: "green"}
	case board.GlyphNeeds:
		return sty{fg: "yellow", bold: true}
	case board.GlyphFailed:
		return sty{fg: "red"}
	}
	return sty{dim: true}
}

func (m *model) glyphs() glyphSet {
	if m.o.ASCII {
		return glyphsASCII
	}
	return glyphsUnicode
}

// frame lays the board out in cols columns. h > 0 pads the body so the footer is on the last
// row and scrolls the body to keep the cursor in view; h == 0 returns every line. keys adds
// the footer (note line and key line). Every line is at most cols wide.
func (m *model) frame(cols, h int, keys bool) []string {
	if cols <= 0 {
		cols = defaultCols
	}
	mode := m.o.Mode
	compact := m.o.Compact || cols <= compactMax
	g := m.glyphs()

	head := m.headerLine().fit(cols, m.o.ASCII)
	var foot []line
	if keys {
		foot = []line{m.noteLine().fit(cols, m.o.ASCII), m.keysLine(compact).fit(cols, m.o.ASCII)}
	} else if n := m.hintLine(); n != nil {
		foot = []line{n.fit(cols, m.o.ASCII)}
	}

	var body []line
	cursorTop, cursorEnd := -1, -1
	switch {
	case m.help:
		for _, l := range helpLines(m.o.ASCII) {
			body = append(body, line{{text: l}}.fit(cols, m.o.ASCII))
		}
	case !m.have:
		body = append(body, line{{text: "Waiting for the first view…", sty: sty{dim: true}}})
	default:
		secs := m.sections()
		if len(secs) == 0 {
			msg := "No sessions yet."
			if m.filter != FilterAll || m.search != "" || len(m.v.Rows) > 0 {
				msg = "No sessions match. f changes the filter, / the search, e shows ended."
			}
			body = append(body, line{{text: msg, sty: sty{dim: true}}}.fit(cols, m.o.ASCII))
		}
		sw := statusWidth(m.v)
		for _, s := range secs {
			top := len(body)
			body = append(body, m.sectionHead(s, g).fit(cols, m.o.ASCII))
			for i, id := range s.ids {
				r := m.v.Rows[id]
				sel := id == m.cursor
				start := len(body)
				if i == 0 {
					start = top
				}
				body = append(body, m.rowLines(r, sel, s.needs, compact, cols, sw, g)...)
				if sel {
					cursorTop, cursorEnd = start, len(body)
				}
			}
		}
	}

	if h > 0 {
		bh := h - 1 - len(foot)
		if bh < 1 {
			bh = 1
		}
		m.bodyH = bh
		if cursorTop >= 0 {
			if cursorTop < m.scroll {
				m.scroll = cursorTop
			}
			if cursorEnd > m.scroll+bh {
				m.scroll = cursorEnd - bh
			}
		}
		if m.scroll > len(body)-bh {
			m.scroll = len(body) - bh
		}
		if m.scroll < 0 || cursorTop == 0 {
			m.scroll = 0
		}
		end := m.scroll + bh
		if end > len(body) {
			end = len(body)
		}
		body = body[m.scroll:end]
		for len(body) < bh {
			body = append(body, nil)
		}
	}

	out := make([]string, 0, 1+len(body)+len(foot))
	out = append(out, m.finish(head).render(mode))
	for _, l := range body {
		out = append(out, m.finish(l).render(mode))
	}
	for _, l := range foot {
		out = append(out, m.finish(l).render(mode))
	}
	return out
}

// finish folds a line to ASCII when --ascii is on (one column per rune, so widths hold).
func (m *model) finish(l line) line {
	if !m.o.ASCII {
		return l
	}
	out := make(line, len(l))
	for i, s := range l {
		out[i] = span{text: asciiFold(s.text), sty: s.sty}
	}
	return out
}

func asciiFold(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x7f:
			return r
		case r == '·' || r == '—':
			return '-'
		case r == '…':
			return '~'
		case r == '█':
			return '_'
		}
		return '?'
	}, s)
}

func (m *model) headerLine() line {
	v := m.v
	l := line{{text: "Watchyard", sty: sty{bold: true}}}
	if !m.have {
		return l
	}
	l = append(l, span{text: fmt.Sprintf("  %d sessions · %d machines · %s", v.Counts.Sessions, v.Counts.Machines, hubText(v))})
	if m.filter != FilterAll {
		l = append(l, span{text: " · " + m.filterLabel(), sty: sty{fg: "cyan"}})
	}
	return l
}

func hubText(v board.View) string {
	switch v.Hub.State {
	case board.HubLive:
		return "hub live"
	case board.HubOffline:
		if v.Hub.OfflineSince != nil {
			return "hub offline since " + v.Hub.OfflineSince.Local().Format("15:04")
		}
		return "hub offline"
	case board.HubUnpaired:
		return "local · not connected"
	}
	return "local"
}

// hintLine is the one-line note --once prints under the board when the hub is not in use.
func (m *model) hintLine() line {
	if !m.have {
		return nil
	}
	switch m.v.Hub.State {
	case board.HubUnpaired:
		return line{{text: "run `wy board connect` to see other machines", sty: sty{dim: true}}}
	case board.HubOffline:
		return line{{text: "other machines are greyed: their rows are from the last hub view", sty: sty{dim: true}}}
	}
	return nil
}

func (m *model) noteLine() line {
	switch {
	case m.typing:
		return line{{text: "/ " + m.search + "█", sty: sty{fg: "cyan"}}}
	case m.note != "":
		return line{{text: m.note, sty: sty{fg: "cyan"}}}
	case m.search != "":
		return line{{text: "search: " + m.search + "  (Esc clears)", sty: sty{fg: "cyan"}}}
	}
	if h := m.hintLine(); h != nil {
		return h
	}
	return nil
}

func (m *model) keysLine(compact bool) line {
	t := "↑↓ move  ⏎ focus  c copy  o open  f filter  / search  e ended  r refresh  ? help  q quit"
	if compact {
		t = "↑↓ ⏎ c o f / e r ? q"
	}
	if m.o.ASCII {
		t = strings.NewReplacer("↑↓", "up/dn", "⏎", "Enter").Replace(t)
	}
	return line{{text: t, sty: sty{dim: true}}}
}

func helpLines(ascii bool) []string {
	h := []string{
		"Keys",
		"  up/down, j/k   move        g/G, Home/End   first/last",
		"  Enter          focus this session's Warp tab (when it has one)",
		"  c              copy the resume command (OSC 52, also shown in the footer)",
		"  o              open the claude.ai link (https://claude.ai/ only)",
		"  f              filter: all, needs you, this machine, each machine",
		"  /              search project, title, account (Enter keeps it, Esc clears)",
		"  e              show or hide ended sessions",
		"  r              refetch now",
		"  ?              this help (any key closes)",
		"  q, Ctrl-C      quit",
		"",
		"The board only reads. It never types into a session or approves anything.",
	}
	if ascii {
		for i := range h {
			h[i] = strings.NewReplacer("…", "...", "—", "-").Replace(h[i])
		}
	}
	return h
}

// sectionHead is the NEEDS YOU line or an account's label with its limit windows.
func (m *model) sectionHead(s section, g glyphSet) line {
	if s.needs {
		return line{{text: fmt.Sprintf("NEEDS YOU (%d)", len(s.ids)), sty: sty{fg: "yellow", bold: true}}}
	}
	gr := s.group
	l := line{
		{text: g.dot + " ", sty: sty{fg: gr.Colour}},
		{text: gr.Label, sty: sty{fg: gr.Colour, bold: true}},
	}
	if gr.Limited {
		l = append(l, span{text: "  limited", sty: sty{fg: "red"}})
	}
	if gr.Note != "" {
		l = append(l, span{text: "  " + gr.Note, sty: sty{fg: "yellow"}})
	}
	if gr.FiveHour != nil {
		l = append(l, span{text: "  5h " + gr.FiveHour.Text, sty: sty{dim: true}})
	}
	if gr.SevenDay != nil {
		l = append(l, span{text: "  7d " + gr.SevenDay.Text, sty: sty{dim: true}})
	}
	return l
}

func statusWidth(v board.View) int {
	w := 4
	for _, r := range v.Rows {
		if n := strWidth(r.Status); n > w {
			w = n
		}
	}
	if w > 16 {
		w = 16
	}
	return w
}

func statusStyle(r board.Row) sty {
	switch r.Glyph {
	case board.GlyphNeeds:
		return sty{fg: "yellow", bold: true}
	case board.GlyphFailed:
		return sty{fg: "red"}
	case board.GlyphWorking:
		return sty{fg: "green"}
	}
	return sty{dim: true}
}

func contextText(r board.Row) (string, sty) {
	if r.ContextPct == nil {
		return "", sty{}
	}
	t := fmt.Sprintf("%d%%", *r.ContextPct)
	switch r.ContextLevel {
	case board.ContextHigh:
		return t, sty{fg: "red", bold: true}
	case board.ContextWarn:
		return t, sty{fg: "yellow"}
	}
	return t, sty{dim: true}
}

// rowLines is one session: one line in the compact layout, one or two in the full one.
func (m *model) rowLines(r board.Row, sel, inNeeds, compact bool, cols, sw int, g glyphSet) []line {
	dim := r.Greyed || r.Stale
	base := sty{dim: dim}
	mark := " "
	if sel {
		mark = g.cursor
		base.bold = true
	}
	left := line{
		{text: mark, sty: sty{bold: sel}},
		{text: g.of(r.Glyph), sty: glyphSt(r, dim)},
		{text: " "},
	}
	name := r.Project
	if r.Title != "" {
		name += g.sep + r.Title
	}
	left = append(left, span{text: name, sty: base})
	if !compact && inNeeds && r.Account != "" {
		left = append(left, span{text: "  " + r.Account, sty: sty{fg: r.Colour, dim: dim}})
	}
	if !r.Here && r.MachineName != "" {
		left = append(left, span{text: "  [" + r.MachineName + "]", sty: sty{fg: "cyan", dim: true}})
	}

	ctxT, ctxS := contextText(r)
	var right line
	if compact {
		right = line{{text: padRight(cutWidth(r.Status, sw, m.o.ASCII), sw), sty: dimmed(statusStyle(r), dim)}}
		if ctxT != "" && r.ContextLevel != board.ContextOK {
			right = append(right, span{text: " "}, span{text: padLeft(ctxT, 4), sty: ctxS})
		}
	} else {
		right = line{
			{text: padRight(cutWidth(r.Status, sw, m.o.ASCII), sw), sty: dimmed(statusStyle(r), dim)},
			{text: " " + padLeft(ctxT, 4), sty: ctxS},
			{text: " " + padLeft(r.Age, 4), sty: sty{dim: true}},
		}
	}
	rw := right.width()
	lw := cols - rw - 1
	if lw < 8 {
		lw = 8
	}
	left = left.fit(lw, m.o.ASCII)
	pad := cols - left.width() - rw
	if pad < 1 {
		pad = 1
	}
	l1 := append(append(left, span{text: strings.Repeat(" ", pad)}), right...)
	l1 = l1.fit(cols, m.o.ASCII)
	out := []line{l1}

	if !compact && r.Line2 != "" && r.Line2Kind != board.Line2None {
		st := sty{dim: true}
		switch r.Line2Kind {
		case board.Line2Question:
			st = sty{dim: dim}
		case board.Line2Error:
			st = sty{fg: "red", dim: true}
		}
		out = append(out, line{{text: "    " + r.Line2, sty: st}}.fit(cols, m.o.ASCII))
	}
	return out
}

func glyphSt(r board.Row, dim bool) sty { return dimmed(glyphStyle(r.Glyph), dim) }

func dimmed(s sty, dim bool) sty {
	if dim {
		s.dim = true
		s.bold = false
	}
	return s
}
