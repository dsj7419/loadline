package datadir_test

// A data directory given through a folder's 8.3 short name, as %TEMP% can
// give one: C:\Users\RUNNER~1\... for a user name longer than eight
// characters. The checks judge the folders the short name stands for. Their
// reports spell the data directory as the user gave it, and the folders
// that really hold it as they are on disk, in full.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// shortAlias makes a folder with a long name in parent and returns the
// same folder by its 8.3 short name, joined to parent as given, and by its
// long one. It skips the test on a volume that makes no short names.
func shortAlias(t *testing.T, parent string) (alias, long string) {
	t.Helper()
	long = filepath.Join(parent, "a folder with a long name")
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(long)
	if err != nil {
		t.Fatal(err)
	}
	var buf [windows.MAX_LONG_PATH]uint16
	n, err := windows.GetShortPathName(name, &buf[0], windows.MAX_LONG_PATH)
	if err != nil || n >= windows.MAX_LONG_PATH {
		t.Fatalf("GetShortPathName(%s): %d, %v", long, n, err)
	}
	alias = filepath.Join(parent, filepath.Base(windows.UTF16ToString(buf[:n])))
	if alias == long {
		t.Skipf("the volume of %s makes no 8.3 short names (fsutil 8dot3name query shows it)", parent)
	}
	return alias, long
}

// A short name is no link: nothing about one is refused.
func TestSecure_TakesADataDirectoryGivenThroughAShortName(t *testing.T) {
	top := privateTop(t)
	alias, _ := shortAlias(t, top)
	home := filepath.Join(alias, "loadline")

	if err := datadir.Secure(home, top); err != nil {
		t.Fatalf("Secure(%s): %v", home, err)
	}

	if got, err := datadir.Check(home, top); err != nil || got != "" {
		t.Errorf("Check(%s) = %q, %v; want \"\"", home, got, err)
	}
	if err := datadir.CheckLink(alias); err != nil {
		t.Errorf("CheckLink(%s), a short name: %v; want nil", alias, err)
	}
}

// A folder above that others can empty, given by its short name, is found
// by the names the user gave, and named by them.
func TestVerify_FindsAFolderAboveGivenByItsShortName(t *testing.T) {
	top := privateTop(t)
	alias, _ := shortAlias(t, top)
	fspermtest.Grant(t, alias, 0x40 /* FILE_DELETE_CHILD */, windows.NO_INHERITANCE)
	home := filepath.Join(alias, "loadline")

	err := datadir.Verify(home, top)

	want := alias + " lets " + fspermtest.AuthenticatedUsers(t) + " delete or rename the folders in it"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Verify(%s) = %v; want it refused, starting %q", home, err, want)
	}
}

// A junction below a short name: the report gives the data directory as
// configured, short name and all, and then, after "is really", where it is
// on disk, with the long name the short one stands for.
func TestSecure_ReportsWhereADataDirectoryGivenThroughAShortNameReallyIs(t *testing.T) {
	top := privateTop(t)
	alias, long := shortAlias(t, top)
	holder := filepath.Join(alias, "real")
	if err := os.Mkdir(holder, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.Grant(t, holder, 0x40 /* FILE_DELETE_CHILD */, windows.NO_INHERITANCE)
	target := filepath.Join(holder, "Local")
	profile := filepath.Join(alias, "profile")
	for _, dir := range []string{target, profile} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	local := filepath.Join(profile, "Local")
	junction(t, local, target)
	home := filepath.Join(local, "loadline")

	err := datadir.Secure(home, top)

	if err == nil {
		t.Fatalf("Secure(%s) succeeded, want it refused: %s lets others empty it", home, holder)
	}
	// The long name joined to top as the system resolves top, which on the
	// runner holds a short name of its own.
	onDisk := filepath.Join(finalPath(t, top), filepath.Base(long))
	want := heldRefusal{
		configured: home,
		really:     filepath.Join(onDisk, "real", "Local", "loadline"),
		folder:     filepath.Join(onDisk, "real"),
	}
	assertHeldRefusal(t, "Secure", err.Error(), want)
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := datadir.Check(home, top)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	assertHeldRefusal(t, "Check", got, want)
}

// A link among Loadline's files below a short name is found, and named as
// the data directory was given.
func TestSecure_RefusesALinkInADataDirectoryGivenThroughAShortName(t *testing.T) {
	top := privateTop(t)
	alias, _ := shortAlias(t, top)
	home := filepath.Join(alias, "loadline")
	theirs := filepath.Join(top, "theirs")
	for _, dir := range []string{home, theirs} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(home, "bin")
	junction(t, bin, theirs)

	err := datadir.Secure(home, top)

	want := bin + " is a link"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Secure(%s) = %v; want it refused, starting %q", home, err, want)
	}
	if fspermtest.Protected(t, theirs) {
		t.Errorf("Secure changed the access list of %s, where the link points", theirs)
	}
	if got, checkErr := datadir.Check(home, top); checkErr != nil || !strings.HasPrefix(got, want) {
		t.Errorf("Check = %q, %v; want it to report %q", got, checkErr, want)
	}
	if err := datadir.CheckLink(bin); err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("CheckLink(%s) = %v; want it refused, starting %q", bin, err, want)
	}
}
