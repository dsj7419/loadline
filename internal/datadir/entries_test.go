package datadir_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/datadir"
)

func TestOwns_KnowsLoadlinesEntriesOnly(t *testing.T) {
	for _, name := range []string{
		"token", ".token-123456", "bin", "spool", "loadline.log", "loadline.log.1", "loadline.log.12",
		"loadline.db", "loadline.db-wal", "loadline.db-shm", "loadline.db-journal",
		"ensure.stamp", "opened.stamp", "config.json", "server.lock",
	} {
		if !datadir.Owns(name) {
			t.Errorf("Owns(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"shared", "notes.txt", "tokens", "token.bak", "loadline.log.x", "loadline.log.", "loadline.log.-1",
		"loadline.db.bak", "Bin", ".git",
	} {
		if datadir.Owns(name) {
			t.Errorf("Owns(%q) = true, want false", name)
		}
	}
}

func TestTemporary_KnowsTheFilesLoadlineWritesBeforePuttingThemInPlace(t *testing.T) {
	for _, name := range []string{".token-123456", ".put-123456.tmp", ".loadline-123456.tmp"} {
		if !datadir.Temporary(name) {
			t.Errorf("Temporary(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"token", ".token", "token-123456", ".put-123456.json", "put-123456.tmp", ".loadline-123456.exe",
		"loadline.exe", "loadline.exe.old", "1-0000000a.json", "loadline.log.1", ".DS_Store",
	} {
		if datadir.Temporary(name) {
			t.Errorf("Temporary(%q) = true, want false", name)
		}
	}
}

func TestSecure_RefusesAFolderThatHoldsOtherThings(t *testing.T) {
	home := filepath.Join(t.TempDir(), "tools")
	notes := filepath.Join(home, "shared", "notes.txt")
	if err := os.MkdirAll(filepath.Dir(notes), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notes, []byte("for everyone"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := datadir.Secure(home, filepath.Dir(home))

	want := home + " holds shared, which is not Loadline's"
	if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), "LOADLINE_HOME must be a folder of Loadline's own") {
		t.Fatalf("Secure = %v, want it refused, starting %q", err, want)
	}
	if got, checkErr := datadir.Check(home, filepath.Dir(home)); checkErr != nil || got != err.Error() {
		t.Errorf("Check = %q, %v; want what Secure refused with: %q", got, checkErr, err.Error())
	}
}

// What the system leaves in a folder its user has looked at does not make
// the folder anyone else's.
func TestSecure_TakesAFolderWithTheSystemsOwnFiles(t *testing.T) {
	home := filepath.Join(t.TempDir(), "loadline")
	for _, name := range []string{"token", ".DS_Store", "desktop.ini", "Thumbs.db"} {
		path := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := datadir.Secure(home, filepath.Dir(home)); err != nil {
		t.Errorf("Secure = %v, want it to take the folder", err)
	}
}

// The creation of the token prepares its folder without judging the folders
// above it, and refuses one that is not Loadline's just the same.
func TestMake_RefusesAFolderThatHoldsOtherThings(t *testing.T) {
	home := filepath.Join(t.TempDir(), "tools")
	if err := os.MkdirAll(filepath.Join(home, "shared"), 0o700); err != nil {
		t.Fatal(err)
	}

	err := datadir.Make(home)

	if err == nil || !strings.HasPrefix(err.Error(), home+" holds shared, which is not Loadline's") {
		t.Errorf("Make = %v, want it refused, naming shared", err)
	}
}

// What the system leaves in a folder is a file. A folder or a link by one of
// those names is someone else's, and the data directory that holds it is
// not Loadline's own.
func TestMake_RefusesAFolderNamedLikeTheSystemsOwnFiles(t *testing.T) {
	for _, name := range []string{".DS_Store", "desktop.ini", "Thumbs.db"} {
		t.Run(name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "loadline")
			if err := os.MkdirAll(filepath.Join(home, name), 0o700); err != nil {
				t.Fatal(err)
			}

			err := datadir.Make(home)

			if err == nil || !strings.HasPrefix(err.Error(), home+" holds "+name+", which is not Loadline's") {
				t.Errorf("Make = %v, want it refused, naming %s", err, name)
			}
		})
	}
}

// The token has two names for a moment while another caller puts it in
// place: its temporary name goes once the link is made. Make waits that out
// rather than take the token for a file that also lives elsewhere.
func TestMake_WaitsForTheTokensTemporaryNameToGo(t *testing.T) {
	home := filepath.Join(t.TempDir(), "loadline")
	token := filepath.Join(home, "token")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(home, ".token-1234")
	if err := os.Link(token, temporary); err != nil {
		t.Skipf("cannot make a hard link here: %v", err)
	}
	time.AfterFunc(20*time.Millisecond, func() { _ = os.Remove(temporary) })

	if err := datadir.Make(home); err != nil {
		t.Errorf("Make = %v, want it to wait for %s to go", err, temporary)
	}
}

// Only the file's own temporary name in its own folder stands beside it. A
// name elsewhere is refused even with such a name there too, and so is a
// temporary name in another folder, which no writer of the token makes. The
// walk refuses the first of the file's names it comes to: a temporary name
// comes before the token in its folder, and the spool before the token.
func TestMake_StillRefusesAnotherNameBesideTheTokensTemporaryOne(t *testing.T) {
	for _, tt := range []struct {
		name  string
		names []string
		// another is a file of its own under a temporary name, another
		// writer's, which is no name of the token's and is left alone.
		another bool
		// refused is the name the walk refuses, in the data directory.
		refused string
	}{
		{name: "a name elsewhere too", names: []string{".token-1234", "elsewhere"}, refused: ".token-1234"},
		{name: "a name elsewhere, beside another file's temporary name", names: []string{"elsewhere"}, another: true, refused: "token"},
		{
			name: "a temporary name in the spool", names: []string{filepath.Join("spool", ".put-1234.tmp")},
			refused: filepath.Join("spool", ".put-1234.tmp"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "loadline")
			token := filepath.Join(home, "token")
			if err := os.MkdirAll(filepath.Join(home, "spool"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(token, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, name := range tt.names {
				other := filepath.Join(home, name)
				if name == "elsewhere" {
					other = filepath.Join(t.TempDir(), "shared.txt")
				}
				if err := os.Link(token, other); err != nil {
					t.Skipf("cannot make a hard link here: %v", err)
				}
			}
			writing := filepath.Join(home, ".token-5678")
			if tt.another {
				if err := os.WriteFile(writing, []byte(strings.Repeat("cd", 32)), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			err := datadir.Make(home)

			if want := filepath.Join(home, tt.refused) + " has another name"; err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("Make = %v, want it refused, starting %q", err, want)
			}
			if _, err := os.Lstat(writing); tt.another && err != nil {
				t.Errorf("Make removed %s, another writer's file (%v)", writing, err)
			}
		})
	}
}

// newHome returns a data directory below top, not made private yet, that
// holds the token and the folders Loadline makes for the spool and the
// binary.
func newHome(t *testing.T) (home, top string) {
	t.Helper()
	top = t.TempDir()
	home = filepath.Join(top, "loadline")
	for _, folder := range []string{"spool", "bin"} {
		if err := os.MkdirAll(filepath.Join(home, folder), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "token"), []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, top
}

// A file elsewhere, linked into the data directory under one of the names
// Loadline writes a file under before it puts it in place, is that file:
// making its folder private, or passing the data directory's list down to
// it, would change who may use it there. Each step that takes the data
// directory refuses it as it refuses any other file of Loadline's with a
// name elsewhere, and the file there stays as it is.
func TestMake_RefusesAHardLinkUnderATemporaryName(t *testing.T) {
	for _, name := range []string{".token-1", filepath.Join("spool", ".put-1.tmp"), filepath.Join("bin", ".loadline-1.tmp")} {
		t.Run(filepath.Base(name), func(t *testing.T) {
			home, top := newHome(t)
			shared, unchanged := sharedFile(t)
			planted := filepath.Join(home, name)
			if err := os.Link(shared, planted); err != nil {
				t.Skipf("cannot make a hard link here: %v", err)
			}
			want := planted + " has another name"

			for step, err := range map[string]error{"Make": datadir.Make(home), "Verify": datadir.Verify(home, top)} {
				if err == nil || !strings.HasPrefix(err.Error(), want) {
					t.Errorf("%s = %v, want it refused, starting %q", step, err, want)
				}
			}
			if got, err := datadir.Check(home, top); err != nil || !strings.HasPrefix(got, want) {
				t.Errorf("Check = %q, %v; want it to report %q", got, err, want)
			}
			unchanged(t)
		})
	}
}

// While a caller links the token into place, and after, when it was ended
// before it removed the temporary name, the token has two names side by side
// in the data directory: its own and the temporary one it was written
// under. That temporary name is the token's, no file elsewhere, and each
// step takes the data directory.
func TestMake_LeavesATokenBeingLinkedIntoPlace(t *testing.T) {
	home, top := newHome(t)
	token := filepath.Join(home, "token")
	temporary := filepath.Join(home, ".token-1")
	if err := os.Link(token, temporary); err != nil {
		t.Skipf("cannot make a hard link here: %v", err)
	}

	// Verify changes nothing, so the temporary name is still there for Make.
	if err := datadir.Verify(home, top); err != nil {
		t.Errorf("Verify = %v, want the data directory taken", err)
	}
	if _, err := os.Lstat(temporary); err != nil {
		t.Errorf("after Verify, %s is gone (%v); want it left as it was", temporary, err)
	}
	if err := datadir.Make(home); err != nil {
		t.Errorf("Make = %v, want the data directory taken", err)
	}
	if got, err := datadir.Check(home, top); err != nil || got != "" {
		t.Errorf("Check = %q, %v; want \"\"", got, err)
	}
	if data, err := os.ReadFile(token); err != nil || string(data) != strings.Repeat("ab", 32) {
		t.Errorf("the token reads %q, %v; want it as it was", data, err)
	}
}
