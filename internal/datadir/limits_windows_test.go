package datadir_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// lockedDataDir returns a data directory Secure made, and the folder above
// it, once its own list is stored as sddl, with the list Private gives put
// back when the test ends.
func lockedDataDir(t *testing.T, sddl string) (dir, top string) {
	t.Helper()
	top = privateTop(t)
	dir = filepath.Join(top, "loadline")
	if err := datadir.Secure(dir, top); err != nil {
		t.Fatal(err)
	}
	fspermtest.SetAccessListAsIs(t, dir, sddl)
	t.Cleanup(func() { _ = fsperm.PassDown(dir) })
	return dir, top
}

// refusedWith checks that Secure and Check both fail on dir with an error
// that holds each of want, as SECURITY.md's known limitations quote it.
func refusedWith(t *testing.T, dir, top string, want ...string) {
	t.Helper()
	_, checkErr := datadir.Check(dir, top)
	for name, err := range map[string]error{"Secure": datadir.Secure(dir, top), "Check": checkErr} {
		for _, part := range want {
			if err == nil || !strings.Contains(err.Error(), part) {
				t.Errorf("%s = %v, want an error that holds %q", name, err, part)
			}
		}
	}
}

// A data directory whose list gives you nothing cannot be opened even to
// find the folders that hold it.
func TestSecure_SaysADataDirectoryThatGivesYouNothingCannotBeFollowed(t *testing.T) {
	dir, top := lockedDataDir(t, "D:P(A;OICI;FA;;;SY)")

	refusedWith(t, dir, top, "find the folders that hold "+dir+": open "+dir+": Access is denied.",
		"; set LOADLINE_HOME to a folder in your user profile")
}

// One whose list keeps you only from listing it is opened, and then cannot
// be read.
func TestSecure_SaysADataDirectoryYouMayNotListCannotBeRead(t *testing.T) {
	user := fspermtest.UserSID(t)
	dir, top := lockedDataDir(t, "D:P(D;;CC;;;WD)(A;OICI;FA;;;"+user+")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")

	refusedWith(t, dir, top, "read the data directory: ", "Access is denied")
}
