package datadir_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// openOnly lets its trustee open a folder and read its attributes and its
// access list, but not list what it holds.
const openOnly = "0x120080"

// lockedDataDir returns a data directory Secure made, which holds the token
// and a file by each name in more, and the folder above it, once the
// directory's own list is stored as sddl. The list Private gives is put back
// when the test ends, where the list lets it be, so that the folder can be
// removed.
func lockedDataDir(t *testing.T, sddl string, more ...string) (dir, top string) {
	t.Helper()
	top = privateTop(t)
	dir = filepath.Join(top, "loadline")
	if err := datadir.Secure(dir, top); err != nil {
		t.Fatal(err)
	}
	for _, name := range append([]string{"token"}, more...) {
		writeFile(t, filepath.Join(dir, name))
	}
	fspermtest.SetAccessListAsIs(t, dir, sddl)
	t.Cleanup(func() { _ = fsperm.PassDown(dir) })
	return dir, top
}

// denyListing is an access list that gives the user full control of the data
// directory, as Private does, but denies everyone the right to list it,
// which no entry Loadline adds overrides.
func denyListing(t *testing.T) string {
	t.Helper()
	user := fspermtest.UserSID(t)
	return "D:P(D;;CC;;;WD)(A;OICI;FA;;;" + user + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
}

