// Package exetest runs copies of a test binary that stand in for a loadline
// binary while it runs, as a hook or the server runs the installed one.
// Windows will neither replace nor delete an executable that is running, and
// install and the server must cope with that.
//
// A package whose tests use Run, Start or Serve calls Hold first thing in its
// TestMain, so that a copy they start waits instead of running the tests.
package exetest

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// readyEnv names a file a copy creates once it runs. Its presence in the
// environment is what makes the test binary a copy. listenEnv, when set,
// names the loopback port a copy listens on before it says it runs.
const (
	readyEnv  = "LOADLINE_EXETEST_READY"
	listenEnv = "LOADLINE_EXETEST_LISTEN"
)

// startPatience bounds how long Run waits for a copy to start, which on a
// loaded machine, with an antivirus scanning the new executable, can take
// seconds.
const startPatience = 30 * time.Second

// Hold returns at once, unless Run, Start or Serve started this process as
// a copy. Then it listens, when Serve asked it to, says the copy runs, and
// waits until its test kills it.
func Hold() {
	ready := os.Getenv(readyEnv)
	if ready == "" {
		return
	}
	var listener net.Listener
	if port := os.Getenv(listenEnv); port != "" {
		var err error
		if listener, err = (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port)); err != nil {
			fmt.Fprintf(os.Stderr, "listen on port %s: %v\n", port, err)
			os.Exit(1)
		}
	}
	if err := os.WriteFile(ready, nil, 0o600); err != nil { //nolint:gosec // G703: ready is a path in the parent test's temp dir
		fmt.Fprintf(os.Stderr, "say the copy runs: %v\n", err)
		os.Exit(1)
	}
	time.Sleep(time.Hour)
	// Held to the end: a listener nothing refers to may be closed when it
	// is collected.
	runtime.KeepAlive(listener)
	os.Exit(0)
}

// Run writes the test binary to path, creating its folder, starts the copy
// and waits until it runs. The test's end kills that process, the one Run
// started and no other, and waits for it to exit. Run returns the binary's
// bytes.
func Run(t *testing.T, path string) []byte {
	t.Helper()

	binary := writeCopy(t, path)
	start(t, path)
	return binary
}

// Serve writes the test binary to path, as Run does, and starts the copy
// listening on port of the loopback interface, as a server does, and
// returns it once it listens. The test's end ends it, unless the test has.
func Serve(t *testing.T, path string, port int) *Process {
	t.Helper()

	writeCopy(t, path)
	return start(t, path, listenEnv+"="+strconv.Itoa(port))
}

// writeCopy writes the test binary to path, creating its folder, and
// returns its bytes.
func writeCopy(t *testing.T, path string) []byte {
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
	return binary
}

// Process is a copy of the test binary that Start started. It waits until
// its test ends it.
type Process struct {
	cmd *exec.Cmd
	// exited is closed once the copy has exited, and waited for: err is
	// what Wait said, and stderr what the copy wrote there.
	exited chan struct{}
	err    error
	stderr bytes.Buffer
	ended  sync.Once
}

// Start starts the test binary itself as a copy that waits, as Run does,
// without writing it anywhere, and returns it once it runs. The test's end
// ends it, unless the test has.
func Start(t *testing.T) *Process {
	t.Helper()

	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("find the test binary: %v", err)
	}
	return start(t, testBinary)
}

// PID is the copy's process ID.
func (p *Process) PID() int { return p.cmd.Process.Pid }

// End kills the copy, the process Start started and no other, and waits for
// it to exit.
func (p *Process) End() {
	p.ended.Do(func() {
		_ = p.cmd.Process.Kill()
		<-p.exited
	})
}

// start starts the binary at path as a copy, with env added to the test's
// environment, and waits until it runs. The test's end ends it.
func start(t *testing.T, path string, env ...string) *Process {
	t.Helper()

	ready := filepath.Join(t.TempDir(), "ready")
	process := &Process{cmd: exec.CommandContext(context.Background(), path, "-test.run=^$"), exited: make(chan struct{})}
	process.cmd.Env = append(append(os.Environ(), env...), readyEnv+"="+ready)
	process.cmd.Stderr = &process.stderr
	if err := process.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", path, err)
	}
	go func() {
		process.err = process.cmd.Wait()
		close(process.exited)
	}()
	t.Cleanup(process.End)
	deadline := time.Now().Add(startPatience)
	for {
		if _, err := os.Stat(ready); err == nil {
			return process
		}
		select {
		case <-process.exited:
			t.Fatalf("the copy at %s exited before it ran (%v): %s", path, process.err, process.stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("the copy at %s did not start within %s; does its TestMain call exetest.Hold?", path, startPatience)
		}
	}
}
