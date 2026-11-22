package forward

import (
	"net/http"
	"testing"
)

// Go's environment proxy already passes loopback addresses by, but the
// client must not depend on that: the token rides on every request.
func TestDirectClient_NeverUsesAProxy(t *testing.T) {
	transport, ok := direct.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("direct client transport is %T, want *http.Transport", direct.Transport)
	}
	if transport.Proxy != nil {
		t.Error("direct client transport has a proxy function, want none")
	}
	if (Client{}).httpClient() != direct {
		t.Error("a Client without HTTP does not use the direct client")
	}
}
