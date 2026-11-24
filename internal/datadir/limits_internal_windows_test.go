package datadir

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// lockedOutDir returns a data directory Secure made, and the folder above it,
// once the directory's own list is stored as sddl. The private list is
// put back when the test ends, so that the folder can be removed.
func lockedOutDir(t *testing.T, sddl string) (dir, top string) {
	t.Helper()
	top = t.TempDir()
	if err := fsperm.Private(top); err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(top, "loadline")
	if err := Secure(dir, top); err != nil {
		t.Fatal(err)
	}
	fspermtest.SetAccessListAsIs(t, dir, sddl)
	t.Cleanup(func() { _ = fsperm.PassDown(dir) })
	return dir, top
}

// listsUnchangeable makes every folder, for the rest of the test, one whose
// list this process may not change, as for a user who is not its owner: an
// account that is not elevated cannot give a folder another owner.
func listsUnchangeable(t *testing.T) {
	t.Helper()
	previous := canChangeList
	canChangeList = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { canChangeList = previous })
}

// ownedAs makes every folder's owner read, for the rest of the test, as
// owner says.
func ownedAs(t *testing.T, owner func(string) (string, error)) {
	t.Helper()
	previous := ownerName
	ownerName = owner
	t.Cleanup(func() { ownerName = previous })
}

// A data directory that keeps the user out, and whose list they may not
// change, as one an elevated shell made, which Administrators own, is
// refused, naming its owner and the command an administrator can reset it
// with. A folder that gives this process nothing may not let it read its
// owner either, and the sentence then says so. Nothing is changed.
func TestSecure_RefusesALockedDataDirectoryItCannotChange(t *testing.T) {
	for name, tt := range map[string]struct {
		owner func(string) (string, error)
		says  string
	}{
		"owned by Administrators": {
			owner: func(string) (string, error) { return `BUILTIN\Administrators`, nil },
			says:  `it belongs to BUILTIN\Administrators`,
		},
		"its owner unreadable": {
			owner: func(string) (string, error) { return "", errors.New("access is denied") },
			says:  "its owner cannot be read",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, top := lockedOutDir(t, "D:P")
			listsUnchangeable(t)
			ownedAs(t, tt.owner)
			before := fspermtest.Descriptor(t, dir)
			want := dir + " keeps you out, and Loadline cannot let you back in without widening who else may use it; " +
				tt.says + `: an administrator can reset it with icacls "` + dir + `" /reset, then run loadline install`

			for step, err := range map[string]error{"Secure": Secure(dir, top), "Make": Make(dir), "Verify": Verify(dir, top)} {
				if err == nil || err.Error() != want {
					t.Errorf("%s = %v, want %q", step, err, want)
				}
			}
			if got, err := Check(dir, top); err != nil || got != want {
				t.Errorf("Check = %q, %v; want %q", got, err, want)
			}
			if after := fspermtest.Descriptor(t, dir); !bytes.Equal(after, before) {
				t.Errorf("the list of %s changed to %s; want it as it was, D:P", dir, fspermtest.AccessList(t, dir))
			}
		})
	}
}

// A data directory the user may list and write to, but whose list they may
// not change, as one Administrators or SYSTEM own that gives the user Modify
// alone, does not keep them out, but cannot be made private either. It is
// refused with a sentence that says so, and its list is left as it was.
func TestSecure_RefusesADataDirectoryItMayListButNotChange(t *testing.T) {
	dir, top := lockedOutDir(t, "D:P(A;OICI;0x1301bf;;;"+fspermtest.UserSID(t)+")")
	listsUnchangeable(t)
	ownedAs(t, func(string) (string, error) { return `BUILTIN\Administrators`, nil })
	before := fspermtest.Descriptor(t, dir)
	want := dir + ` cannot be made private, since you may not change who may use it; it belongs to BUILTIN\Administrators: ` +
		`set LOADLINE_HOME to a folder of your own, or have an administrator reset it with icacls "` + dir +
		`" /reset, then run loadline install`

	for step, err := range map[string]error{"Secure": Secure(dir, top), "Make": Make(dir), "Verify": Verify(dir, top)} {
		if err == nil || err.Error() != want {
			t.Errorf("%s = %v, want %q", step, err, want)
		}
	}
	if got, err := Check(dir, top); err != nil || got != want {
		t.Errorf("Check = %q, %v; want %q", got, err, want)
	}
	if after := fspermtest.Descriptor(t, dir); !bytes.Equal(after, before) {
		t.Errorf("the list of %s changed to %s; want it as it was", dir, fspermtest.AccessList(t, dir))
	}
}
