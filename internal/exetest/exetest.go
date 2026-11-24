// Package exetest runs copies of a test binary that stand in for a loadline
// binary while it runs, as a hook or the server runs the installed one.
// Windows will neither replace nor delete an executable that is running, and
// install and the server must cope with that.
//
// A package whose tests use Run calls Hold first thing in its TestMain, so
// that a copy Run starts waits instead of running the tests.
package exetest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// readyEnv names a file a copy creates once it runs. Its presence in the
// environment is what makes the test binary a copy.
const readyEnv = "LOADLINE_EXETEST_READY"

// startPatience bounds how long Run waits for a copy to start, which on a
// loaded machine, with an antivirus scanning the new executable, can take
// seconds.
const startPatience = 30 * time.Second

// Hold returns at once, unless Run started this process as a copy. Then it
// says the copy runs and waits until its test kills it.
func Hold() {
	ready := os.Getenv(readyEnv)
	if ready == "" {
		return
	}
	if err := os.WriteFile(ready, nil, 0o600); err != nil { //nolint:gosec // G703: ready is a path in the parent test's temp dir
		os.Exit(1)
	}
	time.Sleep(time.Hour)
	os.Exit(0)
}

// Run writes the test binary to path, creating its folder, starts the copy
// and waits until it runs. The test's end kills that process, the one Run
// started and no other, and waits for it to exit. Run returns the binary's
// bytes.
func Run(t *testing.T, path string) []byte {
	t.Helper()

	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("find the test binary: %v", err)
	}
	binary, err := os.ReadFile(testBinary) //nolint:gosec // G304: the running test binary
	if err != nil {
		t.Fatalf("read the test binary: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create the folder of %s: %v", path, err)
	}
	if err := os.WriteFile(path, binary, 0o700); err != nil { //nolint:gosec // G306: the copy must be executable
		t.Fatalf("write %s: %v", path, err)
	}

	ready := filepath.Join(t.TempDir(), "ready")
	process := exec.CommandContext(context.Background(), path, "-test.run=^$")
	process.Env = append(os.Environ(), readyEnv+"="+ready)
	if err := process.Start(); err != nil {
		t.Fatalf("start %s: %v", path, err)
	}
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
	})
	for deadline := time.Now().Add(startPatience); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(ready); err == nil {
			return binary
		}
		if time.Now().After(deadline) {
			t.Fatalf("the copy at %s did not start within %s; does its TestMain call exetest.Hold?", path, startPatience)
		}
	}
}
