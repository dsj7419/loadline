package cli

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/serverlock"
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
	// lockPath is the history's lock, whose holder records the port it
	// serves.
	lockPath string
	// replaced says install has just replaced exe, which a server already
	// running does not run.
	replaced bool
	// spawn starts the new server. Nil means launch.SpawnDetached.
	spawn func(exe string, args ...string) error
	// after schedules the waits. Nil means wall-clock timers.
	after timer.AfterFunc
	// elevated says install runs elevated, as from an administrator's
	// shell.
	elevated bool
}

// handOver hands the running server over to the server the hooks start: one
// that runs the binary install has just put in place, on the configured
// port. Until it exits, a server keeps the binary it started from and the
// port it listens on, and while sessions keep it busy it never exits. So
// handOver asks the server that holds the history to stop when it was
// started on its configured port and that is no longer the configured one,
// as after the port changed in the config, since no server can start beside
// it; failing that, once install has replaced the binary, it asks the
// server on the configured port. A server the user started on a port of
// their choosing, with serve --port, often because the configured one
// cannot take a server, is left where it is, with the line that stops it:
// install cannot tell whether a server can start on the configured port. It
// waits until that server's process is gone, and starts the new one itself:
// only a session's start starts one, and OpenTelemetry posts sent in the gap
// would be lost. That server waits for the lock on the history, which covers
// the old one's last writes.
//
// Install elevated, it stops the old server but starts none: a server it
// started would run elevated for as long as it lived, and it runs the
// claude a config the user can write names. A server that cannot be asked,
// as one from 0.1.0 cannot, gets the line that ends it once; Loadline never
// ends a process itself. A server that does not take the installation's
// token is left alone. What fails is returned, saying what to run: the
// settings and the binary are in place by then. stoppedOld reports that the
// old server stopped answering, so that the binary it ran is about to be
// let go of.
//
// A server whose process Windows will not let be watched counts as stopped
// once it is silent: the new server's wait for the lock on the history
// covers whatever it still writes.
func handOver(ctx context.Context, out *printer, o handoverOptions) (stoppedOld bool, err error) {
	from := o.port
	holder, elsewhere := holderElsewhere(o.lockPath, o.port)
	switch {
	case elsewhere && holder.PortGiven:
		out.printf("Note: the Loadline server on port %d was started with serve --port, and was left running; "+
			"while it runs, no server starts on the configured port %d. Stop it with loadline stop --port %d to let one start there.\n",
			holder.Port, o.port, holder.Port)
	case elsewhere:
		if stoppedOld, err = stopToHandOver(ctx, out, o, holder.Port); err != nil {
			return false, err
		}
		if stoppedOld {
			from = holder.Port
		}
	}
	if !stoppedOld && o.replaced {
		if stoppedOld, err = stopToHandOver(ctx, out, o, o.port); err != nil {
			return false, err
		}
	}
	if !stoppedOld {
		return false, nil
	}
	if o.elevated {
		out.printf("Stopped the old server; run loadline open from a normal shell to start the new one.\n")
		return true, nil
	}
	client := forward.Client{BaseURL: forward.LoopbackURL(o.port), Token: o.token, HealthTimeout: serverHealthTimeout}
	// A session's hook or status line may have started the next server the
	// moment the port was free, and one started now would find it taken.
	version, answers := client.ServerVersion(ctx)
	switch {
	case !answers:
		start := serverStart{exe: o.exe, spawn: o.spawn, after: o.after, wait: handoverStartWait}
		if err := startServer(ctx, client, o.port, start); err != nil {
			return true, fmt.Errorf("the old server stopped, but the new one did not start: %w; run loadline open", err)
		}
	case version != Version:
		out.printf("Stopped the old server, but the server that now answers on port %d runs %s, not %s; "+
			"run loadline stop, and a running session starts the new one.\n", o.port, version, Version)
		return true, nil
	}
	if from != o.port {
		out.printf("Moved the Loadline server from port %d to port %d; run loadline open to see its dashboard.\n", from, o.port)
		return true, nil
	}
	// The page has no reload of its own for a new version.
	out.printf("Restarted the Loadline server on %s; reload an open dashboard.\n", Version)
	return true, nil
}

// holderElsewhere returns what the server holding the history's lock at
// lockPath recorded of itself, when it serves another port than port.
func holderElsewhere(lockPath string, port int) (serverlock.Holder, bool) {
	if !historyHeld(lockPath) {
		return serverlock.Holder{}, false
	}
	holder, known := serverlock.ReadHolder(lockPath)
	if !known || holder.Port == port {
		return serverlock.Holder{}, false
	}
	return holder, true
}

// stopToHandOver asks the server on port to stop, with the installation's
// token, and reports whether it stopped. What install can do about one that
// cannot be asked, or does not take the token, it prints; what it cannot,
// it returns, saying what to run.
func stopToHandOver(ctx context.Context, out *printer, o handoverOptions, port int) (bool, error) {
	client := forward.Client{BaseURL: forward.LoopbackURL(port), Token: o.token, HealthTimeout: serverHealthTimeout}
	outcome := stopServer(ctx, client, o.after, stopWait)
	switch outcome.kind {
	case noServer:
		return false, nil
	case cannotAsk:
		out.printf("%s", oldServerInstruction(runtime.GOOS, port))
		return false, nil
	case notOurs:
		out.printf("Note: %v.\n", notOursError(port))
		return false, nil
	case stillRunning:
		return false, fmt.Errorf("%w; once it has stopped, run loadline open", stillRunningError(port, outcome))
	case failed:
		return false, fmt.Errorf("stop the server on port %d: %w; run loadline open", port, outcome.err)
	default:
		return true, nil
	}
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
