package timer

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Installed is what the installed timer job runs with: the wy it starts, its environment
// and (systemd) its KillMode, read from the unit file and its drop-ins (Linux) or the
// LaunchAgent plist (macOS).
type Installed struct {
	Path     string      // the unit (or plist) file
	WyPath   string      // the program of ExecStart / ProgramArguments ("" when unknown)
	Env      [][2]string // Environment= values in order, the last of a name wins; %h expanded
	KillMode string      // systemd only; "control-group" when the unit sets none
	GOOS     string
}

// Get is the value of name in the job's environment ("" when unset).
func (i Installed) Get(name string) string {
	v := ""
	for _, kv := range i.Env {
		if kv[0] == name {
			v = kv[1]
		}
	}
	return v
}

// ErrNotInstalled means there is no timer unit to read.
var ErrNotInstalled = errors.New("the timer is not installed")

// ReadInstalled reads the installed timer job of e (GOOS linux or darwin). Read-only.
func ReadInstalled(e Env) (Installed, error) {
	switch e.GOOS {
	case "linux":
		return readSystemd(e)
	case "darwin":
		return readLaunchd(e)
	}
	return Installed{GOOS: e.GOOS}, ErrNotInstalled
}

func readSystemd(e Env) (Installed, error) {
	svc := filepath.Join(SystemdDir(e), SystemdName+".service")
	in := Installed{Path: svc, GOOS: "linux", KillMode: "control-group"}
	b, err := os.ReadFile(svc)
	if err != nil {
		return in, ErrNotInstalled
	}
	texts := []string{string(b)}
	drops, _ := filepath.Glob(svc + ".d/*.conf")
	sort.Strings(drops) // systemd applies them in this order: the last value wins
	for _, d := range drops {
		if b, err := os.ReadFile(d); err == nil {
			texts = append(texts, string(b))
		}
	}
	for _, t := range texts {
		for _, line := range strings.Split(t, "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			switch k {
			case "KillMode":
				in.KillMode = strings.TrimSpace(v)
			case "Environment":
				if strings.TrimSpace(v) == "" {
					in.Env = nil // an empty assignment resets the list
				}
				for _, w := range splitUnitWords(v) {
					if n, val, ok := strings.Cut(w, "="); ok && n != "" {
						in.Env = append(in.Env, [2]string{n, strings.ReplaceAll(val, "%h", e.Home)})
					}
				}
			case "ExecStart":
				if w := splitUnitWords(v); len(w) > 0 {
					in.WyPath = strings.ReplaceAll(strings.TrimLeft(w[0], "@-:+!"), "%h", e.Home)
				}
			}
		}
	}
	return in, nil
}

// splitUnitWords splits a systemd value on whitespace, honouring double and single quotes
// and backslash escapes (\xNN, \n, \t, \\, \", \').
func splitUnitWords(s string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	in := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				cur.WriteByte('\n')
			case 't':
				cur.WriteByte('\t')
			case 'x':
				if i+2 < len(s) {
					var n byte
					ok := true
					for _, h := range []byte{s[i+1], s[i+2]} {
						switch {
						case h >= '0' && h <= '9':
							n = n<<4 | (h - '0')
						case h >= 'a' && h <= 'f':
							n = n<<4 | (h - 'a' + 10)
						case h >= 'A' && h <= 'F':
							n = n<<4 | (h - 'A' + 10)
						default:
							ok = false
						}
					}
					if ok {
						cur.WriteByte(n)
						i += 2
						break
					}
				}
				cur.WriteByte('x')
			default:
				cur.WriteByte(s[i])
			}
			in = true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			quote, in = c, true
		case c == ' ' || c == '\t' || c == '\r':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

var (
	plistEnvDict = regexp.MustCompile(`(?s)<key>EnvironmentVariables</key>\s*<dict>(.*?)</dict>`)
	plistKeyStr  = regexp.MustCompile(`(?s)<key>(.*?)</key>\s*<string>(.*?)</string>`)
	plistProgArg = regexp.MustCompile(`(?s)<key>ProgramArguments</key>\s*<array>\s*<string>(.*?)</string>`)
)

func plistUnescape(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&").Replace(s)
}

func readLaunchd(e Env) (Installed, error) {
	p := LaunchAgentPath(e)
	in := Installed{Path: p, GOOS: "darwin"}
	b, err := os.ReadFile(p)
	if err != nil {
		return in, ErrNotInstalled
	}
	if m := plistProgArg.FindStringSubmatch(string(b)); m != nil {
		in.WyPath = plistUnescape(m[1])
	}
	if m := plistEnvDict.FindStringSubmatch(string(b)); m != nil {
		for _, kv := range plistKeyStr.FindAllStringSubmatch(m[1], -1) {
			in.Env = append(in.Env, [2]string{plistUnescape(kv[1]), plistUnescape(kv[2])})
		}
	}
	return in, nil
}
