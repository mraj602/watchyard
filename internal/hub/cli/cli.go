// Package cli implements `wy hub …`: serve, pair, owner-link, token create|list|revoke,
// backup and prune, and `wy phone pair`. cmd/wy only routes here.
//
// Depends on: paths, hub/store, hub/auth, hub/api, hub/ingest, hub/pair, hub/pull,
// hub/push/{clock,engine,model,webpush,ntfy,link,refresh}, webui.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/mraj602/watchyard/internal/hub/api"
	"github.com/mraj602/watchyard/internal/hub/appmirror"
	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/calibrate"
	"github.com/mraj602/watchyard/internal/hub/hublock"
	"github.com/mraj602/watchyard/internal/hub/ingest"
	hubkeys "github.com/mraj602/watchyard/internal/hub/keys"
	hublink "github.com/mraj602/watchyard/internal/hub/link"
	"github.com/mraj602/watchyard/internal/hub/pair"
	"github.com/mraj602/watchyard/internal/hub/peerrelay"
	"github.com/mraj602/watchyard/internal/hub/pull"
	"github.com/mraj602/watchyard/internal/hub/push/clock"
	"github.com/mraj602/watchyard/internal/hub/push/engine"
	pushlink "github.com/mraj602/watchyard/internal/hub/push/link"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/ntfy"
	"github.com/mraj602/watchyard/internal/hub/push/refresh"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/hub/push/webpush"
	"github.com/mraj602/watchyard/internal/hub/store"
	"github.com/mraj602/watchyard/internal/keys"
	"github.com/mraj602/watchyard/internal/localtime"
	"github.com/mraj602/watchyard/internal/paths"
	"github.com/mraj602/watchyard/internal/service"
	"github.com/mraj602/watchyard/internal/version"
	"github.com/mraj602/watchyard/webui"
)

// ErrUsage means the arguments were wrong and usage was printed.
var ErrUsage = errors.New("usage")

const usage = `Usage: wy hub <command> [flags]

Commands:
  serve              Run the hub (HTTP API, live stream, web board, alerts, pushes and pulls)
                     [--listen 127.0.0.1:8787] [--public-url https://<name>.ts.net] [--no-push] [--no-pull]
  pair               Make a one-time pairing code for a phone, browser or machine (--kind ...)
  owner-link         Make a one-time sign-in link for the board's owner settings
  token create       Make a device token: --kind machine|desktop|phone|browser --name X [--machine ID]
  token list         List devices (--all includes revoked)
  token revoke ID    Revoke a device by id, token hint or name
  backup             Write a consistent copy of the database (--to file)
  refold             Rebuild every session's state and times from the stored records [--dry-run]
  prune              Delete raw records and fine-grained rows older than --older-than 90d / --before DATE
  pull               Pull servers over SSH: add|list|run|trust|enable|disable|remove|key
  export / import    Move the hub: freeze and pack it / unpack it into an empty hub
  unfreeze           Call off a move: the hub accepts records again
  machine show ID    One machine's hub-side state, for wy machine add
  keys show          The hub key, the owner key and pending proposals
  keys confirm-owner FP  Activate a pending owner key proposal
  keys register / certify  A machine's link keys over SSH, for wy machine add

Every command takes --db <file> (default <data>/hub.db, see wy paths).
wy hub pair --help and wy hub owner-link --help show their flags.
`

// Run is `wy hub <args>`.
func Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		if len(args) == 0 {
			return ErrUsage
		}
		return nil
	}
	switch args[0] {
	case "serve":
		return serve(args[1:], stdout, stderr)
	case "pair":
		return Pair(args[1:], stdout, stderr)
	case "owner-link":
		return OwnerLink(args[1:], stdout, stderr)
	case "token":
		if len(args) < 2 {
			fmt.Fprint(stderr, usage)
			return ErrUsage
		}
		switch args[1] {
		case "create":
			return tokenCreate(args[2:], stdout, stderr)
		case "list":
			return tokenList(args[2:], stdout, stderr)
		case "revoke":
			return tokenRevoke(args[2:], os.Stdin, stdout, stderr)
		}
	case "backup":
		return backup(args[1:], stdout, stderr)
	case "prune":
		return prune(args[1:], stdout, stderr)
	case "refold":
		return refold(args[1:], stdout, stderr)
	case "pull":
		return Pull(args[1:], stdout, stderr)
	case "export":
		return Export(args[1:], stdout, stderr)
	case "import":
		return Import(args[1:], stdout, stderr)
	case "unfreeze":
		return Unfreeze(args[1:], stdout, stderr)
	case "machine":
		return Machine(args[1:], stdout, stderr)
	case "keys":
		return Keys(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "wy hub: unknown command %q\n\n%s", strings.Join(args, " "), usage)
	return ErrUsage
}

