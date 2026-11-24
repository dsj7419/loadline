package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/timer"
)

const (
	stopPath = "/api/stop"
	// stopAskTimeout bounds the stop request itself.
	stopAskTimeout = 5 * time.Second
	// maxStopAnswer is far more than the server's answer, a small JSON
	// object.
	maxStopAnswer = 1 << 10
	// serverHealthTimeout bounds each check that a server answers, off the
	// hooks' hot path: a server busy shutting down or starting may take
	// longer than a hook can wait.
	serverHealthTimeout = 2 * time.Second
)

// errCannotWatch is why a process cannot be watched to see it exit.
var errCannotWatch = errors.New("the process cannot be watched")

// watchProcess is watchExit. Tests replace it: no process at hand refuses
// to be watched.
var watchProcess = watchExit

// stopWait bounds the wait for a server asked to stop to stop answering and
// exit: its shutdown takes up to shutdownTimeout, and writing the history
// after it as long again. Tests shorten it.
var stopWait = 2 * shutdownTimeout

// stopKind is what came of asking the server on a port to stop.
type stopKind int

const (
	// noServer: nothing answered as a Loadline server.
	noServer stopKind = iota
	// stopped: the server's process has exited.
	stopped
	// cannotAsk: the server has no stop route, as one from before 0.1.1.
	cannotAsk
	// notOurs: the server did not take this installation's token.
	notOurs
	// stillRunning: the server took the request, but its process ran on, or,
	// one that cannot be watched, answered on, past the wait.
	stillRunning
	// unwatched: the server stopped answering, but its process could not
	// be watched to see it exit.
	unwatched
	// failed: the request failed, or its answer named no process.
	failed
)

// stopOutcome is what came of asking the server on a port to stop.
type stopOutcome struct {
	kind stopKind
	// pid is the server's process, as its answer named it.
	pid int
	// answering, for stillRunning, says the server still answered.
	answering bool
	// err is why it failed, for failed.
	err error
}

// stopServer asks the server at client's address to stop, with client's
// token, and waits, up to wait on after, until the process its answer named
// has exited (awaitStop): a server stops answering well before it has
// written the history and let go of its binary. It goes by what the server
// answers, never by its version: one without the route refuses the request
// with 404 or 405, and one of another installation with 401.
func stopServer(ctx context.Context, client forward.Client, after timer.AfterFunc, wait time.Duration) stopOutcome {
	if !client.Healthy(ctx) {
		return stopOutcome{kind: noServer}
	}
	pid, err := askToStop(ctx, client)
	var refused *forward.StatusError
	switch {
	case errors.As(err, &refused) && (refused.Code == http.StatusNotFound || refused.Code == http.StatusMethodNotAllowed):
		return stopOutcome{kind: cannotAsk}
	case errors.As(err, &refused) && refused.Code == http.StatusUnauthorized:
		return stopOutcome{kind: notOurs}
	case err != nil:
		return stopOutcome{kind: failed, err: err}
	}
	return awaitStop(ctx, client, pid, after, wait)
}

