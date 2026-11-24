package serverlock_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
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

// heldPort is the port the holder process records.
const heldPort = 8289

// hold takes the lock at path, records itself as serving heldPort, creates
// ready, and waits to be ended.
func hold(path, ready string) int {
	lock, err := serverlock.Acquire(path, 0, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := lock.Record(serverlock.Holder{Port: heldPort, PID: os.Getpid()}); err != nil {
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

// holdUntilReleased takes the lock at path for the test and returns what
// lets go of it, once: the test calls it, or the test's cleanup does when
// the test stops before then, so that no open of the lock file outlives a
// failing run.
func holdUntilReleased(t *testing.T, path string) (letGo func()) {
	t.Helper()
	lock := acquire(t, path)
	var once sync.Once
	letGo = func() { once.Do(func() { release(t, lock) }) }
	t.Cleanup(letGo)
	return letGo
}

// endWait, as a test's cleanup, ends the Acquire behind done, which waits
// up to wait on clock, if a failing run left it waiting: once the wait has
// passed it gives up, or takes the lock, which is let go of here. An
// Acquire that has returned leaves no timer pending.
func endWait(t *testing.T, clock *clocktest.Clock, done <-chan acquired, wait time.Duration) {
	t.Helper()
	var got acquired
	if len(clock.Pending()) == 0 {
		select {
		case got = <-done:
		default:
		}
	} else {
		clock.Advance(wait)
		got = receive(t, done)
	}
	if got.lock != nil {
		release(t, got.lock)
	}
}

// A server waiting for another to finish shutting down tries again every
// 50 ms, as Acquire promises: soon enough to take over at once, and seldom
// enough not to spin on the lock meanwhile. It takes the lock once the
// holder lets go of it.
func TestAcquire_WaitsUntilTheHolderReleases(t *testing.T) {
	path := lockPath(t)
	letGo := holdUntilReleased(t, path)
	clock := clocktest.New(t0)
	const wait = time.Minute

	done := acquireAsync(path, wait, clock)
	t.Cleanup(func() { endWait(t, clock, done, wait) })
	for elapsed := time.Duration(0); elapsed < 150*time.Millisecond; elapsed += 50 * time.Millisecond {
		clock.WaitForTimers(t, 2)
		want := []time.Duration{50 * time.Millisecond, wait - elapsed}
		if got := clock.Pending(); !slices.Equal(got, want) {
			t.Fatalf("timers pending %v into the wait = %v, want the next try in 50ms beside the wait's end: %v", elapsed, got, want)
		}
		assertStillWaiting(t, done)
		clock.Advance(50 * time.Millisecond)
	}
	clock.WaitForTimers(t, 2)
	letGo()
	clock.Advance(50 * time.Millisecond)

	got := receive(t, done)
	if got.err != nil {
		t.Fatalf("Acquire = %v once the holder released, want the lock", got.err)
	}
	if beside, err := serverlock.Acquire(path, 0, nil); !errors.Is(err, serverlock.ErrHeld) {
		t.Errorf("Acquire beside the waiter that got the lock = %v, want ErrHeld", err)
		if beside != nil {
			release(t, beside)
		}
	}
	release(t, got.lock)
}

func TestAcquire_GivesUpWithErrHeldAfterWait(t *testing.T) {
	path := lockPath(t)
	holdUntilReleased(t, path)
	clock := clocktest.New(t0)
	const wait = 10 * time.Second

	done := acquireAsync(path, wait, clock)
	t.Cleanup(func() { endWait(t, clock, done, wait) })
	waitArmed(t, clock, wait)
	clock.Advance(wait - time.Nanosecond)
	waitArmed(t, clock, serverlock.RetryEvery)
	assertStillWaiting(t, done)
	clock.Advance(time.Nanosecond)

	got := receive(t, done)
	if got.lock != nil {
		release(t, got.lock)
	}
	if got.lock != nil || !errors.Is(got.err, serverlock.ErrHeld) {
		t.Errorf("Acquire once %v had passed = %v, %v; want ErrHeld", wait, got.lock, got.err)
	}
}

// startHolder starts a process that takes the lock at path and records
// itself, and returns once it has. The test's end ends it, as end does
// sooner.
func startHolder(t *testing.T, path string) (holder *exec.Cmd, end func()) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	holder = exec.CommandContext(t.Context(), self, "-test.run=^$") //nolint:gosec // G204: the test binary itself
	holder.Env = append(os.Environ(), holdEnv+"="+path, readyEnv+"="+ready)
	if err := holder.Start(); err != nil {
		t.Fatalf("start the holder: %v", err)
	}
	ended := false
	end = func() {
		if !ended {
			ended = true
			_ = holder.Process.Kill()
			_ = holder.Wait()
		}
	}
	t.Cleanup(end)
	for deadline := time.Now().Add(hangBound); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(ready); err == nil {
			return holder, end
		}
		if time.Now().After(deadline) {
			t.Fatalf("the holder did not take the lock within %v", hangBound)
		}
	}
}

// A server that crashes, or is ended, leaves no lock behind for the next
// to wait out: the system lets go of a process's lock when it ends.
func TestAcquire_TheSystemReleasesTheLockOfAProcessThatEnds(t *testing.T) {
	path := lockPath(t)
	_, end := startHolder(t, path)
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

// A server that cannot start names the one that holds the lock, as that
// one recorded itself in the lock's file. The record must be readable while
// the lock is held, from another process, which is when it is read.
func TestReadHolder_ReadsTheRecordOfAHolderInAnotherProcess(t *testing.T) {
	path := lockPath(t)
	holder, _ := startHolder(t, path)

	got, ok := serverlock.ReadHolder(path)

	if want := (serverlock.Holder{Port: heldPort, PID: holder.Process.Pid}); !ok || got != want {
		t.Errorf("ReadHolder while another process holds the lock = %+v, %v; want %+v", got, ok, want)
	}
}

// Each record replaces what the file held, and reads back whole while the
// lock is held.
func TestLock_RecordReplacesWhatTheFileHeld(t *testing.T) {
	path := lockPath(t)
	lock := acquire(t, path)
	defer release(t, lock)
	for _, want := range []serverlock.Holder{{Port: 61234, PID: 42, PortGiven: true}, {Port: 1024, PID: 7}} {
		if err := lock.Record(want); err != nil {
			t.Fatalf("Record(%+v): %v", want, err)
		}

		if got, ok := serverlock.ReadHolder(path); !ok || got != want {
			t.Errorf("ReadHolder after Record(%+v) = %+v, %v", want, got, ok)
		}
	}
}

// A record written before holders said whether they were given their port
// names one that serves the port it was configured with.
func TestReadHolder_ARecordThatDoesNotSayReadsAsTheConfiguredPort(t *testing.T) {
	path := lockPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"port":8289,"pid":4242}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, ok := serverlock.ReadHolder(path); !ok || got != (serverlock.Holder{Port: 8289, PID: 4242}) {
		t.Errorf("ReadHolder = %+v, %v; want port 8289, process 4242, not given its port", got, ok)
	}
}

// Once let go of, the lock's file names no holder: a record left there
// would name a server that is gone.
func TestLock_ReleaseClearsTheRecord(t *testing.T) {
	path := lockPath(t)
	lock := acquire(t, path)
	if err := lock.Record(serverlock.Holder{Port: 61234, PID: 42}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	release(t, lock)

	if got, ok := serverlock.ReadHolder(path); ok {
		t.Errorf("ReadHolder once the lock was let go of = %+v, want no holder", got)
	}
}

// The file is read while its holder may be writing it, and a server from
// before holders recorded themselves leaves it empty: whatever it holds
// that is not a whole record names no holder.
func TestReadHolder_NamesNoHolderFromAFileWithoutARecord(t *testing.T) {
	for name, contents := range map[string]string{
		"an empty file":            "",
		"a record half written":    `{"port":8289,"pi`,
		"no port":                  `{"pid":4242}`,
		"no process":               `{"port":8289}`,
		"a port out of range":      `{"port":65536,"pid":4242}`,
		"a port of zero":           `{"port":0,"pid":4242}`,
		"a process below one":      `{"port":8289,"pid":-1}`,
		"a port that is no number": `{"port":"8289","pid":4242}`,
		"words":                    "8289 4242",
		"more after the record":    `{"port":8289,"pid":4242}{"port":8290}`,
		"a record far too long":    `{"port":8289,"pid":4242,"pad":"` + strings.Repeat("x", 4096) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := lockPath(t)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}

			if got, ok := serverlock.ReadHolder(path); ok {
				t.Errorf("ReadHolder of %q = %+v, want no holder", contents, got)
			}
		})
	}

	t.Run("no file", func(t *testing.T) {
		if got, ok := serverlock.ReadHolder(lockPath(t)); ok {
			t.Errorf("ReadHolder with no file = %+v, want no holder", got)
		}
	})
}