// DefaultDB is <data>/hub.db.
func DefaultDB() (string, error) {
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return "", err
	}
	return filepath.Join(d.Data.Path, "hub.db"), nil
}

func newFlags(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("wy hub "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	db := fs.String("db", "", "database file (default <data>/hub.db)")
	return fs, db
}

// parse parses flags, allowing them before and after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			for _, a := range pos {
				if len(a) > 2 && strings.HasPrefix(a, "--") {
					return nil, fmt.Errorf("%q was not read as a flag (after --?): nothing done", a)
				}
			}
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func openStore(ctx context.Context, db string, logf func(string, ...any)) (*store.Store, error) {
	if db == "" {
		var err error
		if db, err = DefaultDB(); err != nil {
			return nil, err
		}
	}
	return store.Open(ctx, db, store.Options{Log: logf})
}

// isLoopback reports whether a listen address only accepts local connections.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func serve(args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("serve", stderr)
	listen := fs.String("listen", "127.0.0.1:8787", "address to listen on")
	fs.StringVar(listen, "addr", "127.0.0.1:8787", "alias of --listen")
	insecure := fs.Bool("insecure-lan", false, "allow a non-loopback address without TLS in front (a proxy or tailscale serve must provide it)")
	trustedProxy := fs.String("trusted-proxy", os.Getenv("WY_TRUSTED_PROXY"), "comma-separated CIDRs whose X-Forwarded-For/-Proto headers are trusted, besides loopback (also: WY_TRUSTED_PROXY); for a hub in Docker, the bridge network")
	quiet := fs.Bool("quiet", false, "do not log each request")
	publicURL := fs.String("public-url", "", "the hub's https address phones open, e.g. https://hub.tail1234.ts.net (stored as hub.public_url)")
	noPush := fs.Bool("no-push", false, "run without the alert engine and push channels")
	noPull := fs.Bool("no-pull", false, "do not pull servers over SSH (wy hub pull targets wait)")
	noAppMirror := fs.Bool("no-app-mirror", false, "do not mirror the Android app release from GitHub (also: setting app.android_mirror=false)")
	appRepo := fs.String("app-repo", "", "GitHub owner/name the Android app mirror reads (default the build's repository)")
	pullDirFlag := fs.String("pull-dir", "", "the pull key and generated ssh files (default <state>/pull)")
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 {
		if err == nil {
			fmt.Fprintf(stderr, "wy hub serve: unexpected argument %q\n", pos[0])
		}
		return ErrUsage
	}
	if !isLoopback(*listen) && !*insecure {
		return fmt.Errorf("refusing to listen on %s without TLS: the hub speaks plain HTTP. Put it behind tailscale serve or a reverse proxy and pass --insecure-lan, or listen on 127.0.0.1", *listen)
	}
	proxies, err := auth.ParseTrustedProxies(*trustedProxy)
	if err != nil {
		return fmt.Errorf("--trusted-proxy: %w", err)
	}
	auth.SetTrustedProxies(proxies)
	pub := ""
	if *publicURL != "" {
		origin, _, err := pair.NormalizeURL(*publicURL, "machine", true)
		if err != nil {
			return fmt.Errorf("--public-url: %w", err)
		}
		pub = origin
		if strings.HasPrefix(pub, "http://") {
			fmt.Fprintf(stderr, "wy hub serve: warning: %s is plain http; phones can pair but cannot install the app or get pushes\n", pub)
		}
	}
	logger := log.New(stderr, "hub: ", log.LstdFlags|log.LUTC)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hooks, err := newServeHooks(logger.Printf)
	if err != nil {
		return err
	}
	defer hooks.Close()
	clk := hooks.Clock
	var now func() time.Time
	if hooks.Fake {
		now = clk.Now
	}
	path := *db
	if path == "" {
		if path, err = DefaultDB(); err != nil {
			return err
		}
	}
	// One hub per data folder: the lock is what `wy hub import` checks (machines.md 8.2).
	hl, err := hublock.Acquire(filepath.Dir(path))
	if err != nil {
		if errors.Is(err, hublock.ErrHeld) {
			return fmt.Errorf("%w (%s): stop it first, or use another --db", err, filepath.Join(filepath.Dir(path), hublock.File))
		}
		return err
	}
	defer hl.Release()
	st, err := store.Open(ctx, path, store.Options{Log: logger.Printf, Now: now})
	if err != nil {
		return err
	}
	defer st.Close()
	if pub != "" {
		v, _ := json.Marshal(pub)
		if err := st.PutPushSetting(ctx, store.AlertKeyPublicURL, v); err != nil {
			return err
		}
	}
	// Listen before anything starts, so a busy port changes nothing.
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	cfg := api.Config{Store: st, WebUI: webui.FS(), Now: now, PublicURL: pub}
	if !*quiet {
		cfg.Log = logger
	}
	// The hub signing key (keys.md 3.4), made at the first serve; then every stored key
	// document is checked again, and rows that do not verify are logged and ignored.
	ks, err := keysService(st, logger)
	if err != nil {
		return err
	}
	if n, err := ks.VerifyStored(ctx); err != nil {
		return fmt.Errorf("checking stored key documents: %w", err)
	} else if n > 0 {
		logger.Printf("keys: %d stored key documents do not verify and are ignored (see above)", n)
	}
	logger.Printf("hub key %s", keys.GroupFP(ks.Key.FP))
	cfg.Keys = ks
	// The Android app mirror (phone.md P16.2): served at /v1/app/android, refreshed daily.
	// Fake-clock test runs never reach out.
	var appMirror *appmirror.Mirror
	if !hooks.Fake {
		appMirror = newAppMirror(st, filepath.Dir(path), *appRepo, version.Repo, *noAppMirror, logger.Printf)
		cfg.AppMirror = appMirror
	}
	// The link (link.md): made before the server (the server routes it) and the engine (it
	// is a push channel); it reaches the server's action and settings through srv, set below.
	var srv *api.Server
	// The peer relay (peers.md 8.3) answers the machines' peer frames on the link.
	relay := peerrelay.New(st, ks, now, logger)
	lk := hublink.New(hublink.Config{Store: st, Keys: ks, Now: now, Log: logger, Peers: relay, PeerExpired: relay.Expired,
		Settings: func(ctx context.Context) hublink.Settings {
			urls, ping := srv.LinkSettings(ctx)
			return hublink.Settings{URLs: urls, PingS: ping}
		},
		Action: func(ctx context.Context, token string) (any, bool, int) { return srv.LinkAction(ctx, token) },
	})
	cfg.Link = lk
	relay.Link = lk
	cfg.PeerThreads = relay // "Resume thread" (peers.md 6.3)
	// A new matrix goes to every machine at once (link.md 7); the sweeper catches the rest.
	// A pause also drops the queued peer messages (peers.md 11).
	ks.OnMatrix = func(d keys.Doc) {
		if err := relay.MatrixChanged(context.Background(), d); err != nil {
			logger.Printf("peers: applying the matrix: %v", err)
		}
		if err := lk.PublishDocs(context.Background()); err != nil {
			logger.Printf("link: publishing the matrix: %v", err)
		}
	}
	var eng *engine.Engine
	var linkSender *pushlink.Sender
	if !*noPush {
		senders, err := pushSenders(ctx, st, pub, hooks)
		if err != nil {
			return err
		}
		linkSender = pushlink.New(lk) // the Android app's channel (push.md 13)
		senders = append(senders, linkSender)
		cfg.Senders = map[string]model.Sender{}
		list := make([]model.Sender, 0, len(senders))
		for _, s := range senders {
			cfg.Senders[s.Kind()] = s
			list = append(list, s)
		}
		eng = engine.New(engine.Config{Store: st, Clock: clk, Senders: list, TZ: rules.SystemZone(), PublicURL: pub, Log: logger,
			Link: lk})
		cfg.Engine = eng // only when non-nil: a typed nil in the interface would be called
	}
	// The pull scheduler ingests through the API's ingester (one change feed for the stream
	// and the engine), so it is made after the server; Config.Puller reaches it through ref.
	ref := &pullerRef{}
	if !*noPull {
		cfg.Puller = ref
	}
	srv = api.New(cfg)
	if err := lk.Start(ctx); err != nil {
		return fmt.Errorf("link: %w", err)
	}
	defer lk.Close()
	var sched *pull.Scheduler
	if !*noPull {
		dir, err := pullDir(*pullDirFlag)
		if err != nil {
			return err
		}
		sched = pull.New(pull.Config{Store: st, Ingester: srv.Ingest, Dir: dir, HubVersion: version.Version, Now: now,
			Logf: func(f string, a ...any) { logger.Printf("pull: "+f, a...) }})
		ref.set(sched)
	}
	var refresher *refresh.Refresher
	if eng != nil {
		eng.SetBroker(srv.Broker)
		if err := eng.Start(ctx); err != nil {
			return fmt.Errorf("alert engine: %w", err)
		}
		hooks.Attach(eng)
		// After the engine (push.md 13.5): widget refresh frames on session and usage changes.
		refresher = refresh.NewRefresher(refresh.RefresherOptions{Store: st, Sender: linkSender, Broker: srv.Broker,
			Clock: clk, Log: logger})
		refresher.Start()
		hooks.AttachRefresh(refresher)
	}
	hs := srv.HTTPServer(*listen)
	hs.RegisterOnShutdown(srv.Close)
	pushNote := "pushes on"
	if eng == nil {
		pushNote = "pushes off (--no-push)"
	}
	logger.Printf("%s listening on http://%s (database %s, schema v%d, %s)", version.String(), ln.Addr(), st.Path(), store.LatestVersion(), pushNote)
	if pub == "" {
		if u, _, _ := st.PairSetting(ctx, store.AlertKeyPublicURL); u != "" {
			logger.Printf("public address %s (hub.public_url)", u)
		} else {
			logger.Printf("no public address set: phones need one (%s, then --public-url https://<name>.ts.net)", service.ServeHint(ln.Addr().String()))
		}
	}
	// The first-start owner link (pairing.md 4.1): once per start, only while the hub has
	// never had an owner session, as one block on the log.
	// On a terminal it is printed there. Anywhere else (journal, docker logs, a log file) a log
	// outlives the ten minutes and is readable by more people than the owner, so the block goes
	// to a 0600 file next to the database and the log names only the file.
	linkFile := filepath.Join(filepath.Dir(path), FirstOwnerLinkFile)
	if block, err := FirstOwnerLink(ctx, st, pair.NewService(st, api.PairPepperPath), serveURL(ctx, st, pub, ln.Addr()), "serve",
		writerIsTerminal(stderr)); err != nil {
		logger.Printf("could not make a first owner link (%v): run wy hub owner-link", err)
	} else if block != "" {
		deliverFirstOwnerLink(logger.Printf, stderr, writerIsTerminal(stderr), linkFile, block)
	} else {
		_ = os.Remove(linkFile) // an owner exists: a link left by an earlier start is stale
	}
	if fr, err := st.Frozen(ctx); err == nil && fr != nil {
		movedTo := "nowhere set"
		if fr.MovedTo != nil && *fr.MovedTo != "" {
			movedTo = *fr.MovedTo
		}
		logger.Printf("this hub is FROZEN for a move since %s (moved to %s): ingest answers 503, pulls and pushes are paused; call the move off with `wy hub unfreeze`",
			time.UnixMilli(fr.Since).UTC().Format(time.RFC3339), movedTo)
	}
	if sched != nil {
		// After the API is built, before HTTP serves: a pull and an upload share the ingester.
		sched.Start(ctx)
		logPullStart(ctx, st, logger)
	}
	mctx, mcancel := context.WithCancel(context.Background())
	mdone := make(chan struct{})
	if appMirror != nil {
		go appMirror.Run(mctx, appMirrorEvery)
	}
	go func() { defer close(mdone); maintain(mctx, st, clk, logger) }()
	cdone := make(chan struct{})
	var notify calibrate.Notifier
	if eng != nil {
		notify = eng.Notices // the usage notices (push.md 3.9)
	}
	go func() { defer close(cdone); calibrate.Loop(mctx, st, clk.Now, notify, logger.Printf) }()
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	var serveErr error
	select {
	case serveErr = <-errc:
	case <-ctx.Done():
		logger.Printf("shutting down")
	}
	// Order (push.md 10): end the live streams (Shutdown would wait for them), drain HTTP,
	// then stop the scheduler, so no ingest reaches a stopped engine.
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	srv.Close()
	lk.Close() // a draining notice and 1001 on every link: Shutdown does not wait for hijacked connections
	shutErr := hs.Shutdown(sctx)
	if sched != nil {
		// Running sessions end at their next frame; their ingests still reach the engine.
		sched.Stop()
		ref.set(nil)
	}
	if refresher != nil {
		refresher.Stop()
	}
	if eng != nil {
		eng.Stop()
	}
	mcancel()
	<-mdone
	<-cdone
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	if shutErr != nil {
		return shutErr
	}
	fmt.Fprintln(stdout, "hub stopped")
	return nil
}

