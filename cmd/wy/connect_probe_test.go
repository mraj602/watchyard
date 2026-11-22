package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// connect must send real slot_info records, so a token bound to another machine is caught
// (an empty batch cannot show that), and store nothing when it is.
func TestConnectProbeCatchesTokenForAnotherMachine(t *testing.T) {
	deps, runner, home := sandbox(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var kinds, machines []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"ok":true,"version":"0.2.0","schema":1}`)
		case "/v1/ingest":
			var b struct {
				Records []struct {
					Kind    string `json:"kind"`
					Machine string `json:"machine"`
					Slot    string `json:"slot"`
				} `json:"records"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			for _, rec := range b.Records {
				kinds = append(kinds, rec.Kind+"@"+rec.Slot)
				machines = append(machines, rec.Machine)
			}
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":"this token is bound to another machine","code":"machine_mismatch"}`)
		}
	}))
	defer srv.Close()

	var out, errb bytes.Buffer
	err := connect(context.Background(), deps, []string{srv.URL, "--token", goodToken, "--insecure-lan"}, &out, &errb)
	if len(machines) == 0 {
		t.Fatalf("no record reached the hub: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "another machine") || !strings.Contains(err.Error(), machines[0]) {
		t.Fatalf("err = %v (machines %v)", err, machines)
	}
	if len(kinds) != 1 || kinds[0] != "slot_info@~/.claude" {
		t.Fatalf("probe sent %v, want one slot_info for ~/.claude", kinds)
	}
	if _, err := os.Stat(filepath.Join(deps.dirs.State.Path, "token")); err == nil {
		t.Error("a mismatched token must not be stored")
	}
	if len(runner.calls) != 0 {
		t.Errorf("timer touched: %v", runner.calls)
	}
}

// The labels and colours wy setup stored locally reach the hub in the probe's slot_info
// (setup.md 8.4), so the board shows the account under the label chosen at setup.
func TestConnectProbeCarriesLabels(t *testing.T) {
	deps, _, home := sandbox(t)
	for _, d := range []string{".claude", ".claude-2"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, d, "settings.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(deps.dirs.Config.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "version = 1\n\n[slots]\n[slots.colours]\n'~/.claude' = '#14b8a6'\n\n[slots.labels]\n'~/.claude' = 'work'\n'~/.claude-2' = 'Bad Label'\n" +
		"\n[slots.modes]\n'~/.claude' = 'protected'\n'~/.claude-2' = 'normal'\n"
	if err := os.WriteFile(filepath.Join(deps.dirs.Config.Path, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, err := probeRecords(deps)
	if err != nil || len(recs) != 2 {
		t.Fatalf("%d records, %v", len(recs), err)
	}
	got := map[string]map[string]any{}
	for _, b := range recs {
		var r struct {
			Slot    string         `json:"slot"`
			Payload map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		got[r.Slot] = r.Payload
	}
	if p := got["~/.claude"]; p["label"] != "work" || p["colour"] != "#14b8a6" || p["mode"] != "protected" {
		t.Errorf("~/.claude payload %v, want label work and colour #14b8a6", p)
	}
	if p := got["~/.claude-2"]; p["label"] != nil || p["colour"] != nil || p["mode"] != nil {
		t.Errorf("~/.claude-2: an invalid hand-edited label must be left out: %v", p)
	}
}
