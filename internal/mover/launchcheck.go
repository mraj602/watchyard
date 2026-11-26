package mover

import (
	"os"
	"path/filepath"
	"strings"
)

// LaunchProgram is the program account.move_launch runs: the first word of the template
// after expansion (a leading `env NAME=value...` is looked through). "" for no template.
func LaunchProgram(template string) string {
	if strings.TrimSpace(template) == "" {
		return ""
	}
	argv := Command(template, Target{Key: "x", Label: "x", Dir: "/x/.claude-2"}, nil)
	i := 0
	if len(argv) > 0 && argv[0] == "env" {
		i = 1
		for i < len(argv) && (strings.HasPrefix(argv[i], "-") || strings.Contains(argv[i], "=")) {
			i++
		}
	}
	if i < len(argv) {
		return argv[i]
	}
	return ""
}

// LookIn finds the executable file name in the PATH-style list path (an absolute or
// ~/-relative name is checked as it is). ok is false when none is found.
func LookIn(path, name string) (string, bool) {
	isExec := func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
	}
	if strings.ContainsRune(name, '/') {
		if filepath.IsAbs(name) && isExec(name) {
			return name, true
		}
		return "", false
	}
	for _, d := range filepath.SplitList(path) {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		if p := filepath.Join(d, name); isExec(p) {
			return p, true
		}
	}
	return "", false
}
