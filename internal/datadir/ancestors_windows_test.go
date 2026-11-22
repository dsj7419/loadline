package datadir_test

// The folders above the data directory: what lets someone other than the
// user, SYSTEM and Administrators move one aside, and put a folder, and a
// binary, of their own where the hooks look.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// The right to change a folder's permissions (WRITE_DAC) or take it over
// (WRITE_OWNER) lets its holder give themselves DELETE on it, and
// FILE_DELETE_CHILD, and then move it, or what it holds, aside.
func TestVerify_RefusesAFolderAboveOthersCanTakeOver(t *testing.T) {
	for name, mask := range map[string]windows.ACCESS_MASK{
		"WRITE_DAC (change permissions)": windows.WRITE_DAC,
		"WRITE_OWNER (take ownership)":   windows.WRITE_OWNER,
	} {
		for _, depth := range []string{"the folder that holds it", "a folder further up"} {
			t.Run(name+", on "+depth, func(t *testing.T) {
				top := privateTop(t)
				tools := filepath.Join(top, "tools")
				home := filepath.Join(tools, "loadline")
				if depth == "a folder further up" {
					home = filepath.Join(tools, "inner", "loadline")
				}
				if err := os.MkdirAll(home, 0o700); err != nil {
					t.Fatal(err)
				}
				fspermtest.Grant(t, tools, mask, windows.NO_INHERITANCE)
				if others := fspermtest.Others(t, tools, windows.DELETE|0x40); len(others) > 0 {
					t.Fatalf("%s grants DELETE or FILE_DELETE_CHILD to %v already, so the test proves nothing", tools, others)
				}

				err := datadir.Verify(home, top)

				want := tools + " lets " + fspermtest.AuthenticatedUsers(t) + " change its permissions or owner"
				if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), "LOADLINE_HOME") {
					t.Errorf("Verify(%s) = %v; want it refused, starting %q", home, err, want)
				}
				if got, checkErr := datadir.Check(home, top); checkErr != nil || !strings.HasPrefix(got, want) {
					t.Errorf("Check = %q, %v; want doctor to report the same", got, checkErr)
				}
			})
		}
	}
}

// A junction on the path, a relocated AppData or a dotfiles link, puts the
// data directory in the folders that hold the junction's target, which the
// path's own names never show. Here one of them lets every signed-in user
// delete what it holds, so they could move the target aside and put a
// folder of their own there, which the junction, and every hook, then leads
// into.
func TestVerify_ChecksTheFoldersThatReallyHoldTheDataDirectory(t *testing.T) {
	top := privateTop(t)
	holder := filepath.Join(top, "real")
	if err := os.Mkdir(holder, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.Grant(t, holder, 0x40 /* FILE_DELETE_CHILD */, windows.NO_INHERITANCE)
	target := filepath.Join(holder, "Local")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(top, "profile")
	if err := os.Mkdir(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(profile, "Local")
	junction(t, local, target)
	home := filepath.Join(local, "loadline")

	err := datadir.Secure(home, top)

	if err == nil {
		t.Fatalf("Secure(%s) succeeded, want it refused: %s lets others empty it", home, holder)
	}
	want := heldRefusal{
		configured: home,
		really:     filepath.Join(finalPath(t, target), "loadline"),
		folder:     finalPath(t, holder),
	}
	assertHeldRefusal(t, "Secure", err.Error(), want)
	if _, statErr := os.Stat(home); statErr == nil {
		t.Errorf("Secure created %s although it refused it", home)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := datadir.Check(home, top)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	assertHeldRefusal(t, "Check", got, want)
}

// heldRefusal is a refusal of a data directory over one of the folders that
// really hold it, taken apart: the data directory as configured, the path
// the system resolves it to, and the folder above that which others can
// empty.
type heldRefusal struct {
	configured, really, folder string
}

// assertHeldRefusal takes msg, which step reported, apart, and checks each
// part against want, and that it names every signed-in user as able to
// delete or rename the folders in want.folder.
func assertHeldRefusal(t *testing.T, step, msg string, want heldRefusal) {
	t.Helper()
	configured, rest, isReally := strings.Cut(msg, " is really ")
	really, rest, isAnd := strings.Cut(rest, ", and ")
	folder, grant, isLets := strings.Cut(rest, " lets ")
	if !isReally || !isAnd || !isLets {
		t.Fatalf("%s = %q; want \"<data directory> is really <path>, and <folder> lets …\"", step, msg)
	}
	if got := (heldRefusal{configured, really, folder}); got != want {
		t.Errorf("%s = %q,\ntaken apart: %+v;\nwant %+v", step, msg, got, want)
	}
	if right := fspermtest.AuthenticatedUsers(t) + " delete or rename the folders in it"; !strings.HasPrefix(grant, right) {
		t.Errorf("%s says %s lets %q; want %q", step, folder, grant, right)
	}
}

// finalPath returns the path the system opens for path, as the checks of
// the folders above a data directory resolve it: every link and subst drive
// followed and every name in its long form, on its drive letter.
func finalPath(t *testing.T, path string) string {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var buf [windows.MAX_LONG_PATH]uint16
	// FILE_NAME_NORMALIZED and VOLUME_NAME_DOS, both zero, as the checks ask.
	n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], windows.MAX_LONG_PATH, 0)
	if err != nil || n >= windows.MAX_LONG_PATH {
		t.Fatalf("resolve %s: %d, %v", path, n, err)
	}
	return strings.TrimPrefix(windows.UTF16ToString(buf[:n]), `\\?\`)
}

// A \\?\ path names the same folders as the plain one, and is judged the
// same, with top in either form.
func TestVerify_JudgesAnExtendedLengthPathAsThePlainOne(t *testing.T) {
	top := driveRoot(t)
	tools := filepath.Join(top, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tools, "loadline")
	if datadir.Verify(home, top) == nil {
		t.Fatal("the plain path passes, so the test proves nothing")
	}

	for _, tt := range []struct{ home, top string }{{`\\?\` + home, `\\?\` + top}, {`\\?\` + home, top}, {home, `\\?\` + top}} {
		if err := datadir.Verify(tt.home, tt.top); err == nil || !strings.Contains(err.Error(), tools) {
			t.Errorf("Verify(%s, %s) = %v; want it refused, naming %s", tt.home, tt.top, err, tools)
		}
	}
}
