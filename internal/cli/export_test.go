package cli

import (
	"testing"
	"time"
)

// SetTapSendTimeout replaces the bound on the tap's post for the rest of the
// test.
func SetTapSendTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()

	previous := tapSendTimeout
	tapSendTimeout = timeout
	t.Cleanup(func() { tapSendTimeout = previous })
}
