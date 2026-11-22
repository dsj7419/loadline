package config

import "testing"

// SetBeforePublish runs f, for the rest of the test, each time EnsureToken
// has written a new token to its temporary file, at temp, and is about to
// link it into place.
func SetBeforePublish(t *testing.T, f func(temp string)) {
	t.Helper()

	previous := testHookBeforePublish
	testHookBeforePublish = f
	t.Cleanup(func() { testHookBeforePublish = previous })
}
