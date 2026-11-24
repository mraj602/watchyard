package doctor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/client"
	"github.com/mraj602/watchyard/internal/config"
	"github.com/mraj602/watchyard/internal/identity"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/paths"
)

// Level is the verdict of one check.
type Level string

const (
	OK   Level = "ok"
	Warn Level = "warn"
	Fail Level = "fail"
)

// Check is one line of the report.
type Check struct {
	Name   string `json:"name"`
	Slot   string `json:"slot,omitempty"`
	Level  Level  `json:"level"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"` // one line, present on warn and fail
}

// Report is the result of Run.
type Report struct {
	Checks []Check `json:"checks"`
	OK     int     `json:"ok"`
	Warn   int     `json:"warn"`
	Fail   int     `json:"fail"`
}

// Failed reports whether any check failed: the only case where `wy doctor` exits non-zero.
func (r Report) Failed() bool { return r.Fail > 0 }

func (r *Report) add(c Check) {
	r.Checks = append(r.Checks, c)
	switch c.Level {
	case OK:
		r.OK++
	case Warn:
		r.Warn++
	default:
		r.Fail++
	}
}

// Options is everything Run looks at; all of it is injectable so tests use a fake home.
type Options struct {
	Home     string
	Dirs     paths.Dirs
	Getenv   func(string) string
	Environ  []string
	LookPath func(string) (string, error) // nil: exec.LookPath
	Run      identity.Runner              // nil: the real command (only `claude --version`)
	FS       identity.FS                  // nil: identity.OSFS
	Now      func() time.Time
	// BoardProbe asks the hub who the board token is (GET /v1/me); nil: the real call, 3 s
	// timeout, through hub.dial, no redirects. Tests inject a fake.
	BoardProbe func(ctx context.Context, hubURL, dial, token string) (BoardMe, error)
	// CertProbe asks the hub for this machine's certificate (GET /v1/devices/self/cert,
	// ErrNoCert for none); nil: the real call with the machine token, 3 s timeout.
	CertProbe func(ctx context.Context) (CertInfo, error)
	// Rebaseline re-records which settings.json files are symlinks instead of warning.
	Rebaseline bool
	// HealthProbe asks a remote hub for GET /health; nil: the real call, 3 s, through
	// hub.dial. Tests inject a fake.
	HealthProbe func(ctx context.Context, hubURL, dial string) (client.Health, error)
	// HubListen is the hub's listen address for hints; "" = the installed hub service's
	// --listen (service.InstalledListen). Tests set it.
	HubListen string
}

