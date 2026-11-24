package launch_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/dsj7419/loadline/internal/hermetictest"
	"github.com/dsj7419/loadline/internal/launch"
)

// The tests start this test binary again in place of a server or a browser.
// helperEnv tells the copy which part to play; it inherits the variable,
// as a spawned server inherits its spawner's environment.
const (
	helperEnv = "LOADLINE_LAUNCH_HELPER"
	// reportEnv names the file a reporter writes what it saw of itself to.
	reportEnv = "LOADLINE_LAUNCH_REPORT"
	// holdEnv, when set, names a file the reporter waits for before it
	// exits, so that a test can look at it while it runs.
	holdEnv = "LOADLINE_LAUNCH_HOLD"
	// lingerEnv, when set, has the reporter run on for lingerAfterExit once
	// it has written that it is exiting, as a race-enabled binary on a
	// loaded machine may take a while to exit.
	lingerEnv = "LOADLINE_LAUNCH_LINGER"

	// asReporter reports on itself and exits.
	asReporter = "report"
	// asSpawner starts a reporter with SpawnDetached, says so on stdout and
	// exits at once, as a hook does.
	asSpawner = "spawner"

	// holdLimit is the longest a reporter waits for its hold file, so that
	// none outlives a test that failed before releasing it.
	holdLimit = 30 * time.Second
	// lingerAfterExit is well past goleak's retry window of about 430 ms (20
	// retries, sleeping from 1 microsecond and doubling up to 100 ms), so
	// that a goroutine left waiting for a lingering child is still there for
	// goleak to find.
	lingerAfterExit = 2 * time.Second
)

// The tests' own run is sealed: claude on PATH is a stand-in that fails the
// run, and the home directory, in which the browser and the server start,
// is temporary.
func TestMain(m *testing.M) {
	hermetictest.StandIn()
	switch os.Getenv(helperEnv) {
	case "":
		sealed, err := hermetictest.Seal()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		goleak.VerifyTestMain(m, goleak.Cleanup(func(code int) { os.Exit(sealed.Finish(code)) }))
	case asReporter:
		os.Exit(runReporter())
	case asSpawner:
		os.Exit(runSpawner())
	default:
		os.Exit(2)
	}
}

// helperReport is what a reporter saw of itself.
type helperReport struct {
	PID  int
	Args []string
	Dir  string
	// Detached is true for a process with no console on Windows, and for
	// one that leads a session of its own on Unix.
	Detached bool
	// Hidden is true for a process told to start with its window hidden.
	Hidden bool
	// Created tells this process from a later one given the same ID, where
	// the system can tell.
	Created int64
}

func runReporter() int {
	path := os.Getenv(reportEnv)
	dir, err := os.Getwd()
	if err != nil {
		return 1
	}
	report := helperReport{PID: os.Getpid(), Args: os.Args[1:], Dir: dir}
	inspect(&report)
	if err := writeReport(path, report); err != nil {
		return 1
	}
	if hold := os.Getenv(holdEnv); hold != "" {
		for deadline := time.Now().Add(holdLimit); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(hold); err == nil { //nolint:gosec // G703: hold is a file in the parent test's temp dir
				break
			}
		}
	}
	if err := publish(path+".exit", nil); err != nil {
		return 1
	}
	if os.Getenv(lingerEnv) != "" {
		time.Sleep(lingerAfterExit)
	}
	return 0
}

func writeReport(path string, report helperReport) error {
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return publish(path, data)
}

// publish writes data to path whole or not at all: to a file beside it,
// closed, and then renamed into place. Whoever finds path finds all of it,
// written and closed.
func publish(path string, data []byte) error {
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil { //nolint:gosec // G703: path is a file in the parent test's temp dir
		return err
	}
	return os.Rename(path+".tmp", path) //nolint:gosec // G703: path is a file in the parent test's temp dir
}

