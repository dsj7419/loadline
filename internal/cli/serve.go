package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/dsj7419/loadline/internal/accounts"
	"github.com/dsj7419/loadline/internal/claudecli"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/demo"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/launch"
	"github.com/dsj7419/loadline/internal/logfile"
	"github.com/dsj7419/loadline/internal/poll"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/server"
	"github.com/dsj7419/loadline/internal/serverlock"
	"github.com/dsj7419/loadline/internal/timer"
	"github.com/dsj7419/loadline/internal/tracker"
	"github.com/dsj7419/loadline/internal/web"
)

const (
	// shutdownTimeout bounds a graceful shutdown.
	shutdownTimeout = 5 * time.Second
	// drainEvery spaces the drains of the spool after the one at startup.
	drainEvery = 10 * time.Second
	// idleCheckEvery spaces the checks for a live session that decide when
	// the server has been idle long enough to exit.
	idleCheckEvery = 15 * time.Second
	// lockWait bounds a starting server's wait for the server before it to
	// close the history: that one's shutdown takes up to shutdownTimeout.
	lockWait = shutdownTimeout + 5*time.Second

	logName = "loadline.log"
	// logMaxBytes and logKeep bound the server's log: it rotates at 5 MB
	// and keeps the two files before the current one.
	logMaxBytes = 5 << 20
	logKeep     = 2
)

// ServeOptions is what Serve needs beyond the config file.
type ServeOptions struct {
	Paths config.Paths
	// Home is the user's home directory, which holds Claude Code's default
	// config dir.
	Home string
	// Port is the port to listen on. Zero means the configured one.
	Port int
	// NoOpen is the --no-open flag: the server does not open the dashboard
	// as it starts.
	NoOpen bool
	// Demo serves a made-up machine instead of this one: claude never runs,
	// the spool is left alone, and the ingest routes refuse every event, so
	// that the hooks keep theirs for the real server. A demo never takes a
	// port that is already in use.
	Demo bool
	// Runner runs claude. Nil means the claude binary the config names, or
	// the one on PATH.
	Runner claudecli.Runner
	// Clock reads the time. Nil means time.Now.
	Clock func() time.Time
	// Ticker schedules all the server's deferred work: polls, probes,
	// drains, idle checks, the wait before opening the dashboard and the
	// tracker's publishing. Nil means wall-clock timers.
	Ticker tracker.Ticker
	// Ready, when set, is called with the address the server listens on
	// once it is serving.
	Ready func(addr string)
	// Open opens the dashboard's URL in a browser. Nil means launch.Open.
	Open func(url string, b launch.Browser) error
	// BootTime returns when the machine started, which tells whether the
	// dashboard was opened earlier this boot. Nil means launch.BootTime.
	BootTime func() (time.Time, error)
	// TrustFrom, when set, is a folder that the server takes, with every
	// folder above it, as one nobody else can move: the check that no other
	// user can move the data directory aside then stops below it. Tests set
	// it to the temporary folder they work in, whose permissions they do not
	// choose. Empty means the check goes up to the volume root.
	TrustFrom string
	// BeforeHistoryClose, when set, is called as the server shuts down,
	// just before it writes what the history still holds and closes it.
	BeforeHistoryClose func()
}

// RunServe runs the server for this machine until ctx ends or no session
// has been live for the configured idle time. Its flags are --port,
// --no-open and --demo.
func RunServe(ctx context.Context, args []string, stdio IO) int {
	flags := newFlagSet("serve", stdio)
	port := flags.Int("port", 0, "listen on `port` (default from the Loadline config)")
	noOpen := flags.Bool("no-open", false, "do not open the dashboard as the server starts")
	demoMode := flags.Bool("demo", false, "serve a made-up machine instead of this one")
	if code, ok := parseFlags(flags, args); !ok {
		return code
	}
	if isSet(flags, "port") {
		if err := config.ValidatePort(*port); err != nil {
			_, _ = fmt.Fprintf(stdio.Err, "loadline serve: --port: %v\n", err)
			return exitUsage
		}
	}

	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stdio, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fail(stdio, fmt.Errorf("find the home directory: %w", err))
	}
	return Serve(ctx, ServeOptions{Paths: paths, Home: home, Port: *port, NoOpen: *noOpen, Demo: *demoMode}, stdio)
}

