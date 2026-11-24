package cli

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/timer"
)

// handoverStartWait bounds the wait for the server a hand-over starts to
// answer: it may first wait out the lock on the history, which the server
// before it lets go of last. Tests shorten it.
var handoverStartWait = lockWait + serverStartWait

const (
	// asideSettleBudget bounds how long install keeps trying to remove the
	// binaries earlier upgrades moved aside after a hand-over: the server
	// that ran one has exited, but a scanner may hold its binary a moment
	// longer.
	asideSettleBudget = 500 * time.Millisecond
	// maxAsideSettleDelay caps the pause between those tries.
	maxAsideSettleDelay = 50 * time.Millisecond
)

// handoverOptions is what a hand-over takes.
type handoverOptions struct {
	// port is the configured port, which the hooks start the server on.
	port int
	// token is the installation's.
	token []byte
	// exe is the binary install has just put in place, which the new
	// server runs.
	exe string
	// dataDir holds exe's folder.
	dataDir string
	// spawn starts the new server. Nil means launch.SpawnDetached.
	spawn func(exe string, args ...string) error
	// after schedules the waits. Nil means wall-clock timers.
	after timer.AfterFunc
	// elevated says install runs elevated, as from an administrator's
	// shell.
	elevated bool
}

// handOver hands the server that answers on the configured port over to the
// binary install has just put in place, which it is not running: until it
// exits, a server keeps the binary it started from, and while sessions keep
// it busy it never exits. It asks the server to stop, waits until its
// process is gone, and starts the new binary's server itself: only a
// session's start starts one, and OpenTelemetry posts sent in the gap would
// be lost. That server waits for the lock on the history, which covers the
// old one's last writes.
//
// Install elevated, it stops the old server but starts none: a server it
// started would run elevated for as long as it lived, and it runs the
// claude a config the user can write names. A server that cannot be asked,
// as one from 0.1.0 cannot, gets the line that ends it once; Loadline never
// ends a process itself. A server of another installation is left alone.
// What fails is returned, saying what to run: the settings and the binary
// are in place by then. stoppedOld reports that the old server stopped
// answering, so that the binary it ran is about to be let go of.
//
// A server whose process Windows will not let be watched counts as stopped
// once it is silent: the new server's wait for the lock on the history
// covers whatever it still writes.
func handOver(ctx context.Context, out *printer, o handoverOptions) (stoppedOld bool, err error) {
	client := forward.Client{BaseURL: forward.LoopbackURL(o.port), Token: o.token, HealthTimeout: serverHealthTimeout}
	outcome := stopServer(ctx, client, o.after, stopWait)
	switch outcome.kind {
	case noServer:
		return false, nil
	case cannotAsk:
		out.printf("%s", oldServerInstruction(runtime.GOOS, binDir(o.dataDir), o.port))
		return false, nil
	case notOurs:
		out.printf("Note: %v.\n", notOursError(o.port))
		return false, nil
	case stillRunning:
		return false, fmt.Errorf("%w; once it has stopped, run loadline open", stillRunningError(o.port, outcome))
	case failed:
		return false, fmt.Errorf("stop the server on port %d: %w; run loadline open", o.port, outcome.err)
	case stopped, unwatched:
	}
	if o.elevated {
		out.printf("Stopped the old server; run loadline open from a normal shell to start the new one.\n")
		return true, nil
	}
	start := serverStart{exe: o.exe, spawn: o.spawn, after: o.after, wait: handoverStartWait}
	if err := startServer(ctx, client, o.port, start); err != nil {
		return true, fmt.Errorf("the old server stopped, but the new one did not start: %w; run loadline open", err)
	}
	// The page has no reload of its own for a new version.
	out.printf("Restarted the Loadline server on %s; reload an open dashboard.\n", Version)
	return true, nil
}

// removeAsideSettlingAndSay is removeAsideAndSay after a hand-over: while
// one of the binaries earlier upgrades moved aside in dataDir is still there,
// held as the server that ran it lets go of it, it tries again, until
// asideSettleBudget is spent.
func removeAsideSettlingAndSay(out *printer, stdio IO, dataDir string) {
	deadline := time.Now().Add(asideSettleBudget)
	delay := time.Millisecond
	var removed []string
	for {
		gone, err := removeAside(dataDir)
		removed = append(removed, gone...)
		if err != nil || !time.Now().Add(delay).Before(deadline) || !asideLeft(dataDir) {
			sayAsideRemoved(out, dataDir, removed)
			warnAside(stdio, err)
			return
		}
		time.Sleep(delay)
		delay = min(2*delay, maxAsideSettleDelay)
	}
}

// asideLeft reports whether a binary an earlier upgrade moved aside is still
// in dataDir. One that cannot be looked for is not waited for: removing it
// failed alike, and said so.
func asideLeft(dataDir string) bool {
	left, err := asideBinaries(dataDir)
	return err == nil && len(left) > 0
}
