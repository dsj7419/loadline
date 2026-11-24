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

// writeFiles creates each of paths, holding a byte.
func writeFiles(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// replaceReadFileID makes Inspect read each file's ID with read, for the rest
// of the test.
func replaceReadFileID(t *testing.T, read func(windows.Handle) (FileID, error)) {
	t.Helper()
	previous := readFileID
	readFileID = read
	t.Cleanup(func() { readFileID = previous })
}

// ReFS gives each file a 128-bit ID, and two files' IDs can differ only above
// their low 64 bits. Inspect keeps the whole ID, and tells such files apart.
// No volume at hand numbers files so, so the IDs read are rewritten: the low
// 64 bits the same for every file, and above them, the low 64 bits the real
// IDs differ in.
func TestInspect_TellsApartIDsThatDifferOnlyAboveTheLow64Bits(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "token"), filepath.Join(dir, ".token-9")
	writeFiles(t, a, b)
	readTheVolumes := readFileID
	replaceReadFileID(t, func(handle windows.Handle) (FileID, error) {
		id, err := readTheVolumes(handle)
		if err != nil {
			return FileID{}, err
		}
		var rewritten [16]byte
		copy(rewritten[:8], "sharedlo")
		copy(rewritten[8:], id.id[:8])
		return FullFileID(id.volume, rewritten), nil
	})

	gotA, errA := Inspect(a)
	gotB, errB := Inspect(b)

	if errA != nil || errB != nil {
		t.Fatalf("Inspect = %v, %v", errA, errB)
	}
	if [8]byte(gotA.ID.id[:8]) != [8]byte(gotB.ID.id[:8]) {
		t.Fatalf("the IDs read differ in their low 64 bits, %+v and %+v, so the test proves nothing", gotA.ID, gotB.ID)
	}
	if gotA.ID == gotB.ID {
		t.Errorf("Inspect(%s).ID = Inspect(%s).ID = %+v; want two files told apart by the bits above the low 64", a, b, gotA.ID)
	}
	if again, err := Inspect(a); err != nil || again.ID != gotA.ID {
		t.Errorf("Inspect(%s) again = %+v, %v; want the same ID, %+v", a, again, err, gotA.ID)
	}
}

// indexOf returns the ID of path as its file index and the volume's 32-bit
// serial number give it.
func indexOf(t *testing.T, path string) FileID {
	t.Helper()
	handle, err := openItself(path, windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		t.Fatal(err)
	}
	return indexFileID(uint64(info.VolumeSerialNumber), uint64(info.FileIndexHigh)<<32|uint64(info.FileIndexLow))
}

// NTFS gives every file its full ID: its file index widened to 128 bits,
// beside the volume's 64-bit serial number, whose low 32 bits are the serial
// number that comes with the index. Inspect reads that full ID there, each
// number where FILE_ID_INFO keeps it, rather than falling back.
func TestInspect_ReadsTheFullIDOnNTFS(t *testing.T) {
	dir := t.TempDir()
	if fileSystem := fileSystemOf(t, dir); fileSystem != "NTFS" {
		t.Skipf("%s is on %s, not NTFS", dir, fileSystem)
	}
	file := filepath.Join(dir, "token")
	writeFiles(t, file)
	index := indexOf(t, file)

	got, err := Inspect(file)

	if err != nil || got.ID.form != fullID || got.ID.id != index.id || got.ID.volume&0xffffffff != index.volume {
		t.Errorf("Inspect = %+v, %v; want the full ID, of the same numbers as the index %+v", got, err, index)
	}
}

