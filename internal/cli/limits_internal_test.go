package cli

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

// The server listens on the IPv4 loopback address written as a number: a
// name such as localhost resolves as the machine's hosts file says, which
// can point it at an address other machines reach.
func TestListenAddress_IsTheLoopbackAddressByNumber(t *testing.T) {
	if got := listenAddress(8289); got != "127.0.0.1:8289" {
		t.Errorf("listenAddress(8289) = %q, want %q", got, "127.0.0.1:8289")
	}
}

// The limits bound what a slow or stalled client can hold: a header that
// never ends, a body that stalls, a client that stops reading the answer,
// an idle keep-alive connection, and a header block of any size.
func TestNewHTTPServer_BoundsWhatAClientCanHold(t *testing.T) {
	srv := newHTTPServer(context.Background(), http.NotFoundHandler(), slog.New(slog.DiscardHandler))

	for name, got := range map[string][2]time.Duration{
		"ReadHeaderTimeout": {srv.ReadHeaderTimeout, 5 * time.Second},
		"ReadTimeout":       {srv.ReadTimeout, 30 * time.Second},
		"WriteTimeout":      {srv.WriteTimeout, 30 * time.Second},
		"IdleTimeout":       {srv.IdleTimeout, 120 * time.Second},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %v, want %v", name, got[0], got[1])
		}
	}
	if srv.MaxHeaderBytes != 65536 {
		t.Errorf("MaxHeaderBytes = %d, want 65536", srv.MaxHeaderBytes)
	}
}