// serveURL is the address the first owner link of serve names: --public-url, else the
// hub.public_url setting, else the listen address (a wildcard address as 127.0.0.1).
func serveURL(ctx context.Context, st *store.Store, pub string, addr net.Addr) string {
	if pub != "" {
		return pub
	}
	if u, _, _ := st.PairSetting(ctx, store.AlertKeyPublicURL); u != "" {
		return u
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// writerIsTerminal reports whether w is a terminal (the QR gets black-on-white colour codes).
func writerIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && isTerminal(f)
}

// pullerRef is api.Config.Puller for serve: the scheduler is made after the API (it needs
// the API's ingester), and is gone again after shutdown.
type pullerRef struct {
	mu sync.Mutex
	s  *pull.Scheduler
}

func (r *pullerRef) set(s *pull.Scheduler) {
	r.mu.Lock()
	r.s = s
	r.mu.Unlock()
}

// RunNow implements api.Puller.
func (r *pullerRef) RunNow(ctx context.Context, machineID string) (api.PullStatus, error) {
	r.mu.Lock()
	s := r.s
	r.mu.Unlock()
	if s == nil {
		return api.PullStatus{}, errors.New("the pull scheduler is not running")
	}
	return s.RunNow(ctx, machineID)
}

// logPullStart says how many pull targets the scheduler serves, and warns when pull mode
// cannot work here (no ssh client, no key yet).
func logPullStart(ctx context.Context, st *store.Store, logger *log.Logger) {
	targets, err := st.PullTargets(ctx)
	if err != nil || len(targets) == 0 {
		return
	}
	enabled := 0
	for _, t := range targets {
		if t.Enabled {
			enabled++
		}
	}
	logger.Printf("pull: %d target(s), %d enabled", len(targets), enabled)
	if _, err := exec.LookPath("ssh"); err != nil && enabled > 0 {
		logger.Printf("pull: warning: no ssh client on this host; pulled servers cannot be reached (the Docker image needs openssh-client)")
	}
}

// pushSenders builds the Web Push and ntfy senders (serve adds the link sender). Both read the push settings on every
// send (vapid_subject, webpush_hosts, snooze_min), so a settings change needs no restart.
func pushSenders(ctx context.Context, st *store.Store, pub string, hooks serveHooks) ([]model.Sender, error) {
	keys, err := webpush.LoadVAPID(ctx, st)
	if err != nil {
		return nil, err
	}
	settings := func() model.Settings {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		raw, _ := st.PushSetting(c, store.AlertKeyPushSettings)
		s, err := model.ParseSettings(raw, "")
		if err != nil {
			s.Defaults()
		}
		return s
	}
	subject := func() string {
		if s := settings().VapidSubject; s != "" {
			return s
		}
		if pub != "" {
			return pub
		}
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		u, _, _ := st.PairSetting(c, store.AlertKeyPublicURL)
		return u
	}
	wp := webpush.New(webpush.Options{
		Keys:    keys,
		Subject: subject,
		Hosts:   func() []string { return settings().WebPushHosts },
		Client:  hooks.WebPushClient, // nil: the guarded client
	})
	nf := ntfy.New(ntfy.Options{SnoozeMin: func() int { return settings().SnoozeMin }})
	return []model.Sender{wp, nf}, nil
}

// maintainEvery is how often the hub's own housekeeping runs.
const maintainEvery = 24 * time.Hour

// maintain runs the hub's daily housekeeping until ctx ends: ended pairing codes older
// than 30 days are deleted (pairing.md 2.1). It runs once at start.
func maintain(ctx context.Context, st *store.Store, clk clock.Clock, logger *log.Logger) {
	for {
		before := clk.Now().Add(-30 * 24 * time.Hour).UnixMilli()
		c, cancel := context.WithTimeout(ctx, time.Minute)
		if n, err := st.PrunePairCodes(c, before); err != nil && ctx.Err() == nil {
			logger.Printf("maintenance: prune pairing codes: %v", err)
		} else if n > 0 {
			logger.Printf("maintenance: deleted %d ended pairing codes", n)
		}
		cancel()
		t := time.NewTimer(maintainEvery)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func tokenCreate(args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("token create", stderr)
	kind := fs.String("kind", "", "device kind: machine, desktop, phone, browser")
	fs.StringVar(kind, "scope", "", "alias of --kind")
	name := fs.String("name", "", "device name")
	machine := fs.String("machine", "", "machine id (machine and desktop; wy machine prints it)")
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 {
		return ErrUsage
	}
	ctx := context.Background()
	st, err := openStore(ctx, *db, nil)
	if err != nil {
		return err
	}
	defer st.Close()
	c, err := auth.Create(ctx, st, auth.CreateRequest{Kind: *kind, Name: *name, Machine: *machine})
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(map[string]any{"device": c.Device.ID, "token": c.Token, "scopes": c.Device.Scopes})
	}
	fmt.Fprintf(stdout, "%s\n\nDevice %s (%s, %s), scopes %s.\nThe token is shown once; the hub keeps only its hash.\n",
		c.Token, c.Device.ID, c.Device.Kind, c.Device.Name, strings.Join(c.Device.Scopes, "+"))
	return nil
}

func ms(v int64) string {
	if v == 0 {
		return "-"
	}
	return localtime.MS(v)
}

func tokenList(args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("token list", stderr)
	all := fs.Bool("all", false, "include revoked devices")
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 {
		return ErrUsage
	}
	ctx := context.Background()
	st, err := openStore(ctx, *db, nil)
	if err != nil {
		return err
	}
	defer st.Close()
	ds, err := st.Devices(ctx, *all)
	if err != nil {
		return err
	}
	if *asJSON {
		out := []map[string]any{}
		for _, d := range ds {
			out = append(out, map[string]any{"id": d.ID, "kind": d.Kind, "name": d.Name, "machine": d.MachineID, "hint": d.TokenHint,
				"scopes": d.Scopes, "created_at": store.Time(d.CreatedAt), "created_by": d.CreatedBy,
				"last_used_at": store.Time(d.LastUsedAt), "revoked_at": store.Time(d.RevokedAt)})
		}
		return json.NewEncoder(stdout).Encode(out)
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKIND\tNAME\tMACHINE\tHINT\tSCOPES\tCREATED\tLAST USED\tREVOKED")
	for _, d := range ds {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Kind, d.Name, orDash(d.MachineID), d.TokenHint,
			strings.Join(d.Scopes, "+"), ms(d.CreatedAt), ms(d.LastUsedAt), ms(d.RevokedAt))
	}
	return tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func tokenRevoke(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs, db := newFlags("token revoke", stderr)
	yes := fs.Bool("yes", false, "do not ask")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprintln(stderr, "usage: wy hub token revoke <device id | token hint | name> [--yes]")
		return ErrUsage
	}
	ctx := context.Background()
	st, err := openStore(ctx, *db, nil)
	if err != nil {
		return err
	}
	defer st.Close()
	ds, err := auth.Find(ctx, st, pos[0])
	if err != nil {
		return err
	}
	switch {
	case len(ds) == 0:
		return fmt.Errorf("no device matches %q (wy hub token list)", pos[0])
	case len(ds) > 1:
		var ids []string
		for _, d := range ds {
			ids = append(ids, d.ID+" ("+d.Name+")")
		}
		return fmt.Errorf("%q matches several devices: %s; use the id", pos[0], strings.Join(ids, ", "))
	}
	d := ds[0]
	if d.RevokedAt != 0 {
		fmt.Fprintf(stdout, "%s is already revoked\n", d.ID)
		return nil
	}
	if !*yes {
		fmt.Fprintf(stdout, "Revoke %s (%s, %s, %s)? [y/N] ", d.ID, d.Kind, d.Name, d.TokenHint)
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("not revoked")
		}
	}
	// With a hub key, the device's link keys also go on the next revocation list (keys.md 6).
	hk, err := existingHubKey()
	if err != nil {
		return err
	}
	if hk != nil {
		err = hubkeys.New(st, hk).RevokeDevice(ctx, d.ID, "cli", "revoked")
	} else {
		err = auth.Revoke(ctx, st, d.ID)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "revoked %s; it gets 401 from its next request\n", d.ID)
	return nil
}

func backup(args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("backup", stderr)
	to := fs.String("to", "", "target file (default <data>/backups/hub-<UTC time>.db)")
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 {
		return ErrUsage
	}
	ctx := context.Background()
	st, err := openStore(ctx, *db, nil)
	if err != nil {
		return err
	}
	defer st.Close()
	target := *to
	if target == "" {
		target = filepath.Join(filepath.Dir(st.Path()), "backups", store.BackupName(time.Now()))
	}
	if err := st.Backup(ctx, target); err != nil {
		return err
	}
	fmt.Fprintln(stdout, target)
	return nil
}

// ParseAge reads "90d", "12h", "2w" or any Go duration.
func ParseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if n := len(s); n > 1 {
		mult := map[byte]time.Duration{'d': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[s[n-1]]
		if mult > 0 {
			v, err := strconv.Atoi(s[:n-1])
			if err != nil || v < 0 {
				return 0, fmt.Errorf("bad age %q", s)
			}
			return time.Duration(v) * mult, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("bad age %q (use 90d, 2w or 12h)", s)
	}
	return d, nil
}

// refold is `wy hub refold` (states.md 5.1): the replay path for the session fold. Raw
// records are kept, so a fix to the state engine or to how sessions are made applies to
// the whole history. Safe while the hub runs (one write transaction).
func refold(args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("refold", stderr)
	dry := fs.Bool("dry-run", false, "only report what would change")
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 {
		fmt.Fprintln(stderr, "usage: wy hub refold [--dry-run] [--db hub.db]")
		return ErrUsage
	}
	ctx := context.Background()
	st, err := openStore(ctx, *db, nil)
	if err != nil {
		return err
	}
	defer st.Close()
	rep, err := ingest.Refold(ctx, st, *dry)
	if err != nil {
		return err
	}
	verb := "changed"
	if *dry {
		verb = "would change"
	}
	fmt.Fprintf(stdout, "%d sessions: %d refolded from hook events and lists, %d history (transcript only)\n", rep.Sessions, rep.Live, rep.History)
	fmt.Fprintf(stdout, "%s: %d states, %d start times, %d projects\n", verb, rep.Changed, rep.Started, rep.Project)
	states := make([]string, 0, len(rep.ByState))
	for k := range rep.ByState {
		states = append(states, k)
	}
	sort.Strings(states)
	parts := make([]string, 0, len(states))
	for _, k := range states {
		parts = append(parts, fmt.Sprintf("%s %d", k, rep.ByState[k]))
	}
	fmt.Fprintf(stdout, "states after: %s\n", strings.Join(parts, ", "))
	return nil
}

func prune(args []string, stdout, stderr io.Writer) error {
	fs, db := newFlags("prune", stderr)
	older := fs.String("older-than", "", "age, e.g. 90d")
	before := fs.String("before", "", "date (YYYY-MM-DD) or RFC 3339 time")
	dry := fs.Bool("dry-run", false, "only count")
	if pos, err := parse(fs, args); err != nil || len(pos) > 0 {
		return ErrUsage
	}
	var cut time.Time
	switch {
	case *older != "" && *before != "":
		return errors.New("give --older-than or --before, not both")
	case *older != "":
		d, err := ParseAge(*older)
		if err != nil {
			return err
		}
		cut = time.Now().Add(-d)
	case *before != "":
		t, err := time.Parse("2006-01-02", *before)
		if err != nil {
			t, err = time.Parse(time.RFC3339, *before)
		}
		if err != nil {
			return fmt.Errorf("bad --before %q", *before)
		}
		cut = t
	default:
		fmt.Fprintln(stderr, "usage: wy hub prune --older-than 90d | --before 2026-01-01 [--dry-run]")
		return ErrUsage
	}
	ctx := context.Background()
	st, err := openStore(ctx, *db, nil)
	if err != nil {
		return err
	}
	defer st.Close()
	res, err := st.Prune(ctx, cut, *dry)
	if err != nil {
		return err
	}
	verb := "removed"
	if *dry {
		verb = "would remove"
	}
	fmt.Fprintf(stdout, "before %s: %s %d records (%d hook events), %d limit samples, %d web usage responses, %d token rows, %d limit hits, %d account leases\n",
		localtime.Format(cut), verb, res.Records, res.HookEvents, res.LimitSamples, res.WebUsage, res.TokenRows, res.LimitHits, res.AccountLeases)
	fmt.Fprintln(stdout, "kept: seats, machines, slots, sessions and their states, usage windows, history days, devices")
	return nil
}
