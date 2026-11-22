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

	want := home + " is really " + filepath.Join(target, "loadline") + ", and " + holder + " lets " + fspermtest.AuthenticatedUsers(t)
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("Secure = %v; want it refused, starting %q", err, want)
	}
	if _, statErr := os.Stat(home); statErr == nil {
		t.Errorf("Secure created %s although it refused it", home)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, checkErr := datadir.Check(home, top); checkErr != nil || !strings.HasPrefix(got, want) {
		t.Errorf("Check = %q, %v; want doctor to report the same", got, checkErr)
	}
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
