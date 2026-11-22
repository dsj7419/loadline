package fsperm

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// An access list can hold generic rights, which Windows turns into what they
// stand for on a file. The folder checks must count them the same.
func TestFileRights_CountsGenericRightsAsWhatTheyStandFor(t *testing.T) {
	tests := []struct {
		name string
		mask windows.ACCESS_MASK
		want windows.ACCESS_MASK
		not  windows.ACCESS_MASK
	}{
		{name: "all", mask: windows.GENERIC_ALL, want: windows.DELETE | fileDeleteChild},
		{name: "write", mask: windows.GENERIC_WRITE, want: windows.FILE_WRITE_DATA, not: windows.DELETE | fileDeleteChild},
		{name: "read", mask: windows.GENERIC_READ, want: windows.FILE_READ_DATA, not: windows.DELETE},
		{name: "execute", mask: windows.GENERIC_EXECUTE, want: windows.FILE_EXECUTE, not: windows.DELETE},
		{name: "specific", mask: windows.DELETE, want: windows.DELETE, not: windows.GENERIC_ALL},
	}
	for _, tt := range tests {
		got := fileRights(tt.mask)
		if got&tt.want != tt.want || got&tt.not != 0 {
			t.Errorf("fileRights(%s) = %#x, want %#x in it and none of %#x", tt.name, got, tt.want, tt.not)
		}
	}
}

// ownedBySID makes path, for the rest of the test, read as owned by sid: an
// account that is not elevated cannot give a folder another owner. The walk
// asks for a folder's owner by the names it was given, and again as the
// system resolves them, which spells a short 8.3 name in full; both name
// path.
func ownedBySID(t *testing.T, path string, sid *windows.SID) {
	t.Helper()
	owned, err := resolved(path)
	if err != nil {
		t.Fatal(err)
	}
	previous := ownerOf
	ownerOf = func(p string) (*windows.SID, error) {
		if asked, err := resolved(p); err == nil && samePath(asked, owned) {
			return sid, nil
		}
		return previous(p)
	}
	t.Cleanup(func() { ownerOf = previous })
}

func wellKnown(t *testing.T, kind windows.WELL_KNOWN_SID_TYPE) *windows.SID {
	t.Helper()
	sid, err := windows.CreateWellKnownSid(kind)
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

// The owner of a folder may always change its permissions, and so give
// themselves the right to delete or rename it, or what it holds. A data
// directory, or a folder above it, owned by anyone but the user, SYSTEM,
// Administrators or TrustedInstaller is refused.
func TestMovableAncestor_RefusesAFolderThatBelongsToSomeoneElse(t *testing.T) {
	users := wellKnown(t, windows.WinAuthenticatedUserSid)
	for _, where := range []string{"the data directory", "the folder that holds it", "a folder further up"} {
		t.Run(where, func(t *testing.T) {
			top := t.TempDir()
			if err := Private(top); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(top, "tools", "inner", "loadline")
			if err := MkdirAll(dir); err != nil {
				t.Fatal(err)
			}
			owned := map[string]string{
				"the data directory":       dir,
				"the folder that holds it": filepath.Dir(dir),
				"a folder further up":      filepath.Dir(filepath.Dir(dir)),
			}[where]
			ownedBySID(t, owned, users)

			got, err := MovableAncestor(dir, top)

			if want := owned + " belongs to " + accountName(users) + ", who"; err != nil || !strings.HasPrefix(got, want) {
				t.Errorf("MovableAncestor = %q, %v; want it to start %q", got, err, want)
			}
		})
	}
}

func TestMovableAncestor_TakesTheOwnersThatMayTakeAnyFolderAnyway(t *testing.T) {
	trustedInstaller, err := windows.StringToSid(trustedInstallerSID)
	if err != nil {
		t.Fatal(err)
	}
	for name, owner := range map[string]*windows.SID{
		"SYSTEM":           wellKnown(t, windows.WinLocalSystemSid),
		"Administrators":   wellKnown(t, windows.WinBuiltinAdministratorsSid),
		"TrustedInstaller": trustedInstaller,
	} {
		t.Run(name, func(t *testing.T) {
			top := t.TempDir()
			if err := Private(top); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(top, "tools", "loadline")
			if err := MkdirAll(dir); err != nil {
				t.Fatal(err)
			}
			ownedBySID(t, dir, owner)
			ownedBySID(t, filepath.Dir(dir), owner)

			if got, err := MovableAncestor(dir, top); err != nil || got != "" {
				t.Errorf("MovableAncestor = %q, %v; want \"\" for folders %s owns", got, err, name)
			}
		})
	}
}

// The folder the walk is told to trust is not judged, its owner included.
func TestMovableAncestor_DoesNotJudgeTheOwnerOfTheFolderItTrusts(t *testing.T) {
	top := t.TempDir()
	if err := Private(top); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(top, "loadline")
	ownedBySID(t, top, wellKnown(t, windows.WinAuthenticatedUserSid))

	if got, err := MovableAncestor(dir, top); err != nil || got != "" {
		t.Errorf("MovableAncestor = %q, %v; want \"\"", got, err)
	}
}

// GetFinalPathNameByHandle names a folder in the \\?\ form, which the walk
// and what it reports use without the prefix.
func TestDOSPath_TakesOffTheExtendedLengthPrefix(t *testing.T) {
	for in, want := range map[string]string{
		`\\?\C:\Users\me\AppData\Local`: `C:\Users\me\AppData\Local`,
		`\\?\D:\`:                       `D:\`,
		`\\?\UNC\server\share\loadline`: `\\server\share\loadline`,
		`C:\plain`:                      `C:\plain`,
	} {
		if got := dosPath(in); got != want {
			t.Errorf("dosPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// The owner tests replace ownerOf, so the real reader runs elsewhere only on
// what the test's own account owns. These read real owners that are not it:
// the Windows folder is TrustedInstaller's, the folder of the user profiles
// SYSTEM's, and a file of the test's whose group is set apart from its
// owner reads as the test's account's still (as Administrators' when the
// test runs elevated).
func TestOwnerOf_ReadsTheRealOwner(t *testing.T) {
	trustedInstaller, err := windows.StringToSid(trustedInstallerSID)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(t.TempDir(), "mine")
	if err := os.WriteFile(mine, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	users := wellKnown(t, windows.WinBuiltinUsersSid)
	if err := windows.SetNamedSecurityInfo(mine, windows.SE_FILE_OBJECT, windows.GROUP_SECURITY_INFORMATION, nil, users, nil, nil); err != nil {
		t.Fatalf("give %s another group: %v", mine, err)
	}

	for path, owners := range map[string][]*windows.SID{
		os.Getenv("SystemRoot"):           {trustedInstaller},
		filepath.Dir(os.Getenv("PUBLIC")): {wellKnown(t, windows.WinLocalSystemSid)},
		mine:                              {user.User.Sid, wellKnown(t, windows.WinBuiltinAdministratorsSid)},
	} {
		got, err := ownerOf(path)
		if err != nil || !slices.ContainsFunc(owners, got.Equals) {
			t.Errorf("ownerOf(%s) = %v, %v; want one of %v", path, got, err, owners)
		}
	}
}
