package serverlock_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/dsj7419/loadline/internal/clocktest"
	"github.com/dsj7419/loadline/internal/serverlock"
)

// holdEnv, when set, makes the test binary a process that takes the lock at
// the path it names, says so by creating the file named by readyEnv, and
// waits until its test ends it.
const (
	holdEnv  = "LOADLINE_SERVERLOCK_HOLD"
	readyEnv = "LOADLINE_SERVERLOCK_READY"
)

// hangBound is how long a wait in these tests lasts before the run is taken
// to have hung. It decides nothing: each wait ends as soon as what it waits
// for holds.
const hangBound = 30 * time.Second

var t0 = time.Date(2026, time.November, 24, 12, 0, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	if path := os.Getenv(holdEnv); path != "" {
		os.Exit(hold(path, os.Getenv(readyEnv)))
	}
	goleak.VerifyTestMain(m)
}

// hold takes the lock at path, creates ready, and waits to be ended.
func hold(path, ready string) int {
	lock, err := serverlock.Acquire(path, 0, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(ready, nil, 0o600); err != nil { //nolint:gosec // G703: ready is a path in the parent test's temp dir
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	time.Sleep(time.Hour)
	// Held to the end: a lock nothing refers to may be closed, and let go,
	// when its file is collected.
	runtime.KeepAlive(lock)
	return 0
}

func lockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "data", "server.lock")
}

func acquire(t *testing.T, path string) *serverlock.Lock {
	t.Helper()
	lock, err := serverlock.Acquire(path, 0, nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	return lock
}

func release(t *testing.T, lock *serverlock.Lock) {
	t.Helper()
	if err := lock.Release(); err != nil {
		t.Errorf("Release: %v", err)
	}
}

// acquired is what an Acquire running on another goroutine returned.
type acquired struct {
	lock *serverlock.Lock
	err  error
}

func acquireAsync(path string, wait time.Duration, clock *clocktest.Clock) <-chan acquired {
	done := make(chan acquired, 1)
	go func() {
		lock, err := serverlock.Acquire(path, wait, clock.AfterFunc)
		done <- acquired{lock, err}
	}()
	return done
}

// waitArmed waits until a timer of d, with all of d still to run, is pending
// on clock: the waiting Acquire has armed it.
func waitArmed(t *testing.T, clock *clocktest.Clock, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(hangBound)
	for !slices.Contains(clock.Pending(), d) {
		if time.Now().After(deadline) {
			t.Fatalf("no %v timer armed within %v; pending %v", d, hangBound, clock.Pending())
		}
		time.Sleep(time.Millisecond)
	}
}

func assertStillWaiting(t *testing.T, done <-chan acquired) {
	t.Helper()
	select {
	case got := <-done:
		t.Fatalf("Acquire returned %v, %v while the lock was held, want it still waiting", got.lock, got.err)
	default:
	}
}

func receive(t *testing.T, done <-chan acquired) acquired {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(hangBound):
		t.Fatalf("Acquire did not return within %v", hangBound)
		return acquired{}
	}
}

// Two servers in one process stand in for two processes in the tests of
// the server, so the lock must keep two opens of the file apart wherever it
// is taken, not only two processes.
func TestAcquire_TwoOpensInOneProcessExcludeEachOther(t *testing.T) {
	path := lockPath(t)
	first := acquire(t, path)

	if second, err := serverlock.Acquire(path, 0, nil); !errors.Is(err, serverlock.ErrHeld) {
		t.Fatalf("a second Acquire while the first holds the lock = %v, %v; want ErrHeld", second, err)
	}
	release(t, first)

	release(t, acquire(t, path))
}

func TestAcquire_WaitsUntilTheHolderReleases(t *testing.T) {
	path := lockPath(t)
	holder := acquire(t, path)
	clock := clocktest.New(t0)

	done := acquireAsync(path, time.Minute, clock)
	for range 3 {
		waitArmed(t, clock, serverlock.RetryEvery)
		assertStillWaiting(t, done)
		clock.Advance(serverlock.RetryEvery)
	}
	waitArmed(t, clock, serverlock.RetryEvery)
	release(t, holder)
	clock.Advance(serverlock.RetryEvery)

	got := receive(t, done)
	if got.err != nil {
		t.Fatalf("Acquire = %v once the holder released, want the lock", got.err)
	}
	if _, err := serverlock.Acquire(path, 0, nil); !errors.Is(err, serverlock.ErrHeld) {
		t.Errorf("Acquire beside the waiter that got the lock = %v, want ErrHeld", err)
	}
	release(t, got.lock)
}

func TestAcquire_GivesUpWithErrHeldAfterWait(t *testing.T) {
	path := lockPath(t)
	holder := acquire(t, path)
	defer release(t, holder)
	clock := clocktest.New(t0)
	const wait = 10 * time.Second

	done := acquireAsync(path, wait, clock)
	waitArmed(t, clock, wait)
	clock.Advance(wait - time.Nanosecond)
	waitArmed(t, clock, serverlock.RetryEvery)
	assertStillWaiting(t, done)
	clock.Advance(time.Nanosecond)

	if got := receive(t, done); got.lock != nil || !errors.Is(got.err, serverlock.ErrHeld) {
		t.Errorf("Acquire once %v had passed = %v, %v; want ErrHeld", wait, got.lock, got.err)
	}
}

// A server that crashes, or is ended, leaves no lock behind for the next
// to wait out: the system lets go of a process's lock when it ends.
func TestAcquire_TheSystemReleasesTheLockOfAProcessThatEnds(t *testing.T) {
	path := lockPath(t)
	ready := filepath.Join(t.TempDir(), "ready")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	holder := exec.CommandContext(t.Context(), self, "-test.run=^$") //nolint:gosec // G204: the test binary itself
	holder.Env = append(os.Environ(), holdEnv+"="+path, readyEnv+"="+ready)
	if err := holder.Start(); err != nil {
		t.Fatalf("start the holder: %v", err)
	}
	ended := false
	end := func() {
		if !ended {
			ended = true
			_ = holder.Process.Kill()
			_ = holder.Wait()
		}
	}
	t.Cleanup(end)
	for deadline := time.Now().Add(hangBound); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the holder did not take the lock within %v", hangBound)
		}
	}
	if _, err := serverlock.Acquire(path, 0, nil); !errors.Is(err, serverlock.ErrHeld) {
		t.Fatalf("Acquire while another process holds the lock = %v, want ErrHeld", err)
	}

	end()

	lock, err := serverlock.Acquire(path, hangBound, nil)
	if err != nil {
		t.Fatalf("Acquire once the holder had ended = %v, want the lock", err)
	}
	release(t, lock)
}
