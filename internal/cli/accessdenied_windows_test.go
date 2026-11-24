package cli

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// accessDeniedListenError is the error net.Listen returns when Winsock
// refuses the bind with WSAEACCES: for a port in a range Windows reserves,
// or one another program holds with an exclusive wildcard bind.
func accessDeniedListenError(port int) error {
	return &net.OpError{
		Op:   "listen",
		Net:  "tcp",
		Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port},
		Err:  os.NewSyscallError("bind", windows.WSAEACCES),
	}
}

func TestListenRefusal_AccessDeniedNamesThePortAndSuggestsAnother(t *testing.T) {
	const port = 18290
	for _, demoMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("demo %v", demoMode), func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			refusal := listenRefusal(t.Context(), port, ServeOptions{Demo: demoMode}, accessDeniedListenError(port))
			if refusal == nil {
				t.Fatal("listenRefusal = nil, want the port refused")
			}
			code := fail(IO{In: strings.NewReader(""), Out: &stdout, Err: &stderr}, refusal)

			want := "loadline: port 18290 is reserved by Windows or held exclusively by another program; " +
				"pass --port to use another\n"
			if code != exitFailure || stderr.String() != want || stdout.Len() != 0 {
				t.Errorf("listenRefusal then fail = %d, stdout %q, stderr %q; want 1 and %q", code, stdout.String(), stderr.String(), want)
			}
		})
	}
}
