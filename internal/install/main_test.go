package install_test

import (
	"context"
	"fmt"
	"os"
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
// deadline, or if the heap grows past maxHeap meanwhile. A loop that never
// ended would otherwise hold the suite until its timeout, or fill memory
// first if it allocates as it goes, and a failing test cannot stop the
// goroutine running it. f must not call t's methods.
func within(deadline time.Duration, what string, f func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	heap := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	timeout := time.After(deadline)
	for {
		select {
		case <-done:
			return
		case <-timeout:
			panic(fmt.Sprintf("%s did not return within %s", what, deadline))
		case <-tick.C:
			if metrics.Read(heap); heap[0].Value.Uint64() > maxHeap {
				panic(fmt.Sprintf("%s grew the heap past %d MiB", what, maxHeap>>20))
			}
		}
	}
}

// maxHeap is many times what the tests hold at once, and a fraction of the
// memory a runaway takes before anything outside the tests stops it.
const maxHeap = 512 << 20
