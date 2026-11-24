package datadir

import "testing"

// OwnedByStranger makes path read as someone else's for the rest of the
// test (ownedByStranger), for the tests outside the package.
func OwnedByStranger(t *testing.T, path string) {
	t.Helper()
	ownedByStranger(t, path)
}
