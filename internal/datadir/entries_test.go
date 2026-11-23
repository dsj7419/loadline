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
		"ensure.stamp", "opened.stamp", "config.json",
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
// temporary name in another folder, which no writer of the token makes.
func TestMake_StillRefusesAnotherNameBesideTheTokensTemporaryOne(t *testing.T) {
	for _, tt := range []struct {
		name  string
		names []string
		// another is a file of its own under a temporary name, another
		// writer's, which is no name of the token's and is left alone.
		another bool
	}{
		{name: "a name elsewhere too", names: []string{".token-1234", "elsewhere"}},
		{name: "a name elsewhere, beside another file's temporary name", names: []string{"elsewhere"}, another: true},
		{name: "a temporary name in the spool", names: []string{filepath.Join("spool", ".put-1234.tmp")}},
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

			if want := token + " has another name"; err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("Make = %v, want it refused, starting %q", err, want)
			}
			if _, err := os.Lstat(writing); tt.another && err != nil {
				t.Errorf("Make removed %s, another writer's file (%v)", writing, err)
			}
		})
	}
}
