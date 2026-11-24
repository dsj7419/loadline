package datadir_test

// A data directory on a volume that Windows gives no drive letter to, as
// some RAM disks, container mounts and network redirectors are. Windows names
// the folders that really hold it only by the volume's GUID name, which
// Loadline judges no folder by, so it is refused.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/fsperm"
)

func TestSecure_RefusesADataDirectoryOnAVolumeWithoutADriveLetter(t *testing.T) {
	top := noDriveLetterDir(t)
	home := filepath.Join(top, "loadline")

	err := datadir.Secure(home, top)

	want := home + " is on a volume Windows gives no drive letter to, as some RAM disks, container mounts and network redirectors are, " +
		"so Loadline cannot check the folders that hold it: set LOADLINE_HOME to a folder on a disk with a drive letter, such as one in your user profile"
	if err == nil || err.Error() != want || !errors.Is(err, fsperm.ErrNoDriveLetter) {
		t.Errorf("Secure(%s) = %v; want the error %q, which wraps fsperm.ErrNoDriveLetter", home, err, want)
	}
	if _, err := os.Lstat(home); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Secure(%s) made it, or it cannot be read: %v; want it refused before anything is made", home, err)
	}
}

// noDriveLetterDir returns a new folder inside the one
// LOADLINE_TEST_NOLETTER_DIR names, removed when the test ends. It skips the
// test when the variable is unset or empty. Once it is set, whoever set it
// means the test to run there, so a folder that is not reached by a volume's
// GUID name, is missing, is on a volume Windows also names by a drive letter
// or a folder it is mounted in, or cannot hold a new folder fails the test
// rather than letting it pass untested.
func noDriveLetterDir(t *testing.T) string {
	t.Helper()
	root := os.Getenv("LOADLINE_TEST_NOLETTER_DIR")
	if root == "" {
		t.Skip(`LOADLINE_TEST_NOLETTER_DIR is unset; set it to a folder reached as \\?\Volume{…}\ on a volume without a drive letter to run this test`)
	}
	volume := filepath.VolumeName(root)
	if !strings.HasPrefix(volume, `\\?\Volume{`) || !strings.HasSuffix(volume, "}") {
		t.Fatalf(`LOADLINE_TEST_NOLETTER_DIR (%s) is not reached by a volume's GUID name, as \\?\Volume{…}\`, root)
	}
	info, err := os.Stat(root) //nolint:gosec // G703: root is the folder whoever runs the tests sets aside for them
	switch {
	case err != nil:
		t.Fatalf("LOADLINE_TEST_NOLETTER_DIR (%s) names no folder: %v", root, err)
	case !info.IsDir():
		t.Fatalf("LOADLINE_TEST_NOLETTER_DIR (%s) names a file, not a folder", root)
	}
	if names := mountedAt(t, volume+`\`); len(names) > 0 {
		t.Fatalf("LOADLINE_TEST_NOLETTER_DIR (%s) is on a volume Windows also names %s", root, strings.Join(names, " and "))
	}
	dir, err := os.MkdirTemp(root, "home-")
	if err != nil {
		t.Fatalf("make a folder in LOADLINE_TEST_NOLETTER_DIR (%s): %v", root, err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil { //nolint:gosec // G703: dir is the folder made above for this test alone
			t.Errorf("remove %s: %v", dir, err)
		}
	})
	return dir
}

// mountedAt returns the drive letters and the folders that volume, a
// \\?\Volume{…}\ name, is mounted at, each of which gives it a DOS name.
func mountedAt(t *testing.T, volume string) []string {
	t.Helper()
	name, err := windows.UTF16PtrFromString(volume)
	if err != nil {
		t.Fatal(err)
	}
	size := uint32(windows.MAX_PATH)
	for {
		buf := make([]uint16, size)
		var n uint32
		err := windows.GetVolumePathNamesForVolumeName(name, &buf[0], size, &n)
		if err == nil {
			// One name after another, each ended by a null, and the list by another.
			return strings.FieldsFunc(string(utf16.Decode(buf[:n])), func(r rune) bool { return r == 0 })
		}
		if !errors.Is(err, windows.ERROR_MORE_DATA) {
			t.Fatalf("read where %s is mounted: %v", volume, err)
		}
		// Too small: n is the size it needs.
		size = n
	}
}
