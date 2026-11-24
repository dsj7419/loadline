// Package serverlock keeps two Loadline servers from holding the history
// at once. A server stops answering well before it exits: it closes its
// listener first and writes and closes the history only at the end of its
// shutdown. A server started meanwhile, by an upgrade, a hook or a status
// line, could bind the port and read a history the old one has not finished
// writing, and SQLite takes no lock between the two that would stop it. Each
// server therefore holds an exclusive lock on a file in the data directory
// from before it opens the history until it has closed it.
//
// The lock is the system's, on an open file, so it goes with the process:
// a server that crashes or is ended leaves nothing behind for the next to
// wait out.
//
// The server that holds the lock records its port and process in the file,
// so that a server that cannot start, or install, can name it: it may serve
// another port than the one now configured.
package serverlock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/timer"
)

// ErrHeld is the error of an Acquire that found the lock held throughout
// its wait.
var ErrHeld = errors.New("another process holds the lock")

// ErrUnsupported is the error of an Acquire on a file system that takes no
// lock, as some network shares do not.
var ErrUnsupported = errors.New("the file system takes no lock")

// retryEvery spaces the tries for a lock that is held. A server that is
// shutting down lets go within seconds.
const retryEvery = 50 * time.Millisecond

// errLocked is how lock says that another open of the file holds the lock.
var errLocked = errors.New("locked")

// Lock is a lock that Acquire took. It is held until Release, or until the
// process ends.
type Lock struct {
	file *os.File
}

// Acquire takes the exclusive lock on the file at path, creating the file,
// and its folder, private to the user. Another open of the file, in this
// process or another, holds it off: Acquire tries again every 50 ms while
// the lock is held, until wait has passed, and then returns ErrHeld. The
// tries and the wait run on after.
func Acquire(path string, wait time.Duration, after timer.AfterFunc) (*Lock, error) {
	file, err := open(path)
	if err != nil {
		return nil, err
	}
	waited := make(chan struct{})
	stop := after.Start(wait, func() { close(waited) })
	defer stop()
	for {
		err := lock(file)
		if err == nil {
			return &Lock{file: file}, nil
		}
		if !errors.Is(err, errLocked) {
			_ = file.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if !pause(after, retryEvery, waited) {
			_ = file.Close()
			return nil, ErrHeld
		}
	}
}

// pause waits d on after, reporting false when waited closes first.
func pause(after timer.AfterFunc, d time.Duration, waited <-chan struct{}) bool {
	due := make(chan struct{})
	stop := after.Start(d, func() { close(due) })
	defer stop()
	select {
	case <-due:
		return true
	case <-waited:
		return false
	}
}

// Release clears the holder's record, lets go of the lock and closes its
// file, which stays in place for the next server.
func (l *Lock) Release() error {
	return errors.Join(l.write(nil), unlock(l.file), l.file.Close())
}

// Holder is what the server that holds the lock records of itself.
type Holder struct {
	// Port is the port it serves.
	Port int `json:"port"`
	// PID is its process.
	PID int `json:"pid"`
	// PortGiven says it was given its port, as serve --port gives it,
	// rather than taking the configured one. A record that does not say
	// names a server on its configured port.
	PortGiven bool `json:"portGiven,omitempty"`
}

// maxRecord is far more than a record takes.
const maxRecord = 256

// Record writes h into the lock's file, in place of what it held, for
// ReadHolder to read while the lock is held.
func (l *Lock) Record(h Holder) error {
	record, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("encode the holder of %s: %w", l.file.Name(), err)
	}
	return l.write(record)
}

// write replaces what the lock's file holds with record. One who reads the
// file meanwhile may find it empty or part written.
func (l *Lock) write(record []byte) error {
	if err := l.file.Truncate(0); err != nil {
		return fmt.Errorf("clear %s: %w", l.file.Name(), err)
	}
	if _, err := l.file.WriteAt(record, 0); err != nil {
		return fmt.Errorf("write %s: %w", l.file.Name(), err)
	}
	return nil
}

// ReadHolder returns the record the file at path holds, or false when it
// holds no whole one: the server that holds the lock has yet to write it,
// or came before servers recorded themselves, or the last holder cleared it
// with Release. A record can outlive its holder: one that ends without
// Release, as a server that crashes or is ended does, leaves its record
// behind once the system has let go of its lock. So the record names the
// server that holds the lock only while the lock is held, and a caller
// checks that it is before it reads.
func ReadHolder(path string) (Holder, bool) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return Holder{}, false
	}
	defer func() { _ = file.Close() }()
	record, err := io.ReadAll(io.LimitReader(file, maxRecord+1))
	if err != nil || len(record) > maxRecord {
		return Holder{}, false
	}
	var h Holder
	if err := json.Unmarshal(record, &h); err != nil || h.Port < 1 || h.Port > maxPort || h.PID < 1 {
		return Holder{}, false
	}
	return h, true
}

// maxPort is the highest TCP port.
const maxPort = 1<<16 - 1

// open opens the file at path, creating it and its folder private to the
// user. Whoever could open it could hold the lock, and keep every server
// from starting.
func open(path string) (*os.File, error) {
	if err := fsperm.MkdirAll(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("create the folder of %s: %w", path, err)
	}
	file, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("make %s private: %w", path, err)
	}
	return file, nil
}