// keptOut is the sentence install, the server and doctor refuse dir with
// when they cannot let the user back in without widening who else may use
// it.
func keptOut(t *testing.T, dir string) string {
	t.Helper()
	owner, err := fsperm.OwnerName(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir + " keeps you out, and Loadline cannot let you back in without widening who else may use it; it belongs to " +
		owner + `: an administrator can reset it with icacls "` + dir + `" /reset, then run loadline install`
}

// takenBack is what doctor reports dir with when install can let the user
// back in.
func takenBack(dir string) string {
	return dir + " keeps you out, though you can let yourself back in; run loadline install, which does that and makes it private"
}

// A data directory whose own list keeps its owner from listing it is taken
// back: install checks it first, changing nothing, and then lets the user
// back in, judges it and makes it private. The creation of the token, which
// makes the directory without judging the folders above it, does the same.
func TestSecure_TakesBackADataDirectoryThatKeepsItsOwnerOut(t *testing.T) {
	for name, list := range map[string]string{
		"a list that gives nothing": "D:P",
		"open but not list":         "D:P(A;;" + openOnly + ";;;" + fspermtest.UserSID(t) + ")",
	} {
		for step, prepare := range map[string]func(dir, top string) error{
			"Secure": datadir.Secure,
			"Make":   func(dir, _ string) error { return datadir.Make(dir) },
		} {
			t.Run(step+", "+name, func(t *testing.T) {
				dir, top := lockedDataDir(t, list)
				before := fspermtest.Descriptor(t, dir)

				if err := datadir.Verify(dir, top); err != nil {
					t.Errorf("Verify = %v, want nil: Secure lets the user back in before it judges the folder", err)
				}
				if after := fspermtest.Descriptor(t, dir); !bytes.Equal(after, before) {
					t.Errorf("Verify changed the list of %s, whose list was %s", dir, list)
				}

				if err := prepare(dir, top); err != nil {
					t.Fatalf("%s: %v", step, err)
				}

				if private, err := fsperm.DirPrivate(dir); err != nil || !private {
					t.Errorf("after %s, DirPrivate = %t, %v; want true: %s holds %s", step, private, err, dir, fspermtest.AccessList(t, dir))
				}
				names, err := os.ReadDir(dir)
				if err != nil || !slices.ContainsFunc(names, func(entry os.DirEntry) bool { return entry.Name() == "token" }) {
					t.Errorf("after %s, the user lists %s as %v, %v; want the token there", step, dir, names, err)
				}
				if _, err := os.ReadFile(filepath.Join(dir, "token")); err != nil {
					t.Errorf("after %s, the user cannot read the token: %v", step, err)
				}
			})
		}
	}
}

// A denial keeps the user from listing it whatever is added after it, and
// taking it off would let in everyone else it keeps out, so the data
// directory is refused, and its list is left exactly as it was.
func TestSecure_RefusesADataDirectoryADenyEntryKeepsLocked(t *testing.T) {
	dir, top := lockedDataDir(t, denyListing(t))
	before := fspermtest.Descriptor(t, dir)

	err := datadir.Secure(dir, top)

	if want := keptOut(t, dir); err == nil || err.Error() != want {
		t.Errorf("Secure = %v, want %q", err, want)
	}
	if after := fspermtest.Descriptor(t, dir); !bytes.Equal(after, before) {
		t.Errorf("Secure left the list of %s as %s; want it as it was, %s", dir, fspermtest.AccessList(t, dir), denyListing(t))
	}
}

// An OWNER RIGHTS entry says what a folder's owner may do instead of reading
// and changing its list, as the owner otherwise always may: this one lets
// them read and list it, and no more. The data directory does not keep them
// out, but making it private takes changing its list, so Loadline refuses
// it, says why and what to do, and leaves its list as it was.
func TestSecure_SaysWhatToDoWhenOwnerRightsAreWithheld(t *testing.T) {
	dir, top := lockedDataDir(t, "D:P(A;;0x1200a9;;;OW)")
	before := fspermtest.Descriptor(t, dir)

	if err := datadir.Secure(dir, top); err == nil || err.Error() != cannotMakePrivate(t, dir) {
		t.Errorf("Secure = %v, want %q", err, cannotMakePrivate(t, dir))
	}
	if after := fspermtest.Descriptor(t, dir); !bytes.Equal(after, before) {
		t.Errorf("Secure left the list of %s as %s; want it as it was, D:P(A;;0x1200a9;;;OW)", dir, fspermtest.AccessList(t, dir))
	}
}

// cannotMakePrivate is the sentence install, the server and doctor refuse dir
// with when the user may list it but not change who may use it.
func cannotMakePrivate(t *testing.T, dir string) string {
	t.Helper()
	owner, err := fsperm.OwnerName(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir + " cannot be made private, since you may not change who may use it; it belongs to " + owner +
		`: set LOADLINE_HOME to a folder of your own, or have an administrator reset it with icacls "` + dir +
		`" /reset, then run loadline install`
}

// Doctor says, changing nothing, whether install can let the user back into
// a data directory that keeps them out, and install's own first check
// agrees: it leaves one it can let them back into to Secure, and refuses
// the rest with doctor's sentence.
func TestCheck_SaysWhetherInstallCanTakeBackALockedDataDirectory(t *testing.T) {
	user := fspermtest.UserSID(t)
	for name, tt := range map[string]struct {
		list string
		says func(t *testing.T, dir string) string
	}{
		"a list that gives nothing":  {list: "D:P"},
		"open but not list":          {list: "D:P(A;;" + openOnly + ";;;" + user + ")"},
		"listing denied to everyone": {list: denyListing(t), says: keptOut},
		"owner rights withheld":      {list: "D:P(A;;0x1200a9;;;OW)", says: cannotMakePrivate},
	} {
		t.Run(name, func(t *testing.T) {
			dir, top := lockedDataDir(t, tt.list)
			before := fspermtest.Descriptor(t, dir)

			got, err := datadir.Check(dir, top)
			verified := datadir.Verify(dir, top)

			want := takenBack(dir)
			if tt.says != nil {
				want = tt.says(t, dir)
			}
			if err != nil || got != want {
				t.Errorf("Check = %q, %v; want %q", got, err, want)
			}
			switch {
			case tt.says == nil && verified != nil:
				t.Errorf("Verify = %v, want nil", verified)
			case tt.says != nil && (verified == nil || verified.Error() != want):
				t.Errorf("Verify = %v, want %q", verified, want)
			}
			if after := fspermtest.Descriptor(t, dir); !bytes.Equal(after, before) {
				t.Errorf("Check changed the list of %s, which was %s", dir, tt.list)
			}
		})
	}
}

// Secure lets the user back in before it judges what the data directory
// holds, and something there can still be refused then. The directory keeps
// its list as it was, with the one entry for the user that lets them list
// it, and nothing else: doctor then reports what Secure refused.
func TestSecure_KeepsOnlyTheUsersEntryWhenItRefusesADataDirectoryItLetThemInto(t *testing.T) {
	dir, top := lockedDataDir(t, "D:P", "notes.txt")
	userSID, err := windows.StringToSid(fspermtest.UserSID(t))
	if err != nil {
		t.Fatal(err)
	}
	listing := fspermtest.ACE{
		Type: windows.ACCESS_ALLOWED_ACE_TYPE,
		Mask: windows.FILE_LIST_DIRECTORY | windows.FILE_READ_EA | windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES |
			windows.SYNCHRONIZE | windows.READ_CONTROL,
		SID: userSID,
	}

	err = datadir.Secure(dir, top)

	if want := dir + " holds notes.txt, which is not Loadline's"; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("Secure = %v, want it refused, starting %q", err, want)
	}
	if got := fspermtest.AccessList(t, dir); !got.Protected || !slices.EqualFunc(got.Entries, []fspermtest.ACE{listing}, fspermtest.ACE.Equal) {
		t.Errorf("after Secure, %s holds %s; want D:P with the one entry that lets the user list it", dir, got)
	}
	if got, checkErr := datadir.Check(dir, top); checkErr != nil || got != err.Error() {
		t.Errorf("Check = %q, %v; want what Secure refused, %q", got, checkErr, err)
	}
}
