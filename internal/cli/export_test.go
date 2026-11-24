package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/serverlock"
	"github.com/dsj7419/loadline/internal/timer"
)

// IdleCheckEvery is how often a server checks whether it has been idle long
// enough to exit.
const IdleCheckEvery = idleCheckEvery

// LockWait is how long a starting server waits for the one before it to
// close the history.
const LockWait = lockWait

// ShutdownTimedOutMessage is the message of the log record that says a
// server's graceful shutdown ran out of time.
const ShutdownTimedOutMessage = shutdownTimedOutMessage

// ShutdownTimeout bounds a server's graceful shutdown, before it writes and
// closes the history.
const ShutdownTimeout = shutdownTimeout

// TapSendTimeout returns the bound on the tap's post.
func TapSendTimeout() time.Duration { return tapSendTimeout }

// HookSendTimeout returns the bound on the hook's post.
func HookSendTimeout() time.Duration { return hookSendTimeout }

// SetTapSendTimeout replaces the bound on the tap's post for the rest of the
// test.
func SetTapSendTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()

	previous := tapSendTimeout
	tapSendTimeout = timeout
	t.Cleanup(func() { tapSendTimeout = previous })
}

// SetHookSendTimeout replaces the bound on the hook's post for the rest of
// the test.
func SetHookSendTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()

	previous := hookSendTimeout
	hookSendTimeout = timeout
	t.Cleanup(func() { hookSendTimeout = previous })
}

// TrustFoldersFrom makes install, serve and doctor take dir, with every
// folder above it, as folders only the user can move, and returns what
// undoes it: the check that nobody else can move the data directory aside
// then stops below dir. Tests work in temporary folders whose permissions
// they do not choose.
func TrustFoldersFrom(dir string) (restore func()) {
	previous := trustFrom
	trustFrom = dir
	return func() { trustFrom = previous }
}

// DoctorFallsBackOn makes the doctor fall back on cfg when it cannot load
// the config file, until what it returns undoes it. A sealed run falls back
// on a config that names a port of its own, never the defaults' one.
func DoctorFallsBackOn(cfg config.Config) (restore func()) {
	previous := doctorFallback
	doctorFallback = func() config.Config { return cfg }
	return func() { doctorFallback = previous }
}

// FailAsideHousekeeping makes finding and removing the binaries earlier
// upgrades moved aside fail with err, besides doing their work, for the rest
// of the test.
func FailAsideHousekeeping(t *testing.T, err error) {
	t.Helper()

	find, remove := asideBinaries, removeAside
	asideBinaries = func(dataDir string) ([]string, error) {
		found, findErr := find(dataDir)
		return found, errors.Join(findErr, err)
	}
	removeAside = func(dataDir string) ([]string, error) {
		removed, removeErr := remove(dataDir)
		return removed, errors.Join(removeErr, err)
	}
	t.Cleanup(func() { asideBinaries, removeAside = find, remove })
}

// ServerPollEvery spaces the checks of a server that is starting or
// stopping.
const ServerPollEvery = serverPollEvery

// SetStopWait replaces how long loadline stop waits for the server it asked
// to stop, for the rest of the test.
func SetStopWait(t *testing.T, wait time.Duration) {
	t.Helper()

	previous := stopWait
	stopWait = wait
	t.Cleanup(func() { stopWait = previous })
}

// stopKindNames name what can come of asking a server to stop.
var stopKindNames = map[stopKind]string{
	noServer:     "no server",
	stopped:      "stopped",
	cannotAsk:    "cannot ask",
	notOurs:      "not ours",
	stillRunning: "still running",
	unwatched:    "unwatched",
	failed:       "failed",
}

// StopServer asks the server client names to stop, as loadline stop does,
// waiting up to wait on after, and returns what came of it by name, with
// why it failed.
func StopServer(ctx context.Context, client forward.Client, after timer.AfterFunc, wait time.Duration) (string, error) {
	outcome := stopServer(ctx, client, after, wait)
	return stopKindNames[outcome.kind], outcome.err
}

// OldServerInstruction says how to end a server of goos on port that cannot
// be asked to stop, and then start the new one.
func OldServerInstruction(goos string, port int) string {
	return oldServerInstruction(goos, port)
}

// SetServerSpawn makes install start the server with spawn, for the rest of
// the test.
func SetServerSpawn(t *testing.T, spawn func(exe string, args ...string) error) {
	t.Helper()

	previous := spawnServer
	spawnServer = spawn
	t.Cleanup(func() { spawnServer = previous })
}

// SetElevated makes install take itself to run elevated, or not, for the
// rest of the test.
func SetElevated(t *testing.T, elevated bool) {
	t.Helper()

	previous := isElevated
	isElevated = func() bool { return elevated }
	t.Cleanup(func() { isElevated = previous })
}

// AfterAsideRemoval runs after each try install makes at removing the
// binaries earlier upgrades moved aside, for the rest of the test.
func AfterAsideRemoval(t *testing.T, after func()) {
	t.Helper()

	remove := removeAside
	removeAside = func(dataDir string) ([]string, error) {
		removed, err := remove(dataDir)
		after()
		return removed, err
	}
	t.Cleanup(func() { removeAside = remove })
}

// CannotWatchProcesses makes every process loadline stop and install wait
// for one that cannot be watched, as Windows refuses to let some be, for
// the rest of the test.
func CannotWatchProcesses(t *testing.T) {
	t.Helper()

	previous := watchProcess
	watchProcess = func(pid int) (*exitWatch, error) {
		return nil, fmt.Errorf("open process %d: %w", pid, errCannotWatch)
	}
	t.Cleanup(func() { watchProcess = previous })
}

// SetHandoverStartWait replaces how long install waits for the server it
// starts in a hand-over to answer, for the rest of the test.
func SetHandoverStartWait(t *testing.T, wait time.Duration) {
	t.Helper()

	previous := handoverStartWait
	handoverStartWait = wait
	t.Cleanup(func() { handoverStartWait = previous })
}

// HandoverStartWait is how long install waits for the server it starts in a
// hand-over to answer, unless a test has replaced it.
func HandoverStartWait() time.Duration { return handoverStartWait }

// ServerStartWait bounds the wait for a server that open started to answer.
const ServerStartWait = serverStartWait

// LocksUnsupported makes the data directory's file system take no lock, as
// some network shares do not, for the rest of the test.
func LocksUnsupported(t *testing.T) {
	t.Helper()

	previous := acquireLock
	acquireLock = func(path string, _ time.Duration, _ timer.AfterFunc) (*serverlock.Lock, error) {
		return nil, fmt.Errorf("lock %s: %w", path, serverlock.ErrUnsupported)
	}
	t.Cleanup(func() { acquireLock = previous })
}