// The full ID comes from a call that a volume refuses when it cannot give
// it, with an error that depends on its file system and driver. Whatever the
// refusal, Inspect reads the file index instead, as it did before it read the
// full ID, so it works wherever it worked then.
func TestInspect_FallsBackToTheFileIndexWhenTheFullIDCannotBeRead(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	writeFiles(t, file)
	want := indexOf(t, file)

	for name, refusal := range map[string]windows.Errno{
		"ERROR_INVALID_PARAMETER":    windows.ERROR_INVALID_PARAMETER,
		"ERROR_NOT_SUPPORTED":        windows.ERROR_NOT_SUPPORTED,
		"ERROR_INVALID_FUNCTION":     windows.ERROR_INVALID_FUNCTION,
		"ERROR_ACCESS_DENIED":        windows.ERROR_ACCESS_DENIED,
		"ERROR_CALL_NOT_IMPLEMENTED": windows.ERROR_CALL_NOT_IMPLEMENTED,
	} {
		t.Run(name, func(t *testing.T) {
			refuses := func(windows.Handle, uint32, *byte, uint32) error { return refusal }
			replaceReadFileID(t, func(handle windows.Handle) (FileID, error) { return fileIDByHandle(handle, refuses) })

			got, err := Inspect(file)

			if err != nil || got.ID != want || got.Names != 1 || got.Link || got.Folder {
				t.Errorf("Inspect = %+v, %v; want a file of one name whose ID is its index, %+v", got, err, want)
			}
		})
	}
}

// ReFS, which a Dev Drive uses, numbers files with 128 bits. On a ReFS volume
// Inspect reads every ID in full: two files differ, and a file's second name
// has its ID. CI makes such a volume and names a folder on it in
// LOADLINE_TEST_REFS_DIR.
func TestInspect_OnReFS(t *testing.T) {
	dir := reFSDir(t)
	a, b, second := filepath.Join(dir, "token"), filepath.Join(dir, ".token-9"), filepath.Join(dir, "second")
	writeFiles(t, a, b)
	if err := os.Link(a, second); err != nil {
		t.Fatalf("give %s a second name on ReFS: %v", a, err)
	}

	ids := make(map[string]FileID)
	for _, path := range []string{a, b, second} {
		got, err := Inspect(path)
		if err != nil {
			t.Fatalf("Inspect(%s): %v", path, err)
		}
		if got.ID.form != fullID {
			t.Errorf("Inspect(%s).ID = %+v; want it read in full", path, got.ID)
		}
		ids[path] = got.ID
	}

	if ids[a] == ids[b] {
		t.Errorf("Inspect(%s).ID = Inspect(%s).ID = %+v; want two files told apart", a, b, ids[a])
	}
	if ids[second] != ids[a] {
		t.Errorf("Inspect(%s).ID = %+v; want its file's ID, %+v", second, ids[second], ids[a])
	}
}

// reFSDir returns a new folder inside the one LOADLINE_TEST_REFS_DIR names,
// removed when the test ends. It skips the test when that names no folder on
// a ReFS volume.
func reFSDir(t *testing.T) string {
	t.Helper()
	root := os.Getenv("LOADLINE_TEST_REFS_DIR")
	//nolint:gosec // G703: root is the folder whoever runs the tests sets aside for them
	if info, err := os.Stat(root); root == "" || err != nil || !info.IsDir() {
		t.Skipf("LOADLINE_TEST_REFS_DIR (%q) names no folder on a ReFS volume", root)
	}
	if fileSystem := fileSystemOf(t, root); fileSystem != "ReFS" {
		t.Skipf("LOADLINE_TEST_REFS_DIR (%s) is on %s, not ReFS", root, fileSystem)
	}
	dir, err := os.MkdirTemp(root, "inspect-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil { //nolint:gosec // G703: dir is the folder made above for this test alone
			t.Errorf("remove %s: %v", dir, err)
		}
	})
	return dir
}

// fileSystemOf names the file system of the volume that holds path, such as
// NTFS or ReFS.
func fileSystemOf(t *testing.T, path string) string {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	var volume, fileSystem [windows.MAX_PATH + 1]uint16
	if err := windows.GetVolumePathName(name, &volume[0], uint32(len(volume))); err != nil {
		t.Fatalf("find the volume that holds %s: %v", path, err)
	}
	if err := windows.GetVolumeInformation(&volume[0], nil, 0, nil, nil, nil, &fileSystem[0], uint32(len(fileSystem))); err != nil {
		t.Fatalf("read the file system of %s: %v", windows.UTF16ToString(volume[:]), err)
	}
	return windows.UTF16ToString(fileSystem[:])
}
