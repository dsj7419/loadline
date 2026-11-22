package datadir_test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// The tests take the temporary folder they build in, with every folder above
// it, for a drive root. It is made private first, so that what the machine's
// own temporary folder passes down, which may let other accounts in, is no
// test's subject, and then, like the root of a second drive, lets every
// signed-in user modify what is created below it.
func driveRoot(t *testing.T) string {
	t.Helper()
	top := t.TempDir()
	if err := fsperm.Private(top); err != nil {
		t.Fatal(err)
	}
	fspermtest.Permissive(t, top)
	return top
}

func TestSecure_RefusesADataDirectoryOthersCanMoveAside(t *testing.T) {
	top := driveRoot(t)
	tools := filepath.Join(top, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tools, "loadline")

	err := datadir.Secure(home, top)

	if err == nil {
		t.Fatalf("Secure(%s) succeeded, want it refused: %s lets others move it aside", home, tools)
	}
	for _, want := range []string{
		tools + " lets ", fspermtest.AuthenticatedUsers(t), "delete or rename it",
		"replace the binary every hook runs", "LOADLINE_HOME", "user profile",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Secure = %v, want it to say %q", err, want)
		}
	}
	if _, statErr := os.Stat(home); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("Secure created %s although it refused it", home)
	}
}

func TestSecure_CreatesTheDataDirectoryPrivate(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")

	if err := datadir.Secure(home, top); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	if !fspermtest.Protected(t, home) || len(fspermtest.Others(t, home, fspermtest.Any)) > 0 {
		t.Errorf("%s admits %v, want only the user, SYSTEM and Administrators", home, fspermtest.Others(t, home, fspermtest.Any))
	}
}

func TestSecure_MakesAnExistingDataDirectoryPrivate(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := datadir.Secure(home, top); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	if others := fspermtest.Others(t, home, fspermtest.Any); len(others) > 0 {
		t.Errorf("%s admits %v, want only the user, SYSTEM and Administrators", home, others)
	}
}

// Doctor says what install and the server would refuse, in the same words.
func TestCheck_ReportsWhatSecureRefuses(t *testing.T) {
	top := driveRoot(t)
	tools := filepath.Join(top, "tools")
	home := filepath.Join(tools, "loadline")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	refused := datadir.Secure(home, top)
	if refused == nil {
		t.Fatal("Secure succeeded, want it refused")
	}

	got, err := datadir.Check(home, top)

	if err != nil || got != refused.Error() {
		t.Errorf("Check = %q, %v; want %q", got, err, refused.Error())
	}
}

func TestCheck_ReportsADataDirectoryOthersCanGetInto(t *testing.T) {
	home := filepath.Join(driveRoot(t), "loadline")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := datadir.Check(home, filepath.Dir(home))

	if err != nil || !strings.HasPrefix(got, home+" is not private: its access list also admits ") ||
		!strings.HasSuffix(got, "; run loadline install") {
		t.Errorf("Check = %q, %v; want it to say %s is not private and that install fixes it", got, err, home)
	}
}

func TestCheck_APrivateDataDirectoryPasses(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")
	if err := datadir.Secure(home, top); err != nil {
		t.Fatal(err)
	}

	if got, err := datadir.Check(home, top); err != nil || got != "" {
		t.Errorf("Check = %q, %v; want \"\"", got, err)
	}
}

func TestCheck_AMissingDataDirectoryIsAnError(t *testing.T) {
	_, err := datadir.Check(filepath.Join(t.TempDir(), "missing"), "")

	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Check = %v, want an error that matches fs.ErrNotExist", err)
	}
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

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A data directory that was open before, or one whose files someone gave
// access to: making the directory private keeps what a file was given
// explicitly, so Secure takes it off Loadline's files.
func TestSecure_RemovesExplicitEntriesFromLoadlinesFiles(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")
	token, event := filepath.Join(home, "token"), filepath.Join(home, "spool", "event.json")
	writeFile(t, token)
	writeFile(t, event)
	fspermtest.Grant(t, token, fspermtest.Read, windows.NO_INHERITANCE)
	fspermtest.Grant(t, event, fspermtest.Read, windows.NO_INHERITANCE)

	if err := datadir.Secure(home, top); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	for _, path := range []string{token, event, filepath.Dir(event)} {
		if others := fspermtest.Others(t, path, fspermtest.Any); len(others) > 0 {
			t.Errorf("%s admits %s after Secure, want only the user, SYSTEM and Administrators", path, strings.Join(others, ", "))
		}
	}
	if data, err := os.ReadFile(token); err != nil || len(data) != 64 {
		t.Errorf("read the token back: %q, %v", data, err)
	}
}

// Doctor reports a file of Loadline's that someone gave access to, which
// making the directory private does not take away.
func TestCheck_ReportsAFileOfLoadlinesOthersCanGetInto(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")
	if err := datadir.Secure(home, top); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(home, "token")
	writeFile(t, token)
	fspermtest.Grant(t, token, fspermtest.Read, windows.NO_INHERITANCE)

	got, err := datadir.Check(home, top)

	if err != nil || !strings.HasPrefix(got, token+" is not private: its access list also admits ") ||
		!strings.HasSuffix(got, "; run loadline install") {
		t.Errorf("Check = %q, %v; want it to say %s is not private and that install fixes it", got, err, token)
	}
}

// A bin that is a junction to a folder outside, as one left in a data
// directory that was open before: making it private would set the list on
// the junction, and the binary every hook runs would go into the folder it
// points to.
func TestSecure_RefusesALinkAmongLoadlinesFiles(t *testing.T) {
	top := driveRoot(t)
	home := filepath.Join(top, "loadline")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(top, "theirs")
	if err := os.Mkdir(theirs, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	junction(t, bin, theirs)

	err := datadir.Secure(home, top)

	if err == nil || !strings.Contains(err.Error(), bin+" is a link") {
		t.Fatalf("Secure = %v, want it refused, naming %s as a link", err, bin)
	}
	if fspermtest.Protected(t, theirs) {
		t.Errorf("Secure changed the access list of %s, where the link points", theirs)
	}
	if got, checkErr := datadir.Check(home, top); checkErr != nil || got != err.Error() {
		t.Errorf("Check = %q, %v; want what Secure refused with: %q", got, checkErr, err.Error())
	}
}