// errHistoryHeld is why a server gives up starting when the one before it
// still has the history open once lockWait has passed.
var errHistoryHeld = errors.New("another server still holds the data directory")

// Serve runs the server until ctx ends, it is asked to stop or no session
// has been live for the configured idle time, then shuts it down gracefully
// and returns 0. It returns 1 when it cannot start, and 0 without a word
// when another loadline already serves the port: binding the port is what
// keeps to one server per machine. A server that has stopped answering may
// still be writing the history, so once it has the port, a server waits
// for that one to close it, up to lockWait, before it opens the history
// itself. It makes the data directory private to the user each time it
// starts. Why it could not start goes to stderr and to its log file in the
// data directory; what the running server has to say goes to the log alone,
// never to stdout or stderr.
func Serve(ctx context.Context, o ServeOptions, stdio IO) int {
	trust := cmp.Or(o.TrustFrom, trustFrom)
	failToStart := func(err error) int { return startFailed(stdio, o.Paths.DataDir, trust, err) }
	cfg, err := config.Load(o.Paths.ConfigFile, o.Home)
	if err != nil {
		return failToStart(err)
	}
	port := cfg.Port
	if o.Port != 0 {
		if err := config.ValidatePort(o.Port); err != nil {
			return failToStart(err)
		}
		port = o.Port
	}
	// Before the token is read, which a list of its own may keep from the
	// user, and the log and the history are written there.
	relaxed, err := secureServedDataDir(o.Paths.DataDir, trust, o.Demo)
	if err != nil {
		return failToStart(err)
	}
	token, err := config.EnsureToken(o.Paths.TokenFile)
	if err != nil {
		return failToStart(err)
	}

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listenAddress(port))
	if err != nil {
		if refusal := listenRefusal(ctx, port, o, err); refusal != nil {
			return failToStart(refusal)
		}
		return exitOK
	}
	// lockless is why the history goes without its lock, on a file system
	// that takes none.
	var lockless error
	if !o.Demo {
		// A demo holds no history. Released last, once the log has closed
		// too, so that the next server opens neither while this one has it.
		lock, err := lockHistory(o)
		if lock != nil {
			defer func() { _ = lock.Release() }()
		}
		switch {
		case ctx.Err() != nil:
			// Stopped while it waited: the history is the next server's,
			// whether this one got the lock or not.
			_ = listener.Close()
			return exitOK
		case errors.Is(err, serverlock.ErrUnsupported):
			// Servers ran there before they took the lock.
			lockless = err
		case err != nil:
			_ = listener.Close()
			return failToStart(err)
		}
	}
	// Opened only once the port and the history are ours, so that one
	// server at a time writes the log.
	logs, err := logfile.Open(filepath.Join(o.Paths.DataDir, logName), logMaxBytes, logKeep)
	if err != nil {
		_ = listener.Close()
		return fail(stdio, err)
	}
	defer func() { _ = logs.Close() }()

	log := slog.New(slog.NewTextHandler(logs, nil))
	if relaxed != nil {
		log.Warn("data directory check relaxed for the demo", "problem", relaxed)
	}
	if lockless != nil {
		log.Warn("the data directory's file system takes no lock: a server that starts as another stops "+
			"may read the history before that one has written all of it", "err", lockless)
	}
	if !o.Demo {
		// A demo serves a made-up machine and leaves the real one's files
		// alone.
		removeAsideAndLog(log, o.Paths.DataDir)
	}
	s := newInstance(ctx, o, cfg, port, token, log)
	return s.run(ctx, listener)
}

// acquireLock is serverlock.Acquire. Tests replace it: no file system at hand
// refuses locks.
var acquireLock = serverlock.Acquire

// lockHistory takes the lock that keeps the history to one server, waiting
// up to lockWait for the server before to let go of it, on o's timers.
func lockHistory(o ServeOptions) (*serverlock.Lock, error) {
	var after timer.AfterFunc
	if o.Ticker != nil {
		after = o.Ticker.AfterFunc
	}
	lock, err := acquireLock(o.Paths.ServerLock, lockWait, after)
	if errors.Is(err, serverlock.ErrHeld) {
		return nil, errHistoryHeld
	}
	return lock, err
}

