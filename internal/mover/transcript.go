package mover

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// CarryTranscript makes the session's transcript readable from toDir before it is resumed
// there. Claude Code looks a session id up only under its own config folder's projects/
// (setup.md 8.6, Transcripts): folders made by `wy account add` share one projects/ folder
// and need nothing, but a folder with its own gets a copy of the transcript and of the
// session's folder beside it (its subagents and tool results). The session has stopped, so
// the copy is complete; a stale copy left by an earlier move is replaced. No transcript under
// fromDir is not an error (nothing to carry).
func CarryTranscript(fromDir, toDir, sessionID string) error {
	if fromDir == toDir || !sidRe.MatchString(sessionID) {
		return nil
	}
	src := newest(filepath.Join(fromDir, "projects", "*", sessionID+".jsonl"))
	if src == "" {
		return nil
	}
	proj := filepath.Base(filepath.Dir(src))
	dst := filepath.Join(toDir, "projects", proj, sessionID+".jsonl")
	if a, err := os.Stat(src); err == nil {
		if b, err := os.Stat(dst); err == nil && os.SameFile(a, b) {
			return nil // one shared projects folder
		}
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	side := filepath.Join(filepath.Dir(src), sessionID)
	if fi, err := os.Stat(side); err == nil && fi.IsDir() {
		to := filepath.Join(filepath.Dir(dst), sessionID)
		return filepath.WalkDir(side, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(side, p)
			if d.IsDir() {
				return os.MkdirAll(filepath.Join(to, rel), 0o700)
			}
			if !d.Type().IsRegular() {
				return nil
			}
			return copyFile(p, filepath.Join(to, rel))
		})
	}
	return nil
}

// newest is the most recently written file matching pattern ("" when none).
func newest(pattern string) string {
	ms, _ := filepath.Glob(pattern)
	type f struct {
		p string
		t int64
	}
	var fs []f
	for _, m := range ms {
		if fi, err := os.Stat(m); err == nil && fi.Mode().IsRegular() {
			fs = append(fs, f{m, fi.ModTime().UnixNano()})
		}
	}
	if len(fs) == 0 {
		return ""
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].t > fs[j].t })
	return fs[0].p
}

// copyFile copies src over dst through a temporary file in dst's folder.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".wy-move-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	_ = os.Chmod(tmp.Name(), 0o600)
	return os.Rename(tmp.Name(), dst)
}
