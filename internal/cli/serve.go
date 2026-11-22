package cli

import (
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
	"github.com/dsj7419/loadline/internal/demo"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/logfile"
	"github.com/dsj7419/loadline/internal/poll"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/server"
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
	// drains, idle checks and the tracker's publishing. Nil means
	// wall-clock timers.
	Ticker tracker.Ticker
	// Ready, when set, is called with the address the server listens on
	// once it is serving.
	Ready func(addr string)
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

// Serve runs the server until ctx ends or no session has been live for the
// configured idle time, then shuts it down gracefully and returns 0. It
// returns 1 when it cannot start, and 0 without a word when another
// loadline already serves the port: binding the port is what keeps to one
// server per machine. What the running server has to say goes to its log
// file in the data directory, never to stdout or stderr.
func Serve(ctx context.Context, o ServeOptions, stdio IO) int {
	cfg, err := config.Load(o.Paths.ConfigFile, o.Home)
	if err != nil {
		return fail(stdio, err)
	}
	port := cfg.Port
	if o.Port != 0 {
		if err := config.ValidatePort(o.Port); err != nil {
			return fail(stdio, err)
		}
		port = o.Port
	}
	token, err := config.EnsureToken(o.Paths.TokenFile)
	if err != nil {
		return fail(stdio, err)
	}

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return refuseToListen(ctx, port, o.Demo, err, stdio)
	}
	// Opened only once the port is ours, so that one server at a time
	// writes the log.
	logs, err := logfile.Open(filepath.Join(o.Paths.DataDir, logName), logMaxBytes, logKeep)
	if err != nil {
		_ = listener.Close()
		return fail(stdio, err)
	}
	defer func() { _ = logs.Close() }()

	s := newInstance(o, cfg, port, token, slog.New(slog.NewTextHandler(logs, nil)))
	return s.run(ctx, listener)
}

// refuseToListen explains a listen that failed. A port that a loadline
// already serves is no failure: two sessions that start together each start
// a server, and the one that loses the race leaves quietly. A demo is not
// one of them, and says why it cannot start.
func refuseToListen(ctx context.Context, port int, demoMode bool, err error, stdio IO) int {
	switch {
	case isAccessDenied(err):
		_, _ = fmt.Fprintf(stdio.Err, "loadline: port %d %s; pass --port to use another\n", port, accessDeniedReason)
	case !isAddrInUse(err):
		return fail(stdio, fmt.Errorf("listen on port %d: %w", port, err))
	case demoMode:
		_, _ = fmt.Fprintf(stdio.Err,
			"loadline: port %d is in use (a Loadline server may be running); pass --port to run the demo elsewhere\n", port)
	case (forward.Client{BaseURL: forward.LoopbackURL(port)}).Healthy(ctx):
		return exitOK
	default:
		_, _ = fmt.Fprintf(stdio.Err, "loadline: port %d is in use by another program\n", port)
	}
	return exitFailure
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

	tracker *tracker.Tracker
	srv     *server.Server
	spool   forward.Spool
	// poller and prober are nil in demo mode.
	poller *poll.Poller
	prober *accounts.Prober
}

func newInstance(o ServeOptions, cfg config.Config, port int, token []byte, log *slog.Logger) *instance {
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
		spool:     forward.Spool{Dir: o.Paths.SpoolDir, MaxFiles: forward.DefaultMaxFiles},
	}

	trackerCfg := tracker.Config{
		Alerts: domain.AlertConfig{
			WeeklyPct:     cfg.Alerts.WeeklyPct,
			ContextPct:    cfg.Alerts.ContextPct,
			NeedsYouAfter: time.Duration(cfg.Alerts.NeedsYouSeconds) * time.Second,
		},
		Ticker: o.Ticker,
	}
	deps := server.Deps{Token: token, Port: port, Static: web.FS(), Version: Version, Clock: clock, Log: log, Demo: o.Demo}
	if o.Demo {
		trackerCfg.Budgets = demo.Budgets()
		s.tracker = tracker.New(clock, trackerCfg, tracker.NopRecorder{})
		seedDemo(s.tracker, clock())
		deps.State = s.tracker
		s.srv = server.New(deps)
		return s
	}

	defaultDir := config.ResolveConfigDir("", o.Home)
	startDirs, labels, budgets := accountSettings(cfg.Accounts, o.Home)
	startDirs = append(startDirs, defaultDir)
	trackerCfg.Labels, trackerCfg.Budgets = labels, budgets
	s.tracker = tracker.New(clock, trackerCfg, tracker.NopRecorder{})

	runner := o.Runner
	if runner == nil {
		runner = claudeRunner(cfg, o.Home)
	}
	client := claudecli.Client{R: runner}
	s.poller = &poll.Poller{
		List:      s.listSessions(client),
		Sink:      pollSink{Tracker: s.tracker, start: startDirs},
		Fast:      poll.DefaultFast,
		Slow:      poll.DefaultSlow,
		Debounce:  poll.DefaultDebounce,
		Clock:     clock,
		AfterFunc: after,
	}
	s.prober = &accounts.Prober{
		Status:    s.probeAccount(client),
		Set:       s.tracker.SetAccount,
		Labels:    labels,
		Every:     accounts.DefaultEvery,
		Clock:     clock,
		AfterFunc: after,
	}
	for _, dir := range startDirs {
		s.prober.Observe(dir)
	}

	deps.State, deps.Kick, deps.OnDir = s.tracker, s.poller.Kick, s.prober.Observe
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

// accountSettings returns the config dir of each configured account, and
// the labels and daily budgets the config gives them, keyed by those dirs.
func accountSettings(settings []config.AccountSetting, home string) (
	dirs []domain.ConfigDir, labels map[domain.ConfigDir]string, budgets map[domain.ConfigDir]float64,
) {
	labels, budgets = make(map[domain.ConfigDir]string), make(map[domain.ConfigDir]float64)
	for _, account := range settings {
		dir := account.Dir(home)
		dirs = append(dirs, dir)
		if account.Label != "" {
			labels[dir] = account.Label
		}
		if account.DailyBudgetUSD > 0 {
			budgets[dir] = account.DailyBudgetUSD
		}
	}
	return dirs, labels, budgets
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

func (s *instance) listSessions(client claudecli.Client) func(context.Context, domain.ConfigDir, time.Time) ([]domain.Poll, error) {
	return func(ctx context.Context, dir domain.ConfigDir, at time.Time) ([]domain.Poll, error) {
		polls, err := client.Agents(ctx, dir, at)
		if err != nil && ctx.Err() == nil {
			s.failures.note("poll failed", string(dir), err)
		}
		return polls, err
	}
}

func (s *instance) probeAccount(client claudecli.Client) func(context.Context, domain.ConfigDir) (claudecli.AuthStatus, error) {
	return func(ctx context.Context, dir domain.ConfigDir) (claudecli.AuthStatus, error) {
		status, err := client.AuthStatus(ctx, dir)
		if err != nil && ctx.Err() == nil {
			s.failures.note("probe failed", string(dir), err)
		}
		return status, err
	}
}

// run serves on listener and runs the poller, prober, spool drain and idle
// watch beside it, until ctx ends, the server has been idle long enough or
// the listener fails. Then it shuts everything down and waits for it.
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
	}
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
// it knows, and the dirs known from the start, before any event named them.
type pollSink struct {
	*tracker.Tracker
	start []domain.ConfigDir
}

func (s pollSink) KnownDirs() []domain.ConfigDir {
	dirs := append(s.Tracker.KnownDirs(), s.start...)
	slices.Sort(dirs)
	return slices.Compact(dirs)
}
