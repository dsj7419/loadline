package install_test

// Attacks on the data directory from the security re-check: the folders
// above it, the moment it is created, and what an existing one holds. Each
// builds the folder LOADLINE_HOME would sit in from a temporary directory
// that, like the root of a second drive, lets every signed-in user modify
// what is created below it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
	"github.com/dsj7419/loadline/internal/install"
)

// permissiveParent is a temporary folder that lets every signed-in user
// modify what is created below it.
func permissiveParent(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	fspermtest.Permissive(t, parent)
	return parent
}

func testExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// junction makes link a directory junction to target, as any user may in a
// folder they can create directories in.
func junction(t *testing.T, link, target string) {
	t.Helper()
	//nolint:gosec // G204: the paths are the test's own temporary folders
	if out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
}

// openStill reports whether dir still takes the access list its parent
// passes down, that is, nothing has made it private yet.
func openStill(dir string) bool {
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	control, _, err := sd.Control()
	return err == nil && control&windows.SE_DACL_PROTECTED == 0
}

// With LOADLINE_HOME two levels below a folder that lets everyone in, the
// folder between them, which Loadline creates on the way, would inherit
// "Authenticated Users: Modify", which includes DELETE: anyone could move it
// aside and put a bin\loadline.exe of their own at the path every hook runs.
func TestSelfInstall_NoFolderLoadlineCreatesOnTheWayCanBeMovedAside(t *testing.T) {
	parent := permissiveParent(t)
	home := filepath.Join(parent, "tools", "loadline")

	if _, err := config.EnsureToken(filepath.Join(home, "token")); err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}
	exe, err := install.SelfInstall(testExe(t), home)
	if err != nil {
		t.Fatalf("SelfInstall: %v", err)
	}

	tools := filepath.Join(parent, "tools")
	if others := fspermtest.Others(t, tools, windows.DELETE); len(others) > 0 {
		t.Errorf("%s, which Loadline created above %s, can be deleted or moved aside by %s", tools, exe, strings.Join(others, ", "))
	}
}

// How often a watcher finds the new data directory before it is private.
func TestEnsureToken_TheDataDirectoryIsNeverSeenOpen(t *testing.T) {
	parent := permissiveParent(t)
	const rounds = 200
	seen, open := 0, 0
	for i := range rounds {
		home := filepath.Join(parent, "home"+strconv.Itoa(i))
		result := make(chan int, 1)
		go func() {
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
				if _, err := os.Stat(home); err == nil {
					if openStill(home) {
						result <- 2
					} else {
						result <- 1
					}
					return
				}
			}
			result <- 0
		}()
		if _, err := config.EnsureToken(filepath.Join(home, "token")); err != nil {
			t.Fatalf("EnsureToken: %v", err)
		}
		switch <-result {
		case 2:
			open++
			seen++
		case 1:
			seen++
		}
	}
	t.Logf("%d of %d rounds saw the new data directory; %d of them before it was private", seen, rounds, open)
	if open > 0 {
		t.Errorf("the new data directory was open to everyone its parent admits in %d of %d rounds", open, rounds)
	}
}

// What a watcher can plant while the new data directory is still open: a
// token it knows, which EnsureToken would hand out, and a bin junction to a
// folder of its own, into which install would write the binary every hook
// runs. It plants only while the directory is still open, as another user
// could.
func TestEnsureToken_NothingCanBePlantedBeforeTheDataDirectoryIsPrivate(t *testing.T) {
	parent := permissiveParent(t)
	theirs := filepath.Join(permissiveParent(t), "theirs")
	if err := os.MkdirAll(theirs, 0o700); err != nil {
		t.Fatal(err)
	}
	planted := strings.Repeat("0f", 32)
	const rounds = 30
	var openWhenPlanted, tokenTaken int
	for i := range rounds {
		home := filepath.Join(parent, "home"+strconv.Itoa(i))
		prepared := filepath.Join(parent, "prep"+strconv.Itoa(i))
		junction(t, prepared, theirs)

		result := make(chan bool, 1)
		go func() {
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
				if _, err := os.Stat(home); err == nil {
					break
				}
			}
			if !openStill(home) {
				result <- false
				return
			}
			if f, err := os.OpenFile(filepath.Join(home, "token"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
				_, _ = f.WriteString(planted)
				_ = f.Close()
			}
			_ = os.Rename(prepared, filepath.Join(home, "bin"))
			result <- openStill(home)
		}()
		token, err := config.EnsureToken(filepath.Join(home, "token"))
		if err != nil {
			t.Fatalf("EnsureToken: %v", err)
		}
		if !<-result {
			continue
		}
		openWhenPlanted++
		if string(token) == planted {
			tokenTaken++
		}
	}
	if openWhenPlanted > 0 {
		t.Errorf("entries were created in the new data directory before it was private in %d of %d rounds "+
			"(token taken over %d times)", openWhenPlanted, rounds, tokenTaken)
	}
}

// A bin that is a junction, as one left in a data directory that was open
// before: SelfInstall must not write the binary every hook runs through it,
// into a folder outside the data directory with an access list of its own.
func TestSelfInstall_RefusesABinJunction(t *testing.T) {
	home := filepath.Join(permissiveParent(t), "loadline")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(permissiveParent(t), "theirs")
	if err := os.MkdirAll(theirs, 0o700); err != nil {
		t.Fatal(err)
	}
	junction(t, filepath.Join(home, "bin"), theirs)

	_, err := install.SelfInstall(testExe(t), home)

	if err == nil || !strings.Contains(err.Error(), "is a link") {
		t.Errorf("SelfInstall = %v, want it refused: bin is a link", err)
	}
	if entries, _ := os.ReadDir(theirs); len(entries) > 0 {
		t.Errorf("SelfInstall wrote %d entries into %s, where the bin junction points", len(entries), theirs)
	}
}
