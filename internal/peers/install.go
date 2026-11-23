package peers

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The install checks of peers.md 4.1a step 3: the venv holds agent-doorbell as an installer
// (pip, pipx, uv) wrote it. They catch a wrong path, a damaged or half-upgraded install and a
// package file edited after it was installed; a process of the same user can still build a
// fake venv that passes them (the honest limit), which is why the owner reads the paths.

// doorbellEntryPoint is the console script's line in entry_points.txt.
const doorbellEntryPoint = "doorbell = doorbell.cli:main"

// doorbellRecorded are the files RECORD must list with a matching hash, relative to
// site-packages ("" + script: the console script, found by path).
var doorbellRecorded = []string{"doorbell/__init__.py", "doorbell/__main__.py", "doorbell/cli.py"}

// launcherLines are the lines a console-script launcher for doorbell.cli:main may hold after
// its interpreter line (blank lines aside): pip's and pipx's (distlib, old and new) and uv's.
var launcherLines = map[string]bool{
	"# -*- coding: utf-8 -*-":       true,
	"import re":                     true,
	"import sys":                    true,
	"from doorbell.cli import main": true,
	`if __name__ == '__main__':`:    true,
	`if __name__ == "__main__":`:    true,
	`sys.argv[0] = re.sub(r'(-script\.pyw|\.exe)?$', '', sys.argv[0])`: true,
	`sys.argv[0] = sys.argv[0].removesuffix('.exe')`:                   true,
	`sys.argv[0] = sys.argv[0].removesuffix(".exe")`:                   true,
	`if sys.argv[0].endswith("-script.pyw"):`:                          true,
	`sys.argv[0] = sys.argv[0][:-11]`:                                  true,
	`elif sys.argv[0].endswith(".exe"):`:                               true,
	`sys.argv[0] = sys.argv[0][:-4]`:                                   true,
	"sys.exit(main())":                                                 true,
}

// CheckDoorbellInstall checks that venv holds the agent-doorbell distribution as installed and
// that program (venv's bin/doorbell) is its console-script launcher: METADATA names it,
// entry_points.txt declares `doorbell = doorbell.cli:main`, RECORD lists the script and the
// package's __init__, __main__ and cli modules with hashes that match the files, and the
// script holds nothing but the standard launcher.
func CheckDoorbellInstall(venv, program string) error {
	dists, _ := filepath.Glob(filepath.Join(venv, "lib", "python*", "site-packages", "agent_doorbell-*.dist-info"))
	var last error = fmt.Errorf("%s does not hold the %s package", venv, DoorbellDist)
	for _, dist := range dists {
		if !distNamed(filepath.Join(dist, "METADATA")) {
			continue
		}
		if err := checkDist(dist, program); err != nil {
			last = err
			continue
		}
		return checkLauncher(program)
	}
	return last
}

// distNamed reports whether a METADATA file names agent-doorbell.
func distNamed(meta string) bool {
	f, err := os.Open(meta)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for i := 0; i < 64 && sc.Scan(); i++ {
		if name, ok := strings.CutPrefix(sc.Text(), "Name:"); ok {
			return normDist(name) == normDist(DoorbellDist)
		}
	}
	return false
}

func checkDist(dist, program string) error {
	site := filepath.Dir(dist)
	ep, err := os.ReadFile(filepath.Join(dist, "entry_points.txt"))
	if err != nil || !hasConsoleScript(ep) {
		return fmt.Errorf("%s does not declare the console script %q", filepath.Base(dist), doorbellEntryPoint)
	}
	rf, err := os.Open(filepath.Join(dist, "RECORD"))
	if err != nil {
		return fmt.Errorf("%s has no RECORD: not installed by pip, pipx or uv", filepath.Base(dist))
	}
	defer rf.Close()
	r := csv.NewReader(io.LimitReader(rf, 4<<20))
	r.FieldsPerRecord = -1
	want := map[string]bool{filepath.Clean(program): false}
	for _, rel := range doorbellRecorded {
		want[filepath.Join(site, rel)] = false
	}
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: RECORD does not parse", filepath.Base(dist))
		}
		if len(row) < 2 || row[0] == "" {
			continue
		}
		path := filepath.Join(site, filepath.FromSlash(row[0]))
		if _, ok := want[path]; !ok {
			continue
		}
		size := ""
		if len(row) > 2 {
			size = row[2]
		}
		if err := recordMatches(path, row[1], size); err != nil {
			return fmt.Errorf("%s does not match its RECORD (%v): edited after it was installed", path, err)
		}
		want[path] = true
	}
	for path, seen := range want {
		if !seen {
			return fmt.Errorf("%s's RECORD does not list %s", filepath.Base(dist), path)
		}
	}
	return nil
}

// hasConsoleScript reports whether entry_points.txt declares doorbell = doorbell.cli:main
// under [console_scripts].
func hasConsoleScript(b []byte) bool {
	section := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line
			continue
		}
		if section != "[console_scripts]" {
			continue
		}
		name, target, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(name) == DoorbellScript && strings.TrimSpace(target) == "doorbell.cli:main" {
			return true
		}
	}
	return false
}

// recordMatches checks a file against its RECORD hash (sha256=<urlsafe base64, no padding>)
// and size.
func recordMatches(path, hash, size string) error {
	algo, sum, ok := strings.Cut(hash, "=")
	if !ok || algo != "sha256" {
		return errors.New("no sha256 hash")
	}
	want, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(sum, "="))
	if err != nil {
		return errors.New("a malformed hash")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return errors.New("unreadable")
	}
	if size != "" {
		if n, err := strconv.ParseInt(size, 10, 64); err != nil || n != int64(len(b)) {
			return errors.New("another size")
		}
	}
	got := sha256.Sum256(b)
	if !bytes.Equal(got[:], want) {
		return errors.New("another hash")
	}
	return nil
}

// checkLauncher checks that program is the standard console-script launcher of
// doorbell.cli:main: an interpreter line (or the #!/bin/sh trampoline's three lines), then only
// launcherLines.
func checkLauncher(program string) error {
	b, err := os.ReadFile(program)
	if err != nil {
		return err
	}
	if len(b) > 4096 {
		return fmt.Errorf("%s is not a console-script launcher (too long)", program)
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "#!") {
		return fmt.Errorf("%s does not start with an interpreter line", program)
	}
	rest := lines[1:]
	if f := strings.Fields(strings.TrimPrefix(lines[0], "#!")); len(f) > 0 && (filepath.Base(f[0]) == "sh" || filepath.Base(f[0]) == "bash") {
		// pip's trampoline for a long interpreter path:
		//   '''exec' "/venv/bin/python" "$0" "$@"
		//   ' '''
		if len(rest) < 2 || !strings.HasPrefix(rest[0], "'''exec' ") || !strings.HasSuffix(rest[0], ` "$0" "$@"`) ||
			strings.TrimSpace(rest[1]) != "' '''" {
			return fmt.Errorf("%s is a shell script, not a console-script launcher", program)
		}
		rest = rest[2:]
	}
	var imports, exits bool
	for _, l := range rest {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if !launcherLines[t] {
			return fmt.Errorf("%s is not the standard launcher of doorbell.cli:main (it holds %q)", program, clip(t, 60))
		}
		imports = imports || t == "from doorbell.cli import main"
		exits = exits || t == "sys.exit(main())"
	}
	if !imports || !exits {
		return fmt.Errorf("%s is not the standard launcher of doorbell.cli:main", program)
	}
	return nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