// readPublished waits for a file that publish puts at path, and reads it.
// For a moment after the new name appears, the rename that put it there
// still holds the file, as a virus scanner may too, and Windows refuses to
// open it for reading; that refusal is waited out.
func readPublished(t *testing.T, path, what string) []byte {
	t.Helper()
	waitForFile(t, path, what)
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			return data
		}
		if !isSharingViolation(err) || time.Now().After(deadline) {
			t.Fatalf("read %s: %v", path, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runSpawner() int {
	exe, err := os.Executable()
	if err != nil {
		return 1
	}
	if err := os.Setenv(helperEnv, asReporter); err != nil {
		return 1
	}
	if err := launch.SpawnDetached(exe); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("spawned")
	return 0
}

// child is a reporter a test is about to start.
type child struct {
	report, hold string
	// reaped, once the test has opened the child as its browser through
	// OpenBrowser, is closed when the reaper has waited for the child.
	reaped <-chan struct{}
	// opened is set once the test has opened the child as its browser
	// through Open, which says nothing of its reaper.
	opened bool
	// reapWaited is set once waitReaped has run, so that the test's end
	// does not wait, and report a failure, a second time.
	reapWaited bool
	// pid is the child's process ID, once the test has read its report.
	pid int
}

// expectChild arranges for the next copy of the test binary to report on
// itself, and, when hold is set, to wait until the test releases it. The
// test's end releases it, waits for it to exit and, if it was opened as a
// browser, for the reaper to have waited for it.
func expectChild(t *testing.T, hold bool) *child {
	t.Helper()
	dir := t.TempDir()
	c := &child{report: filepath.Join(dir, "report.json")}
	t.Setenv(helperEnv, asReporter)
	t.Setenv(reportEnv, c.report)
	if hold {
		c.hold = filepath.Join(dir, "hold")
		t.Setenv(holdEnv, c.hold)
	}
	t.Cleanup(func() {
		c.release(t)
		c.waitExit(t)
		if !c.reapWaited {
			c.waitReaped(t)
		}
	})
	return c
}

// open opens url in b, which starts the child, with Open, and has the
// test's end wait for the child to be reaped.
func (c *child) open(url string, b launch.Browser) error {
	err := launch.Open(url, b)
	c.opened = err == nil
	return err
}

// openBrowser opens url in b, which starts the child, with OpenBrowser, and
// keeps what says when the child has been reaped.
func (c *child) openBrowser(url string, b launch.Browser) error {
	reaped, err := launch.OpenBrowser(url, b)
	c.reaped = reaped
	return err
}

// read waits for the child's report.
func (c *child) read(t *testing.T) helperReport {
	t.Helper()
	data := readPublished(t, c.report, "the started program to report")
	var report helperReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode the report %s: %v", data, err)
	}
	c.pid = report.PID
	return report
}

func (c *child) exited() bool {
	_, err := os.Stat(c.report + ".exit")
	return err == nil
}

func (c *child) release(t *testing.T) {
	t.Helper()
	if c.hold == "" {
		return
	}
	if err := publish(c.hold, nil); err != nil {
		t.Errorf("release the started program: %v", err)
	}
}

// waitExit waits until the child has written that it is about to exit. A
// race-enabled child can still take a second or more to do so.
func (c *child) waitExit(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(c.report); errors.Is(err, fs.ErrNotExist) && t.Failed() {
		// It may never have started; there is nothing to wait for then.
		return
	}
	waitForFile(t, c.report+".exit", "the started program to exit")
}

// waitReaped waits until the reaper of a child opened as a browser has
// waited for it, and reports whether it has. The child can run on for a
// while after it has written that it is exiting, and a test that ended
// first would leave the reaper behind, for goleak to find once the
// package's tests are done.
func (c *child) waitReaped(t *testing.T) bool {
	t.Helper()
	c.reapWaited = true
	limit := reapLimit()
	switch {
	case c.reaped != nil:
		select {
		case <-c.reaped:
		case <-time.After(limit):
			t.Errorf("%s still ran %v after it said it was exiting, so its reaper still waits for it", c.process(), limit)
			return false
		}
	case c.opened:
		// Open gives no word of its reaper, so the wait is for goleak to find
		// no goroutine left, which Find retries for a while on each call.
		// That holds only because this package's tests run one at a time:
		// Find cannot tell one test's goroutines from another's, so beside a
		// parallel test it would wait on goroutines that are not the reaper.
		deadline := time.Now().Add(limit)
		for err := goleak.Find(); err != nil; err = goleak.Find() {
			if time.Now().After(deadline) {
				t.Errorf("%s still ran %v after it said it was exiting, so Open's reaper still waits for it: %v", c.process(), limit, err)
				return false
			}
		}
	}
	return true
}

// process names the child's process, by its ID once its report has given
// it.
func (c *child) process() string {
	if c.pid == 0 {
		return "the started program, which never reported its process ID,"
	}
	return fmt.Sprintf("the started program, process %d,", c.pid)
}

// raceSlowdown is how much longer the race detector may make a test binary
// take to exit.
const raceSlowdown = 5

// reapLimit is how long a test's end waits for a browser it opened to exit
// and be reaped: well past lingerAfterExit, and raceSlowdown times longer
// under the race detector, which slows a test binary's exit.
func reapLimit() time.Duration {
	const limit = 10 * time.Second
	if raceEnabled {
		return raceSlowdown * limit
	}
	return limit
}

func waitForFile(t *testing.T, path, what string) {
	t.Helper()
	deadline := time.Now().Add(holdLimit + 5*time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("find the test binary: %v", err)
	}
	return exe
}

// assertInHome checks that the child runs in the user's home directory, so
// that it holds no project directory open.
func assertInHome(t *testing.T, report helperReport) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("find the home directory: %v", err)
	}
	got, err := os.Stat(report.Dir)
	if err != nil {
		t.Fatalf("stat %s: %v", report.Dir, err)
	}
	want, err := os.Stat(home)
	if err != nil {
		t.Fatalf("stat %s: %v", home, err)
	}
	if !os.SameFile(got, want) {
		t.Errorf("the started program runs in %s, want the home directory %s", report.Dir, home)
	}
}
