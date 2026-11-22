package datadir_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
