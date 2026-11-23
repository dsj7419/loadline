package install_test

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"runtime/metrics"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
)

const (
	// helperEnv makes the test binary a helper instead of running the
	// tests: "sleep" sleeps until it is killed, so a test can hold a copy of
	// it running, and "loadline" runs it as loadline's tap.
	helperEnv = "LOADLINE_INSTALL_HELPER"
	// helperReadyEnv names a file the sleeping helper creates once it runs,
	// so a test knows the process has started.
	helperReadyEnv = "LOADLINE_INSTALL_HELPER_READY"
)

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "sleep":
		if ready := os.Getenv(helperReadyEnv); ready != "" {
			if err := os.WriteFile(ready, nil, 0o600); err != nil { //nolint:gosec // G703: ready is a path in the parent test's temp dir
				os.Exit(1)
			}
		}
		time.Sleep(time.Hour)
		os.Exit(0)
	case "loadline":
		// Anything but the tap, such as the server a tap may start, exits at
		// once.
		if len(os.Args) > 1 && os.Args[1] == "tap" {
			os.Exit(cli.Run(context.Background(), os.Args[1:], cli.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}))
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// within runs f and stops the whole test binary if f has not returned by the
// deadline, or if the heap grows by more than maxHeapGrowth meanwhile. A loop
// that never ended would otherwise hold the suite until its timeout, or fill
// memory first if it allocates as it goes, and a failing test cannot stop the
// goroutine running it. A panic in f is raised again here, on the caller's
// goroutine, so that the testing package names the test it failed. f must
// not call t's methods.
func within(deadline time.Duration, what string, f func()) {
	heap := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(heap)
	baseline := heap[0].Value.Uint64()

	done := make(chan string, 1)
	go func() {
		failure := ""
		defer func() { done <- failure }()
		defer func() {
			if r := recover(); r != nil {
				failure = fmt.Sprintf("%s panicked: %v\n%s", what, r, debug.Stack())
			}
		}()
		f()
	}()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	timeout := time.After(deadline)
	for {
		select {
		case failure := <-done:
			if failure != "" {
				panic(failure)
			}
			return
		case <-timeout:
			panic(fmt.Sprintf("%s did not return within %s", what, deadline))
		case <-tick.C:
			if metrics.Read(heap); heap[0].Value.Uint64() > baseline+maxHeapGrowth {
				panic(fmt.Sprintf("%s grew the heap by more than %d MiB", what, maxHeapGrowth>>20))
			}
		}
	}
}

// maxHeapGrowth is many times what any call these tests guard allocates, and
// a fraction of the memory a runaway takes before anything outside the tests
// stops it.
const maxHeapGrowth = 512 << 20
