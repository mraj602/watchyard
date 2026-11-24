package mover

import (
	"path/filepath"
	"strings"

	"github.com/mraj602/watchyard/internal/jsonedit"
)

// CarryTrust gives the session's folder the trust it had under its old account on the new
// one (setup.md 8.6, Trust): Claude Code asks "Do you trust this folder?" per account, and a
// resumed interactive session would wait at that question (whose default answer exits). When
// fromDir's .claude.json trusts cwd or a folder above it and toDir's trusts none of them, the
// project entry of cwd in toDir's .claude.json gets "hasTrustDialogAccepted": true, written
// the way wy edits .claude.json (setup.md 5.4: Claude Code's lock, only that value changes).
// Nothing else is copied; a folder not trusted before stays untrusted.
func CarryTrust(fromDir, toDir, cwd string) (bool, error) {
	if fromDir == toDir || cwd == "" || !filepath.IsAbs(cwd) {
		return false, nil
	}
	if !trusted(filepath.Join(fromDir, ".claude.json"), cwd) || trusted(filepath.Join(toDir, ".claude.json"), cwd) {
		return false, nil
	}
	res, err := jsonedit.Apply(filepath.Join(toDir, ".claude.json"), jsonedit.Options{Claude: true, Folder: toDir, IgnoreLive: true},
		jsonedit.Op{Pointer: trustPointer(cwd), Value: []byte("true")})
	return err == nil && res.Written, err
}

// trusted reports whether the .claude.json at path trusts dir or a folder above it.
func trusted(path, dir string) bool {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if v, ok, err := jsonedit.Read(path, trustPointer(d)); err == nil && ok && strings.TrimSpace(string(v)) == "true" {
			return true
		}
		if d == filepath.Dir(d) {
			return false
		}
	}
}

func trustPointer(dir string) string {
	esc := strings.NewReplacer("~", "~0", "/", "~1").Replace(dir)
	return "/projects/" + esc + "/hasTrustDialogAccepted"
}
