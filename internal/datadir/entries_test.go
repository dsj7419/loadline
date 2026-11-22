package datadir_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