// askToStop asks the server to stop, and returns the process its answer
// names.
func askToStop(ctx context.Context, client forward.Client) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, stopAskTimeout)
	defer cancel()
	body, err := client.Post(ctx, stopPath, maxStopAnswer)
	if err != nil {
		return 0, err
	}
	var answer struct {
		Stopping bool `json:"stopping"`
		PID      int  `json:"pid"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || !answer.Stopping || answer.PID <= 0 {
		return 0, errors.New("the server's answer does not name the process that is stopping")
	}
	return answer.PID, nil
}

// awaitStop waits, up to wait on after, until the process pid has exited:
// with it, the server has written the history and let go of its binary and
// its lock, and whatever answers on the port then is another server, as one
// a status line starts the moment the port is free. The process is watched
// from the start, while it surely runs, so that no other process that takes
// its ID later is taken for it. One the system will not let be watched is
// taken to run on, as one that may not be signaled is on Unix: once the
// server is silent, there is nothing more to learn of it.
func awaitStop(ctx context.Context, client forward.Client, pid int, after timer.AfterFunc, wait time.Duration) stopOutcome {
	process, err := watchProcess(pid)
	switch {
	case errors.Is(err, errCannotWatch):
		process = nil
	case err != nil:
		return stopOutcome{kind: failed, pid: pid, err: fmt.Errorf("watch the server's process %d: %w", pid, err)}
	default:
		defer process.close()
	}
	waiting, giveUp := context.WithCancel(ctx)
	defer giveUp()
	stop := after.Start(wait, giveUp)
	defer stop()
	answering := true
	for {
		if process != nil {
			exited, err := process.exited()
			if err != nil {
				return stopOutcome{kind: failed, pid: pid, err: fmt.Errorf("watch the server's process %d: %w", pid, err)}
			}
			if exited {
				return stopOutcome{kind: stopped, pid: pid}
			}
		}
		// Once silent, the server is not asked again: a server a hook starts
		// meanwhile may answer on the port.
		answering = answering && client.Healthy(ctx)
		switch {
		case !answering && process == nil:
			return stopOutcome{kind: unwatched, pid: pid}
		case after.Sleep(waiting, serverPollEvery):
			continue
		case ctx.Err() != nil:
			return stopOutcome{kind: failed, pid: pid, err: fmt.Errorf("wait for the server to stop: %w", ctx.Err())}
		}
		return stopOutcome{kind: stillRunning, pid: pid, answering: answering}
	}
}

// runStopCommand asks the server on the configured port, or on --port, to
// stop, and waits until it has. It exits 0 once the server has stopped or
// when none runs, and 1, saying what to do, otherwise.
func runStopCommand(ctx context.Context, args []string, stdio IO) int {
	flags := newFlagSet("stop", stdio)
	port := flags.Int("port", 0, "stop the server on `port` (default from the Loadline config)")
	if code, ok := parseFlags(flags, args); !ok {
		return code
	}
	if isSet(flags, "port") {
		if err := config.ValidatePort(*port); err != nil {
			_, _ = fmt.Fprintf(stdio.Err, "loadline stop: --port: %v\n", err)
			return exitUsage
		}
	}

	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stdio, err)
	}
	if *port == 0 {
		// Without a home directory, Load refuses only an account that names
		// its config dir from home, and says so.
		home, _ := os.UserHomeDir()
		cfg, err := config.Load(paths.ConfigFile, home)
		if err != nil {
			return fail(stdio, err)
		}
		*port = cfg.Port
	}
	// Read, never created: a server started later would take a token that
	// no hook holds. Without one, a server that answers is another
	// installation's.
	token, err := config.ReadToken(paths.TokenFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail(stdio, err)
	}

	client := forward.Client{BaseURL: forward.LoopbackURL(*port), Token: token, HealthTimeout: serverHealthTimeout}
	outcome := stopServer(ctx, client, nil, stopWait)
	switch outcome.kind {
	case stopped:
		return printStdout(stdio, fmt.Sprintf("Stopped the Loadline server on port %d. A running Claude Code session starts it again within 30 seconds.\n", *port))
	case noServer:
		return printStdout(stdio, fmt.Sprintf("No Loadline server is running on port %d.\n", *port))
	case cannotAsk:
		_, _ = io.WriteString(stdio.Err, oldServerInstruction(runtime.GOOS, *port))
		return exitFailure
	case notOurs:
		return fail(stdio, notOursError(*port))
	case stillRunning:
		return fail(stdio, fmt.Errorf("%w; run loadline stop again in a moment", stillRunningError(*port, outcome)))
	case unwatched:
		return fail(stdio, unwatchedError(*port, outcome.pid))
	default:
		return fail(stdio, fmt.Errorf("stop the server on port %d: %w; run loadline doctor to check the server", *port, outcome.err))
	}
}

// unwatchedError says that the server on port went silent, but that its
// process pid could not be watched.
func unwatchedError(port, pid int) error {
	return fmt.Errorf("the server on port %d stopped answering, but its process %d could not be watched to see it exit; "+
		"run loadline doctor to check the server", port, pid)
}

// notOursError says that the server on port refused this installation's
// token.
func notOursError(port int) error {
	return fmt.Errorf("the server on port %d did not take this installation's token: it belongs to another installation, and was left running", port)
}

// stillRunningError says what of the server on port outlasted the wait.
func stillRunningError(port int, outcome stopOutcome) error {
	if outcome.answering {
		return fmt.Errorf("the server on port %d was asked to stop, but still answers after %v", port, stopWait)
	}
	return fmt.Errorf("the server on port %d was asked to stop, but its process %d still runs after %v", port, outcome.pid, stopWait)
}

// oldServerInstruction says how to end, once, a server on port that cannot
// be asked to stop, as one from before 0.1.1 cannot, and then start the new
// one. Loadline never ends a process itself. Such a server may run from any
// folder: 0.1.0's open and serve start the binary the user ran, often the
// one in the download's folder, not the one install puts in bin. Each line
// says what it did.
//
// On Windows a server that a hook started has no console, and gets no
// signal, so the line ends the process. It goes by the port, which one
// process alone listens on, and by the process's name, so that it ends
// nothing that is not Loadline's: install has just had a Loadline server's
// answer from that port. Its name is the one it started with, whatever an
// upgrade renamed its binary to since. Elsewhere the line goes by the
// command line, and ends every loadline serve of the user's, as it says:
// not every Linux comes with a tool that tells which process listens on a
// port. It keeps to the user's own processes, which pkill run as root would
// not. pkill matches the whole command line, arguments and all, so the
// pattern asks for loadline serve at its start: run from PATH, from a
// relative path, or from an absolute one that may hold blanks. A command
// such as make -C ~/src/loadline serve is left alone; one run by an absolute
// path that names an absolute .../loadline serve later on is not, since
// pkill sees no boundary between arguments.
func oldServerInstruction(goos string, port int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The Loadline server on port %d is from an earlier version, which cannot be asked to stop.\n", port)
	if goos == "windows" {
		fmt.Fprintf(&b, "End it once, in PowerShell:\n"+
			"  $server = Get-NetTCPConnection -State Listen -LocalPort %d -ErrorAction SilentlyContinue | "+
			"ForEach-Object { Get-Process -Id $_.OwningProcess } | Where-Object { $_.ProcessName -like 'loadline*' }; "+
			"if ($server) { $server | Stop-Process -PassThru } else { 'Nothing was ended: no Loadline server listens on port %d.' }\n",
			port, port)
	} else {
		b.WriteString("End it once; this ends every loadline serve that runs as you, wherever it was started from:\n" +
			"  pkill -u \"$(id -u)\" -f '^([^ ]*/|/.*/)?loadline serve( |$)' && echo 'Ended the Loadline server.' || echo 'Nothing was ended: no loadline serve runs as you.'\n")
	}
	b.WriteString("Then start the new one:\n  loadline open\n")
	return b.String()
}
