package datadir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/fsperm"
)

// ownedByStranger makes path, for the rest of the test, read as owned by
// someone else: an account that is not elevated, or not root, cannot give a
// file another owner.
func ownedByStranger(t *testing.T, path string) {
	t.Helper()
	previous := foreignOwner
	foreignOwner = func(p string) (fsperm.Owner, error) {
		if p == path {
			return fsperm.Owner{Name: "a stranger"}, nil
		}
		return previous(p)
	}
	t.Cleanup(func() { foreignOwner = previous })
}

// home returns a data directory that holds the token, a spooled event, the
// installed binary and, being written, a token's temporary file.
func home(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "loadline")
	for _, name := range []string{"token", filepath.Join("spool", "1-0000000a.json"), filepath.Join("bin", "loadline"), ".token-1234"} {
		path := filepath.Join(dir, name)
		if err := fsperm.MkdirAll(filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Whoever owns one of Loadline's files or folders may always let anyone at
// it, whatever Loadline makes of its access list. Install, the server and
// the creation of the token refuse it, and doctor reports it.
func TestMake_RefusesAFileOfLoadlinesThatBelongsToSomeoneElse(t *testing.T) {
	for _, name := range []string{"token", filepath.Join("spool", "1-0000000a.json"), "bin"} {
		t.Run(name, func(t *testing.T) {
			dir := home(t)
			path := filepath.Join(dir, name)
			ownedByStranger(t, path)
			want := path + " belongs to a stranger"

			for step, err := range map[string]error{"Make": Make(dir), "Secure": Secure(dir, filepath.Dir(dir))} {
				if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.HasSuffix(err.Error(), "run loadline install") {
					t.Errorf("%s = %v; want it refused, starting %q", step, err, want)
				}
			}
			if got, err := Check(dir, filepath.Dir(dir)); err != nil || !strings.HasPrefix(got, want) {
				t.Errorf("Check = %q, %v; want it to report %q", got, err, want)
			}
		})
	}
}

// A file another process is writing is its own, whoever owns it.
func TestMake_DoesNotJudgeTheOwnerOfAFileBeingWritten(t *testing.T) {
	dir := home(t)
	ownedByStranger(t, filepath.Join(dir, ".token-1234"))

	if err := Make(dir); err != nil {
		t.Errorf("Make = %v, want nil", err)
	}
}

// A file the system leaves, in the data directory, is judged by its owner
// as Loadline's own are.
func TestMake_RefusesASystemFileThatBelongsToSomeoneElse(t *testing.T) {
	dir := home(t)
	ini := filepath.Join(dir, "desktop.ini")
	if err := os.WriteFile(ini, []byte("[.ShellClassInfo]\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ownedByStranger(t, ini)
	want := ini + " belongs to a stranger"

	if err := Make(dir); err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Make = %v; want it refused, starting %q", err, want)
	}
	if got, err := Check(dir, filepath.Dir(dir)); err != nil || !strings.HasPrefix(got, want) {
		t.Errorf("Check = %q, %v; want it to report %q", got, err, want)
	}
}
