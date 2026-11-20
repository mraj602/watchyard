package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNextKeys(t *testing.T) {
	dir := t.TempDir()
	f, _ := Load(dir)
	for _, kv := range [][]string{
		{"next.strategy", "fill"},
		{"next.session_5h", "12.5"},
		{"next.accounts.me@example.test.mode", "aggressive"},
		{"next.accounts.me@example.test.cap_7d", "80"},
		{"next.accounts.claude-7.mode", "conservative"},
		{"next.accounts.work.hours", "09:00-18:00"},
		{"next.accounts.work.days", "mon-fri"},
		{"next.accounts.work.exclude", "true"},
		{"next.accounts.~/.claude-3.priority", "1"},
	} {
		if err := f.Set(kv[0], kv[1:]); err != nil {
			t.Fatalf("set %v: %v", kv, err)
		}
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.Typed()
	if err != nil {
		t.Fatal(err)
	}
	n := c.Next
	if n.Strategy != "fill" || n.Session5h != 12.5 || n.Session7d != 2 || n.StaleMin != 30 {
		t.Errorf("globals %+v", n)
	}
	me := n.Accounts["me@example.test"]
	if me.Mode != "aggressive" || me.Cap7d != 80 {
		t.Errorf("me %+v", me)
	}
	if w := n.Accounts["work"]; !w.Exclude || w.Hours != "09:00-18:00" || w.Days != "mon-fri" {
		t.Errorf("work %+v", w)
	}
	if n.Accounts["~/.claude-3"].Priority != 1 {
		t.Errorf("accounts %+v", n.Accounts)
	}
	if v, err := f.Get("next.accounts.me@example.test.mode"); err != nil || v != "aggressive" {
		t.Errorf("get: %v %v", v, err)
	}
	if u := f.Unknown(); len(u) != 0 {
		t.Errorf("unknown %v", u)
	}
}

func TestNextKeysRefuseBadValues(t *testing.T) {
	f, _ := Load(t.TempDir())
	for _, kv := range [][]string{
		{"next.strategy", "random"},
		{"next.session_5h", "-1"},
		{"next.session_5h", "lots"},
		{"next.accounts.me.mode", "rogue"},
		{"next.accounts.me.cap_5h", "0"},
		{"next.accounts.me.cap_5h", "101"},
		{"next.accounts.me.reserve_pct", "100"},
		{"next.accounts.me.hours", "9am-5pm"},
		{"next.accounts.me.hours", "09:00-09:00"},
		{"next.accounts.me.days", "weekdays"},
		{"next.accounts.me.colour", "red"},
		{"next.accounts.mode", "normal"},
	} {
		if err := f.Set(kv[0], kv[1:]); err == nil {
			t.Errorf("set %v: want an error", kv)
		}
	}
}

func TestNextHandEditedFile(t *testing.T) {
	dir := t.TempDir()
	body := "[next]\nstrategy = \"smart\"\nsession_7d = 3\n[next.accounts.\"a@b.test\"]\nmode = \"aggressive\"\nweight = 2\nbogus = 1\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.Typed()
	if err != nil {
		t.Fatal(err)
	}
	if c.Next.Session7d != 3 || c.Next.Accounts["a@b.test"].Weight != 2 {
		t.Errorf("next %+v", c.Next)
	}
	if u := f.Unknown(); len(u) != 1 || u[0] != "next.accounts.a@b.test.bogus" {
		t.Errorf("unknown %v", u)
	}
}

func TestParseNextHoursAndDays(t *testing.T) {
	s, e, err := ParseNextHours("9-17:30")
	if err != nil || s != 9*time.Hour || e != 17*time.Hour+30*time.Minute {
		t.Errorf("hours %v %v %v", s, e, err)
	}
	d, err := ParseNextDays("fri-mon,wed")
	if err != nil || len(d) != 5 || !d[time.Sunday] || d[time.Tuesday] {
		t.Errorf("days %v %v", d, err)
	}
}
