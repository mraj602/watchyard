package mover

import (
	"strings"
	"testing"
	"time"
)

func TestDigest(t *testing.T) {
	st := State{Dir: t.TempDir()}
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	acc := func(key string, protected bool, window string, r5, l7, p7 float64, note string) PassAccount {
		return PassAccount{Key: key, Label: key, Protected: protected, Window: window, R5: r5, Level7: l7, P7: p7, Reset7S: 7200, Note: note}
	}
	pass := func(min int, as ...PassAccount) {
		if err := st.AppendPass(PassRecord{At: t0.Add(time.Duration(min) * time.Minute), Accounts: as}); err != nil {
			t.Fatal(err)
		}
	}
	// 10:00-10:02 the protected account "P" is over its cap; "S" triggers at 10:02 and is back
	// under its cap at 10:05 with its burn unchanged: a suspect.
	pass(0, acc("S", false, "", 5, 40, 45, ""), acc("P", true, "5h", 5, 60, 70, ""))
	pass(1, acc("S", false, "", 5, 40, 45, ""), acc("P", true, "5h", 5, 60, 70, ""))
	pass(2, acc("S", false, "5h", 10, 40, 45, ""), acc("P", true, "", 5, 60, 70, ""))
	pass(5, acc("S", false, "", 9, 41, 46, ""), acc("P", true, "", 5, 61, 71, ""))
	// "U" moves on the readings: a suspect by its reason.
	pass(30, acc("U", false, "5h", 10, 10, 10, "the readings rose ...: used the readings"))
	for _, e := range []LogEntry{
		{At: t0.Add(2 * time.Minute), SessionID: "sess-1", Name: "one", From: "S", FromDir: "dirS", ToDir: "dirT", Auto: true, Reason: "5h projects over"},
		{At: t0.Add(30 * time.Minute), SessionID: "sess-2", From: "U", FromDir: "dirU", ToDir: "dirT", Auto: true, Reason: "used the readings"},
		{At: t0.Add(40 * time.Minute), SessionID: "sess-3", From: "Q", FromDir: "dirQ", ToDir: "dirT", Reason: "by hand"},
		{At: t0.Add(41 * time.Minute), SessionID: "bad", From: "Q", Error: "failed"},
	} {
		if err := st.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	now := t0.Add(time.Hour)
	hits := []LimitHit{{At: t0.Add(10 * time.Minute), Session: "sess-9"}, {At: t0.Add(-48 * time.Hour), Session: "old"}}
	d := st.BuildDigest(t0.Add(-time.Hour), now, tname, hits)
	if len(d.Moves) != 3 || len(d.LimitHits) != 1 {
		t.Fatalf("moves %d hits %d", len(d.Moves), len(d.LimitHits))
	}
	if len(d.ProtectedOver) != 1 || d.ProtectedOver[0].Account != "P" || d.ProtectedOver[0].Minutes != 2 {
		t.Fatalf("protected over: %+v", d.ProtectedOver)
	}
	if len(d.Suspects) != 2 || d.Suspects[0].Session != "sess-1" || !strings.Contains(d.Suspects[0].Why, "back under its cap 3 min") ||
		d.Suspects[1].Session != "sess-2" || !strings.Contains(d.Suspects[1].Why, "readings") {
		t.Fatalf("suspects: %+v", d.Suspects)
	}
	var s *WeeklyRoom
	for i := range d.Weekly {
		if d.Weekly[i].Account == "S" {
			s = &d.Weekly[i]
		}
	}
	if s == nil || s.Room != 54 || !s.ResetAt.Equal(t0.Add(5*time.Minute+2*time.Hour)) {
		t.Fatalf("weekly: %+v", d.Weekly)
	}
	txt := d.Text()
	for _, want := range []string{"Moves: 3", "Limit hits (sessions a limit stopped): 1", "one  dirS -> dirT", "(by hand)", "P: 2 min", "may not have been needed"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text lacks %q:\n%s", want, txt)
		}
	}
	if sum := d.Summary(); !strings.Contains(sum, "3 moves, 1 limit hits") || !strings.Contains(sum, "2 suspect") {
		t.Errorf("summary: %s", sum)
	}
	// No pass records: the parts that need them are left out with a note.
	d = State{Dir: t.TempDir()}.BuildDigest(t0, now, tname, nil)
	if len(d.Notes) != 1 || len(d.Weekly) != 0 {
		t.Fatalf("empty digest: %+v", d)
	}
}

func TestDigestDue(t *testing.T) {
	day := time.Date(2026, 11, 24, 0, 0, 0, 0, time.Local)
	for _, c := range []struct {
		name string
		at   time.Time
		sent bool
		want bool
	}{
		{"before 09:00", day.Add(8*time.Hour + 59*time.Minute), false, false},
		{"09:00", day.Add(9 * time.Hour), false, true},
		{"09:00 already sent", day.Add(9*time.Hour + time.Minute), true, false},
		{"missed, still morning", day.Add(11 * time.Hour), false, true},
		{"too late", day.Add(13 * time.Hour), false, false},
		{"next day", day.Add(33 * time.Hour), true, true},
	} {
		st := State{Dir: t.TempDir()}
		if c.sent {
			if err := st.MarkDigest(day.Add(9 * time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		if got := st.DigestDue(c.at); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
