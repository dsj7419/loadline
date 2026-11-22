//go:build !windows

package cli

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
)

// accessDeniedListenError is the error net.Listen returns when the system
// refuses the bind with EACCES.
func accessDeniedListenError(port int) error {
	return &net.OpError{
		Op:   "listen",
		Net:  "tcp",
		Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port},
		Err:  os.NewSyscallError("bind", syscall.EACCES),
	}
}

func TestRefuseToListen_AccessDeniedNamesThePortAndSuggestsAnother(t *testing.T) {
	const port = 18290
	for _, demoMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("demo %v", demoMode), func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := refuseToListen(t.Context(), port, demoMode, accessDeniedListenError(port),
				IO{In: strings.NewReader(""), Out: &stdout, Err: &stderr})

			want := "loadline: port 18290 is not open to this program; pass --port to use another\n"
			if code != exitFailure || stderr.String() != want || stdout.Len() != 0 {
				t.Errorf("refuseToListen = %d, stdout %q, stderr %q; want 1 and %q", code, stdout.String(), stderr.String(), want)
			}
		})
	}
}
