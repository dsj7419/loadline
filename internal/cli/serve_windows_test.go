package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/cli"
)

// soExclusiveAddrUse is Winsock's SO_EXCLUSIVEADDRUSE, the complement of
// SO_REUSEADDR.
const soExclusiveAddrUse = ^windows.SO_REUSEADDR

// listenExclusively holds every address of a free port the way a program
// that binds exclusively does: a loopback bind of that port then fails with
// WSAEACCES, as it does for a port in a range Windows has reserved. Only a
// wildcard holder has that effect; an exclusive loopback holder gives
// WSAEADDRINUSE instead.
//
// A wildcard listen by a newly built test binary makes Windows Defender
// Firewall ask, interactively, whether to let it accept connections, and
// leaves a rule behind for each answer. So it runs only in CI (which sets
// CI=true); listenRefusal's own test covers the message everywhere.
func listenExclusively(t *testing.T) int {
	t.Helper()
	if os.Getenv("CI") != "true" {
		t.Skip("binds a wildcard address, which raises an interactive Windows Defender Firewall prompt on a developer machine; runs in CI")
	}
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var setErr error
		if err := c.Control(func(fd uintptr) {
			setErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, soExclusiveAddrUse, 1)
		}); err != nil {
			return err
		}
		return setErr
	}}
	l, err := lc.Listen(context.Background(), "tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("exclusive listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return portOf(t, l.Addr())
}

func TestServe_PortReservedOrHeldExclusivelyNamesThePort(t *testing.T) {
	for _, demo := range []bool{false, true} {
		t.Run(fmt.Sprintf("demo %v", demo), func(t *testing.T) {
			f := newServeFixture(t)
			f.port = listenExclusively(t)
			o := f.options()
			o.Demo = demo

			var stdout, stderr bytes.Buffer
			code := cli.Serve(t.Context(), o, cli.IO{In: strings.NewReader(""), Out: &stdout, Err: &stderr})

			want := fmt.Sprintf("loadline: port %d is reserved by Windows or held exclusively by another program; "+
				"pass --port to use another\n", f.port)
			if code != 1 || stderr.String() != want || stdout.Len() != 0 {
				t.Errorf("Serve = %d, stdout %q, stderr %q; want 1 and %q", code, stdout.String(), stderr.String(), want)
			}
		})
	}
}
