package install_test

import (
	"context"
	"os"
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