func (o *Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Run performs every check. It never changes the user's files: it writes only inside the
// state folder (doctor.json, the identity cache, a probe file in the outbox folder).
func Run(ctx context.Context, o Options) Report {
	if o.FS == nil {
		o.FS = identity.OSFS{}
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	var rep Report

	cfg := checkConfig(&rep, o)
	claudeVersion := checkClaude(ctx, &rep, o)

	loc := identity.Locator{Home: o.Home, FS: o.FS}
	slots, err := loc.Discover(cfg.Slots.Extra, o.Getenv)
	if err != nil {
		rep.add(Check{Name: "slots", Level: Fail, Detail: err.Error(), Fix: "check that the home folder is readable"})
	} else if len(slots) == 0 {
		rep.add(Check{Name: "slots", Level: Warn, Detail: "no Claude config folder found",
			Fix: "run claude once, or list folders in config.toml: wy config set slots.extra <folder>"})
	} else {
		names := make([]string, len(slots))
		for i, s := range slots {
			names[i] = s.Name
		}
		rep.add(Check{Name: "slots", Level: OK, Detail: fmt.Sprintf("%d found: %s", len(slots), strings.Join(names, " "))})
	}

	store := &identity.Store{StateDir: o.Dirs.State.Path, FS: o.FS, Now: o.now}
	for _, s := range slots {
		if !checkLogin(&rep, o, store, s, cfg.Slots.Labels[s.Name]) {
			continue // a logged-out folder is not in use: its hooks and status line do not matter yet
		}
		set, serr := readSettings(o.FS, filepath.Join(s.Dir, "settings.json"))
		checkHooks(&rep, s, set, serr)
		checkStatusline(&rep, s, set, serr)
		checkMoverHooks(&rep, s, set, serr, cfg.Account.SmartSwitch)
	}
	checkSymlinks(&rep, o, slots)
	checkSetup(&rep, o, cfg, slots)
	checkTimerUnit(&rep, o, cfg)
	checkAgent(&rep, o, cfg)
	checkOutbox(&rep, o, cfg)
	checkMachine(ctx, &rep, o, cfg)
	checkPush(ctx, &rep, o, cfg)
	checkTerminal(ctx, &rep, o, cfg, claudeVersion)
	return rep
}

func checkConfig(rep *Report, o Options) config.Config {
	rep.add(Check{Name: "folders", Level: OK, Detail: fmt.Sprintf("config %s (%s), state %s (%s), data %s (%s)",
		o.Dirs.Config.Path, o.Dirs.Config.Describe(), o.Dirs.State.Path, o.Dirs.State.Describe(),
		o.Dirs.Data.Path, o.Dirs.Data.Describe())})
	f, err := config.Load(o.Dirs.Config.Path)
	if err != nil {
		rep.add(Check{Name: "config", Level: Fail, Detail: err.Error(), Fix: "fix or remove config.toml (wy config get/set edit it safely)"})
		return config.Defaults()
	}
	cfg, err := f.Typed()
	if err != nil {
		rep.add(Check{Name: "config", Level: Fail, Detail: err.Error(), Fix: "correct the value with: wy config set <key> <value>"})
		return config.Defaults()
	}
	if unk := f.Unknown(); len(unk) > 0 {
		rep.add(Check{Name: "config", Level: Warn, Detail: "unknown keys kept as they are: " + strings.Join(unk, ", "),
			Fix: "check the spelling against docs/spec/paths.md section 4"})
	} else {
		rep.add(Check{Name: "config", Level: OK, Detail: f.Path()})
	}
	return cfg
}

// checkClaude adds the claude line and returns the version string ("" when unknown).
func checkClaude(ctx context.Context, rep *Report, o Options) string {
	look := o.LookPath
	if look == nil {
		look = lookPath
	}
	p, err := look("claude")
	if err != nil {
		rep.add(Check{Name: "claude", Level: Fail, Detail: "claude not found on PATH",
			Fix: "install Claude Code and make sure `claude` is on PATH (the status line and wy agents need it)"})
		return ""
	}
	run := o.Run
	if run == nil {
		run = identity.ExecRunner
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := run(ctx, o.Environ, p, "--version")
	ver := strings.TrimSpace(string(out))
	if err != nil || ver == "" {
		rep.add(Check{Name: "claude", Level: Warn, Detail: p + ": version unknown",
			Fix: "run `claude --version` yourself and check it works"})
		return ""
	}
	ver, _, _ = strings.Cut(ver, "\n")
	rep.add(Check{Name: "claude", Level: OK, Detail: p + " (" + ver + ")"})
	return ver
}

// checkLogin adds the login line of a slot and reports whether the slot is logged in.
func checkLogin(rep *Report, o Options, store *identity.Store, s identity.Slot, label string) bool {
	name := "login"
	login, err := store.Login(s)
	prefix := ""
	if label != "" {
		prefix = label + ": "
	}
	switch {
	case err != nil:
		rep.add(Check{Name: name, Slot: s.Name, Level: Warn, Detail: "cannot read the login: " + err.Error(),
			Fix: "run " + loginCmd(s) + " status --json to see what claude says"})
		return false
	case !login.LoggedIn(): // identity.md 4: the same rule as wy account next
		d := "logged out"
		switch {
		case login.HasSeat:
			d = "logged out (claude auth status; the state file still names " + login.Seat.Email + ")"
		case login.AuthStatusAt.IsZero():
			d = "logged out (no login in the state file)"
		}
		rep.add(Check{Name: name, Slot: s.Name, Level: Warn, Detail: d + "; hooks and status line not checked",
			Fix: "log in with: " + loginCmd(s) + " login (or ignore this folder)"})
		return false
	default:
		who := login.Seat.Email
		if who == "" {
			who = "account " + short(login.Seat.Account)
		}
		rep.add(Check{Name: name, Slot: s.Name, Level: OK, Detail: prefix + who + " (" + login.Seat.Src + ")"})
	}
	return true
}

// loginCmd is the claude command line for a slot's auth commands (identity.md section 4:
// ~/.claude runs with CLAUDE_CONFIG_DIR unset).
func loginCmd(s identity.Slot) string {
	if s.Default {
		return "claude auth"
	}
	return "CLAUDE_CONFIG_DIR=" + s.Name + " claude auth"
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func checkOutbox(rep *Report, o Options, cfg config.Config) {
	dir := filepath.Join(o.Dirs.State.Path, "outbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		rep.add(Check{Name: "outbox", Level: Fail, Detail: "cannot create " + dir + ": " + err.Error(),
			Fix: "make the state folder writable, or point WY_STATE_DIR at a writable folder"})
		return
	}
	probe, err := os.CreateTemp(dir, ".doctor-probe-*")
	if err != nil {
		rep.add(Check{Name: "outbox", Level: Fail, Detail: "not writable: " + err.Error(),
			Fix: "make " + dir + " writable; hooks cannot record anything until it is"})
		return
	}
	probe.Close()
	os.Remove(probe.Name())
	rep.add(Check{Name: "outbox", Level: OK, Detail: "writable: " + dir})

	st, err := outbox.New(dir).Stat()
	if err != nil {
		rep.add(Check{Name: "outbox size", Level: Warn, Detail: "cannot measure: " + err.Error(),
			Fix: "run wy outbox stat for the details"})
		return
	}
	size := fmt.Sprintf("%s in %d day and %d spill files, %s not yet flushed", humanBytes(st.Bytes), st.DayFiles, st.SpillFiles, humanBytes(st.UnflushedBytes))
	if limit := int64(cfg.Outbox.MaxMB) << 20; cfg.Outbox.MaxMB > 0 && st.UnflushedBytes > limit {
		rep.add(Check{Name: "outbox size", Level: Warn, Detail: size + fmt.Sprintf(" (over the %d MB cap)", cfg.Outbox.MaxMB),
			Fix: "connect a hub so records can be flushed, or raise outbox.max_mb"})
	} else {
		rep.add(Check{Name: "outbox size", Level: OK, Detail: size})
	}

	hub := cfg.Hub.URL
	switch {
	case st.OldestUnflushed.IsZero():
		rep.add(Check{Name: "oldest unflushed", Level: OK, Detail: "none"})
	case hub == "":
		rep.add(Check{Name: "oldest unflushed", Level: OK,
			Detail: fmt.Sprintf("%s ago (no hub configured: records stay local for wy status)", age(o.now().Sub(st.OldestUnflushed)))})
	case o.now().Sub(st.OldestUnflushed) > 15*time.Minute:
		rep.add(Check{Name: "oldest unflushed", Level: Warn, Detail: age(o.now().Sub(st.OldestUnflushed)) + " ago",
			Fix: "run wy flush and read its error; check the hub is reachable"})
	default:
		rep.add(Check{Name: "oldest unflushed", Level: OK, Detail: age(o.now().Sub(st.OldestUnflushed)) + " ago"})
	}
	checkFlushStatus(rep, o, dir, hub)
}

// checkFlushStatus shows what the last `wy flush` run found (flush.status.json): a token
// the hub rejected, a hub that refuses records, any other error, and the last success.
// Nothing is shown without a hub or before the first flush.
func checkFlushStatus(rep *Report, o Options, dir, hub string) {
	if hub == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(dir, client.StatusFile)); err != nil {
		return
	}
	fs := client.ReadStatus(dir)
	last := "no successful flush yet"
	if t, err := time.Parse(time.RFC3339Nano, fs.LastSuccess); err == nil {
		last = "last success " + age(o.now().Sub(t)) + " ago"
	}
	switch {
	case fs.AuthFailed:
		rep.add(Check{Name: "flush", Level: Fail, Detail: "token rejected by the hub (" + last + "): " + fs.Error,
			Fix: "create a new token on the hub and run: wy connect " + hub + " --token-stdin (paste the token on standard input)"})
	case fs.RefusesRecords:
		rep.add(Check{Name: "flush", Level: Fail, Detail: "hub refuses records (" + last + "): " + fs.Error,
			Fix: "the hub or a proxy in front of it answers 400/413 to everything; check its logs and size limits; nothing is dropped meanwhile"})
	case !fs.OK && fs.Error != "":
		rep.add(Check{Name: "flush", Level: Warn, Detail: "last flush failed (" + last + "): " + fs.Error,
			Fix: "run wy flush by hand and read its error; check the hub is reachable"})
	default:
		rep.add(Check{Name: "flush", Level: OK, Detail: last})
	}
}

func age(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return d.String()
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// WriteText prints the report for people.
func (r Report) WriteText(w io.Writer) {
	for _, c := range r.Checks {
		name := c.Name
		if c.Slot != "" {
			name += " " + c.Slot
		}
		fmt.Fprintf(w, "%-4s  %-26s %s\n", strings.ToUpper(string(c.Level)), name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(w, "      %-26s fix: %s\n", "", c.Fix)
		}
	}
	fmt.Fprintf(w, "\n%d ok, %d warn, %d fail\n", r.OK, r.Warn, r.Fail)
}
