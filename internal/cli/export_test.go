package cli

import (
	"testing"
	"time"
)

// IdleCheckEvery is how often a server checks whether it has been idle long
// enough to exit.
const IdleCheckEvery = idleCheckEvery

// SetTapSendTimeout replaces the bound on the tap's post for the rest of the
// test.
func SetTapSendTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()

	previous := tapSendTimeout
	tapSendTimeout = timeout
	t.Cleanup(func() { tapSendTimeout = previous })
}

// SetHookSendTimeout replaces the bound on the hook's post for the rest of
// the test.
func SetHookSendTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()

	previous := hookSendTimeout
	hookSendTimeout = timeout
	t.Cleanup(func() { hookSendTimeout = previous })
}
