package ccjobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	sidA = "8de967aa-1111-4000-8000-000000000001"
	sidB = "40f6a21a-2222-4000-8000-000000000002"
	sidC = "c65bc95d-3333-4000-8000-000000000003"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A config folder laid out as Claude Code 2.1.330's daemon leaves it.
func folder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "jobs", "8de967aa", "state.json"), `{"state":"done","sessionId":"`+sidA+`","inFlight":{"tasks":0}}`)
	write(t, filepath.Join(dir, "jobs", "40f6a21a", "state.json"), `{"state":"idle","sessionId":"`+sidB+`"}`)
	// A job folder whose short id is not its session's prefix (the scan finds it).
	write(t, filepath.Join(dir, "jobs", "0ece0453", "state.json"), `{"state":"done","sessionId":"`+sidC+`"}`)
	write(t, filepath.Join(dir, "jobs", "deadbeef", "state.json"), `not json`)
	write(t, filepath.Join(dir, "jobs", "pins.json"), "[\n  \"40f6a21a\"\n]")
	write(t, filepath.Join(dir, "daemon.log"), strings.Join([]string{
		"[2026-10-31T23:41:58.060Z] [bg] bg retire 8de967aa: settled, idle 61m",
		"[2026-10-31T23:41:58.070Z] [bg] bg settled 8de967aa (done)",
		"[2026-11-12T14:14:55.585Z] [bg] bg retire 8de967aa: idle-prompt, idle 8h",
		"[2026-11-11T17:22:54.746Z] [bg] bg retire 0ece0453: stale-spare, idle 60m",
		"[2026-10-15T05:18:25.443Z] [bg] bg settled 05c895bf (killed)",
		"[bad ts] [bg] bg retire 11111111: settled, idle 8h",
		"garbage",
	}, "\n")+"\n")
	return dir
}

func TestLoad(t *testing.T) {
	dir := folder(t)
	a := Load(dir)
	if a.Job(sidA) != "8de967aa" || a.Job(sidB) != "40f6a21a" || a.Job(sidC) != "0ece0453" || a.Job("other") != "" || a.Job("") != "" {
		t.Fatalf("jobs %v", a.Jobs)
	}
	if !a.Pins["40f6a21a"] || a.Pins["8de967aa"] || len(a.Pins) != 1 {
		t.Fatalf("pins %v", a.Pins)
	}
	r := a.Retired["8de967aa"]
	if want := time.Date(2026, 11, 12, 14, 14, 55, 585e6, time.UTC); !r.At.Equal(want) || r.Reason != "idle-prompt" {
		t.Fatalf("newest retirement %+v", r)
	}
	if a.Retired["0ece0453"].Reason != "stale-spare" || len(a.Retired) != 2 {
		t.Fatalf("retired %v", a.Retired)
	}
	if JobOf(dir, sidA) != "8de967aa" || JobOf(dir, sidC) != "0ece0453" || JobOf(dir, "ffffffff-x") != "" || JobOf("", sidA) != "" {
		t.Fatal("JobOf")
	}
}

func TestMissingOrMalformed(t *testing.T) {
	for _, a := range []Account{Load(""), Load(filepath.Join(t.TempDir(), "nope"))} {
		if len(a.Jobs)+len(a.Pins)+len(a.Retired) != 0 {
			t.Fatalf("empty folder: %+v", a)
		}
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "jobs", "pins.json"), `{"not":"an array"}`)
	if len(ReadPins(dir)) != 0 {
		t.Fatal("malformed pins")
	}
	if Short("abc") != "" || Short(sidA) != "8de967aa" {
		t.Fatal("short")
	}
}

// Nothing is written: the folder is byte-for-byte what it was.
func TestReadOnly(t *testing.T) {
	dir := folder(t)
	before := snapshot(t, dir)
	_ = Load(dir)
	_ = JobOf(dir, sidC)
	if after := snapshot(t, dir); after != before {
		t.Fatalf("folder changed:\n%s\n%s", before, after)
	}
}

func snapshot(t *testing.T, dir string) string {
	var b strings.Builder
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		b.WriteString(p + " " + fi.Mode().String() + " " + fi.ModTime().String() + "\n")
		if !fi.IsDir() {
			c, _ := os.ReadFile(p)
			b.Write(c)
		}
		return nil
	})
	return b.String()
}
