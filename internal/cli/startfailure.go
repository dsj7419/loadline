package cli

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/dsj7419/loadline/internal/datadir"
)

// startFailureMessage is the message of the log record that says why a
// server could not start.
const startFailureMessage = "loadline could not start"

// startFailed says why the server could not start, on stderr and in the log
// in dataDir, and returns 1. A server that a hook or a status line started
// has a stderr that goes nowhere, so the log is where to find out why no
// server answers.
func startFailed(stdio IO, dataDir, trustFrom string, cause error) int {
	logStartFailure(dataDir, trustFrom, cause)
	return fail(stdio, cause)
}

// logStartFailure appends one record of cause to the log in dataDir, as far
// as it can. It writes only into a data directory that is the user's alone,
// as datadir.Check finds it: one that is not there yet is left to a start
// that gets far enough to secure it, and one that Loadline refuses may hold
// a link, or someone else's file, where the log would go. It opens the log
// for this record alone, in append mode, creating it private, without the
// rotation of the running server's log; the record goes in one write, so it
// lands whole beside what a running server writes.
func logStartFailure(dataDir, trustFrom string, cause error) {
	if problem, err := datadir.Check(dataDir, trustFrom); err != nil || problem != "" {
		return
	}
	file, err := os.OpenFile(filepath.Clean(filepath.Join(dataDir, logName)), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	slog.New(slog.NewTextHandler(file, nil)).Error(startFailureMessage, "err", cause)
}