// removeAsideAndLog removes the binaries earlier upgrades moved aside in
// dataDir that nothing runs any more, and logs which went. One that still
// runs, such as that of a previous server still shutting down, goes on a
// later start. What fails is a warning in the log: it never keeps the server
// from starting.
func removeAsideAndLog(log *slog.Logger, dataDir string) {
	removed, err := removeAside(dataDir)
	for _, path := range removed {
		log.Info("removed a binary an earlier upgrade replaced", "path", path)
	}
	if err != nil {
		log.Warn("could not remove the binaries earlier upgrades replaced", "err", err)
	}
}

// secureServedDataDir prepares the data directory as datadir.Secure does.
// A demo holds no real data, takes no events, installs no binary, and no
// hook runs anything from its data directory, so for a demo the check that
// nobody else can move the directory aside only reports, as relaxed, what it
// would refuse, for the log: the directory is still made private, with
// nothing in it but Loadline's own files and no link among them.
func secureServedDataDir(dir, trustFrom string, demoMode bool) (relaxed, err error) {
	if !demoMode {
		return nil, datadir.Secure(dir, trustFrom)
	}
	if err := datadir.Make(dir); err != nil {
		return nil, err
	}
	return datadir.Verify(dir, trustFrom), nil
}

// listenAddress is where the server listens: the IPv4 loopback address,
// written as a number, since a name resolves as the hosts file says.
func listenAddress(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// listenRefusal explains a listen that failed, or returns nil for a port
// that a loadline already serves, which is no failure: two sessions that
// start together each start a server, and the one that loses the race
// leaves quietly. So does one that finds a server that holds the port and
// the history's lock, but answers nothing, as it starts or stops. A demo is
// not one of them, and says why it cannot start.
func listenRefusal(ctx context.Context, port int, o ServeOptions, err error) error {
	switch {
	case isAccessDenied(err):
		return fmt.Errorf("port %d %s; pass --port to use another", port, accessDeniedReason)
	case !isAddrInUse(err):
		return fmt.Errorf("listen on port %d: %w", port, err)
	case o.Demo:
		return fmt.Errorf("port %d is in use (a Loadline server may be running); pass --port to run the demo elsewhere", port)
	case (forward.Client{BaseURL: forward.LoopbackURL(port)}).Healthy(ctx), historyHeld(o.Paths.ServerLock):
		return nil
	default:
		return fmt.Errorf("port %d is in use by another program", port)
	}
}

// historyHeld reports whether another server holds the history's lock at
// lockPath. Taken, the lock is let go of at once.
func historyHeld(lockPath string) bool {
	lock, err := acquireLock(lockPath, 0, nil)
	if err != nil {
		return errors.Is(err, serverlock.ErrHeld)
	}
	_ = lock.Release()
	return false
}

// instance is one running server and the work it runs beside its routes.
type instance struct {
	opts      ServeOptions
	port      int
	idleAfter time.Duration
	clock     func() time.Time
	after     timer.AfterFunc
	log       *slog.Logger
	failures  *failureLog
	opener    dashboardOpener
	// stopAsked is closed once a request has asked the server to stop.
	stopAsked chan struct{}

	tracker *tracker.Tracker
	srv     *server.Server
	spool   forward.Spool
	// configured holds the config dirs of the accounts the config names.
	// configured, dirs, poller and prober are nil in demo mode.
	configured []domain.ConfigDir
	dirs       *existingDirs
	poller     *poll.Poller
	prober     *accounts.Prober
	// history is nil in demo mode, and when its database cannot be opened.
	history *history
}

func newInstance(ctx context.Context, o ServeOptions, cfg config.Config, port int, token []byte, log *slog.Logger) *instance {
	clock := o.Clock
	if clock == nil {
		clock = time.Now
	}
	var after timer.AfterFunc
	if o.Ticker != nil {
		after = o.Ticker.AfterFunc
	}
	s := &instance{
		opts:      o,
		port:      port,
		idleAfter: time.Duration(cfg.IdleExitMinutes) * time.Minute,
		clock:     clock,
		after:     after,
		log:       log,
		failures:  newFailureLog(log, clock),
		opener:    newDashboardOpener(o, cfg),
		stopAsked: make(chan struct{}),
		spool:     forward.Spool{Dir: o.Paths.SpoolDir, MaxFiles: forward.DefaultMaxFiles},
	}

	trackerCfg := tracker.Config{
		Alerts: domain.AlertConfig{
			WeeklyPct:     cfg.Alerts.WeeklyPct,
			ContextPct:    cfg.Alerts.ContextPct,
			NeedsYouAfter: time.Duration(cfg.Alerts.NeedsYouSeconds) * time.Second,
		},
		Ticker: o.Ticker,
		Log:    log,
	}
	deps := server.Deps{
		Token: token, Port: port, Static: web.FS(), Version: Version, Clock: clock, Log: log, Demo: o.Demo,
		// A request repeated while the server stops changes nothing.
		Stop: sync.OnceFunc(func() { close(s.stopAsked) }),
	}
	if o.Demo {
		trackerCfg.Plans, trackerCfg.Budgets, trackerCfg.Credits = demo.Plans(), demo.Budgets(), demo.Credits(clock())
		s.tracker = tracker.New(clock, trackerCfg, tracker.NopRecorder{})
		seedDemo(s.tracker, clock())
		deps.State = s.tracker
		s.srv = server.New(deps)
		return s
	}

	defaultDir := config.ResolveConfigDir("", o.Home)
	settings := accountSettings(cfg.Accounts, o.Home)
	s.configured = settings.dirs
	startDirs := slices.Concat(settings.dirs, []domain.ConfigDir{defaultDir})
	trackerCfg.Labels, trackerCfg.Budgets, trackerCfg.Credits = settings.labels, settings.budgets, settings.credits
	trackerCfg.Plans = settings.plans
	// Credit estimates count no further back than the history keeps, which
	// is all a restart gives back.
	trackerCfg.CreditDays = cfg.RetentionDays
	s.history = s.openHistory(ctx, cfg.RetentionDays)
	s.tracker = tracker.New(clock, trackerCfg, s.history.recorder())

	runner := o.Runner
	if runner == nil {
		runner = claudeRunner(cfg, o.Home)
	}
	client := claudecli.Client{R: runner}
	s.dirs = newExistingDirs(s.failures)
	s.poller = &poll.Poller{
		List:      s.listSessions(client),
		Sink:      pollSink{Tracker: s.tracker, start: startDirs, dirs: s.dirs},
		Fast:      poll.DefaultFast,
		Slow:      poll.DefaultSlow,
		Debounce:  poll.DefaultDebounce,
		Clock:     clock,
		AfterFunc: after,
	}
	s.prober = &accounts.Prober{
		Status:    s.probeAccount(client),
		Set:       s.tracker.SetAccount,
		Labels:    settings.labels,
		Every:     accounts.DefaultEvery,
		Clock:     clock,
		AfterFunc: after,
	}
	for _, dir := range startDirs {
		s.prober.Observe(dir)
	}

	deps.State, deps.Kick, deps.OnDir = s.tracker, s.poller.Kick, s.noteDir
	deps.HoldIngest = true
	s.srv = server.New(deps)
	s.poller.OnPoll = s.srv.NotePoll
	return s
}

// claudeRunner runs the claude binary the config names. Claude Code's
// default config dir comes from the home directory alone: the server may
// have been started by another account's hook, and inherited that
// account's CLAUDE_CONFIG_DIR, which polling the default dir must clear.
func claudeRunner(cfg config.Config, home string) claudecli.Exec {
	return claudecli.Exec{Path: cfg.ClaudePath, DefaultDir: config.ResolveConfigDir("", home)}
}

// configuredAccounts is what the config says of its accounts: the config dir
// of each, and the labels, plans, daily budgets and usage credits it gives
// them, keyed by those dirs.
type configuredAccounts struct {
	dirs    []domain.ConfigDir
	labels  map[domain.ConfigDir]string
	plans   map[domain.ConfigDir]string
	budgets map[domain.ConfigDir]float64
	credits map[domain.ConfigDir][]domain.CreditGrant
}

func accountSettings(settings []config.AccountSetting, home string) configuredAccounts {
	c := configuredAccounts{
		labels:  make(map[domain.ConfigDir]string),
		plans:   make(map[domain.ConfigDir]string),
		budgets: make(map[domain.ConfigDir]float64),
		credits: make(map[domain.ConfigDir][]domain.CreditGrant),
	}
	for _, account := range settings {
		dir := account.Dir(home)
		c.dirs = append(c.dirs, dir)
		if account.Label != "" {
			c.labels[dir] = account.Label
		}
		if account.Plan != "" {
			c.plans[dir] = account.Plan
		}
		if account.DailyBudgetUSD > 0 {
			c.budgets[dir] = account.DailyBudgetUSD
		}
		for _, grant := range account.Credits {
			c.credits[dir] = append(c.credits[dir], domain.CreditGrant{USD: grant.USD, AddedAt: grant.AddedAt})
		}
	}
	return c
}

// seedDemo fills the tracker with the demo scenario as of now.
func seedDemo(t *tracker.Tracker, now time.Time) {
	accounts, events, requests := demo.Scenario(now)
	for _, a := range accounts {
		t.SetAccount(a)
	}
	for _, e := range events {
		t.Apply(e)
	}
	for _, r := range requests {
		t.RecordAPI(r)
	}
}

// noteDir notes the config dir an event named: a dir dropped for being
// missing is polled again, and a new one is probed.
func (s *instance) noteDir(dir domain.ConfigDir) {
	s.dirs.named(dir)
	s.prober.Observe(dir)
}

func (s *instance) listSessions(client claudecli.Client) func(context.Context, domain.ConfigDir, time.Time) ([]domain.Poll, error) {
	return func(ctx context.Context, dir domain.ConfigDir, at time.Time) ([]domain.Poll, error) {
		if !s.dirs.exists(dir) {
			// claude would list no session there, once it had created the
			// dir. With none of its sessions live, the dir is neither polled
			// nor probed again until an event names it.
			if !s.tracker.HasLiveIn(dir) {
				s.dirs.drop(dir)
				s.prober.Forget(dir)
			}
			return nil, nil
		}
		polls, err := client.Agents(ctx, dir, at)
		if err != nil && ctx.Err() == nil {
			s.failures.note("poll failed", string(dir), err)
		}
		return polls, err
	}
}

func (s *instance) probeAccount(client claudecli.Client) func(context.Context, domain.ConfigDir) (claudecli.AuthStatus, error) {
	return func(ctx context.Context, dir domain.ConfigDir) (claudecli.AuthStatus, error) {
		if !s.dirs.exists(dir) {
			return claudecli.AuthStatus{}, errConfigDirMissing
		}
		status, err := client.AuthStatus(ctx, dir)
		if err != nil && ctx.Err() == nil {
			s.failures.note("probe failed", string(dir), err)
		}
		return status, err
	}
}

// run serves on listener and runs the poller, prober, spool drain and idle
// watch beside it, until ctx ends, a request asks it to stop, the server has
// been idle long enough or the listener fails. Then it shuts everything
// down and waits for it.
func (s *instance) run(ctx context.Context, listener net.Listener) int {
	s.log.Info("loadline started", "version", Version, "port", s.port, "dataDir", s.opts.Paths.DataDir, "demo", s.opts.Demo)

	// Every request's context ends with the shutdown, so an open event
	// stream lets the shutdown finish instead of holding it to its timeout.
	requests, endRequests := context.WithCancel(context.WithoutCancel(ctx))
	defer endRequests()
	httpServer := newHTTPServer(requests, s.srv.Handler(), s.log)
	httpServer.RegisterOnShutdown(endRequests)
	served := make(chan error, 1)
	go func() { served <- httpServer.Serve(listener) }()
	// Restart recovery begins with what the history kept. It waits until
	// the server is serving, so that a loadline that lost the race for the
	// port finds this one healthy, and ingest waits for it: an event applied
	// first would replace the session it restores. The hooks spool what is
	// refused meanwhile, for the drain below.
	s.history.restore(ctx, s.tracker)
	// The configured accounts are listed from the start, not from the first
	// poll of their dirs, which a missing dir or a failing claude may never
	// bring. Added after the restore, which keeps nothing of an account the
	// tracker already knows.
	s.tracker.AddAccounts(s.configured)
	s.srv.OpenIngest()
	if s.opts.Ready != nil {
		s.opts.Ready(listener.Addr().String())
	}

	work, stopWork := context.WithCancel(ctx)
	defer stopWork()
	var workers sync.WaitGroup
	idle := make(chan struct{})
	if !s.opts.Demo {
		// Restart recovery: what the hooks spooled while no server ran goes
		// in before the first poll, which would otherwise find those
		// sessions missing, and which confirms what it applied.
		s.drain(work)
		workers.Go(func() { s.poller.Run(work) })
		workers.Go(func() { s.prober.Run(work) })
		workers.Go(func() { s.drainEvery(work) })
		workers.Go(func() { s.history.pruneEvery(work) })
	}
	workers.Go(func() { s.openDashboard(work) })
	if s.idleAfter > 0 {
		workers.Go(func() {
			if waitIdle(work, s.tracker.HasLive, s.idleAfter, s.clock, s.after) {
				close(idle)
			}
		})
	}

	code := exitOK
	var serveErr error
	select {
	case <-ctx.Done():
		s.log.Info("loadline stopping", "reason", "stop requested")
	case <-s.stopAsked:
		// The graceful shutdown below waits for the stop request's own
		// answer before the tracker and the history close.
		s.log.Info("loadline stopping", "reason", "asked to stop")
	case <-idle:
		s.log.Info("loadline stopping", "reason", "idle", "idleFor", s.idleAfter)
	case serveErr = <-served:
		s.log.Error("loadline stopping", "reason", "serving failed", "err", serveErr)
		code = exitFailure
	}

	stopWork()
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdown); err != nil {
		s.log.Warn("graceful shutdown timed out", "err", err)
		_ = httpServer.Close()
	}
	if serveErr == nil {
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("serving failed", "err", err)
		}
	}
	workers.Wait()
	s.tracker.Close()
	if s.opts.BeforeHistoryClose != nil {
		s.opts.BeforeHistoryClose()
	}
	s.history.close(shutdown)
	s.log.Info("loadline stopped")
	return code
}

