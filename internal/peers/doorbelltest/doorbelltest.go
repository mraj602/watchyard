// Package doorbelltest writes a Doorbell install as pip, pipx and uv leave it (peers.md 4.1a
// step 3), for tests: the agent-doorbell distribution's METADATA, entry_points.txt and RECORD
// (with matching hashes), the doorbell package's modules and the console-script launcher.
package doorbelltest

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Dist is the dist-info folder WriteDist makes, relative to the venv.
const Dist = "lib/python3.12/site-packages/agent_doorbell-0.4.1.dist-info"

// Launcher is pip's console-script launcher for doorbell.cli:main after shebang (the whole
// first line, without its newline).
func Launcher(shebang string) string {
	return shebang + "\n# -*- coding: utf-8 -*-\nimport re\nimport sys\nfrom doorbell.cli import main\nif __name__ == '__main__':\n" +
		"    sys.argv[0] = re.sub(r'(-script\\.pyw|\\.exe)?$', '', sys.argv[0])\n    sys.exit(main())\n"
}

// WriteDist writes the distribution and bin/doorbell (the launcher after shebang; "" means
// "#!<venv>/bin/python") into venv, and pyvenv.cfg when it is missing. It does not make the
// interpreter.
func WriteDist(t testing.TB, venv, shebang string) {
	t.Helper()
	if shebang == "" {
		shebang = "#!" + filepath.Join(venv, "bin", "python")
	}
	site := filepath.Join(venv, "lib", "python3.12", "site-packages")
	files := []struct{ rel, body string }{
		{"doorbell/__init__.py", "__version__ = \"0.4.1\"\n"},
		{"doorbell/__main__.py", "from doorbell.cli import main\nmain()\n"},
		{"doorbell/cli.py", "def main():\n    return 0\n"},
		{"agent_doorbell-0.4.1.dist-info/METADATA", "Metadata-Version: 2.1\nName: agent-doorbell\nVersion: 0.4.1\n"},
		{"agent_doorbell-0.4.1.dist-info/entry_points.txt", "[console_scripts]\ndoorbell = doorbell.cli:main\n"},
		{"../../../bin/doorbell", Launcher(shebang)},
	}
	var record strings.Builder
	for _, f := range files {
		p := filepath.Join(site, filepath.FromSlash(f.rel))
		write(t, p, f.body)
		sum := sha256.Sum256([]byte(f.body))
		fmt.Fprintf(&record, "%s,sha256=%s,%d\n", f.rel, base64.RawURLEncoding.EncodeToString(sum[:]), len(f.body))
	}
	if err := os.Chmod(filepath.Join(venv, "bin", "doorbell"), 0o755); err != nil {
		t.Fatal(err)
	}
	record.WriteString("agent_doorbell-0.4.1.dist-info/RECORD,,\n")
	write(t, filepath.Join(venv, Dist, "RECORD"), record.String())
	if _, err := os.Stat(filepath.Join(venv, "pyvenv.cfg")); err != nil {
		write(t, filepath.Join(venv, "pyvenv.cfg"), "home = /usr/bin\n")
	}
}

func write(t testing.TB, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
