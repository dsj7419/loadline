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

	// asReporter reports on itself and exits.
	asReporter = "report"
	// asSpawner starts a reporter with SpawnDetached, says so on stdout and
	// exits at once, as a hook does.
	asSpawner = "spawner"

	// holdLimit is the longest a reporter waits for its hold file, so that
	// none outlives a test that failed before releasing it.
	holdLimit = 30 * time.Second
)

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "":
		goleak.VerifyTestMain(m)
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
}

// expectChild arranges for the next copy of the test binary to report on
// itself, and, when hold is set, to wait until the test releases it. The
// test's end releases it and waits for it to exit.
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
	})
	return c
}

// read waits for the child's report.
func (c *child) read(t *testing.T) helperReport {
	t.Helper()
	data := readPublished(t, c.report, "the started program to report")
	var report helperReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode the report %s: %v", data, err)
	}
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

// waitExit waits until the child is about to exit, which it does at once
// once it has written that it is.
func (c *child) waitExit(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(c.report); errors.Is(err, fs.ErrNotExist) && t.Failed() {
		// It may never have started; there is nothing to wait for then.
		return
	}
	waitForFile(t, c.report+".exit", "the started program to exit")
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