// newHTTPServer returns the server for handler. Its limits bound what a
// slow or stalled client can hold; an event stream sets a write deadline of
// its own for each write. Every request's context derives from base.
func newHTTPServer(base context.Context, handler http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return base },
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// drain applies the hook events the spool holds and returns how many. An
// event that can never be applied is set aside, so that it cannot hold up
// the ones after it. Once ctx has ended the drain stops, and what is left
// waits for the next one.
func (s *instance) drain(ctx context.Context) int {
	undeliverable := 0
	drained, err := s.spool.Drain(func(env protocol.Envelope) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.srv.ApplySpooledHook(env); err != nil {
			undeliverable++
			return fmt.Errorf("%w: %w", forward.ErrUndeliverable, err)
		}
		return nil
	})
	if drained > 0 || undeliverable > 0 {
		s.log.Info("spool drained", "events", drained, "setAside", undeliverable)
	}
	if err != nil && ctx.Err() == nil {
		s.failures.note("spool drain failed", s.spool.Dir, err)
	}
	return drained
}

// drainEvery drains the spool every drainEvery until ctx ends, and asks for
// a poll to confirm what each drain applied: a hook that found the server
// busy or not yet listening spools its event.
func (s *instance) drainEvery(ctx context.Context) {
	for s.after.Sleep(ctx, drainEvery) {
		if s.drain(ctx) > 0 {
			s.poller.Kick()
		}
	}
}

// waitIdle reports true once no session has been live for idleAfter, as
// checks every idleCheckEvery see it, or false when ctx ends first. The
// span starts at the first check that finds no live session, so the server
// never leaves earlier than idleAfter after the last one it saw.
func waitIdle(ctx context.Context, live func() bool, idleAfter time.Duration, clock func() time.Time, after timer.AfterFunc) bool {
	var idleSince time.Time
	for {
		switch now := clock(); {
		case live():
			idleSince = time.Time{}
		case idleSince.IsZero():
			idleSince = now
		case now.Sub(idleSince) >= idleAfter:
			return true
		}
		if !after.Sleep(ctx, idleCheckEvery) {
			return false
		}
	}
}

// pollSink is the tracker as the poller sees it: the dirs of the accounts
// it knows, and the dirs known from the start, before any event named them,
// less those dropped for being missing.
type pollSink struct {
	*tracker.Tracker
	start []domain.ConfigDir
	dirs  *existingDirs
}

func (s pollSink) KnownDirs() []domain.ConfigDir {
	dirs := append(s.Tracker.KnownDirs(), s.start...)
	slices.Sort(dirs)
	return s.dirs.known(slices.Compact(dirs))
}
