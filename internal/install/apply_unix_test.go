//go:build unix

package install_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/install"
)

// The owner's own bits are theirs to keep: a settings file made read-only, or
// executable, stays so when Apply replaces it, and its backup matches. The
// new file starts as a temporary one with 0600, so it gets them only because
// Apply sets them.
func TestApply_KeepsTheOwnersOwnPermissions(t *testing.T) {
	for _, mode := range []fs.FileMode{0o400, 0o700} {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings.json")
		original := []byte(`{"model": "opus"}`)
		writeFile(t, path, original)
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}

		backup, err := install.Apply(path, original, []byte(`{}`), applyTime)
		if err != nil {
			t.Fatalf("mode %o: Apply: %v", mode, err)
		}

		for _, p := range []string{path, backup} {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != mode {
				t.Errorf("%s has mode %o, want the owner's %o kept", filepath.Base(p), got, mode)
			}
		}
	}
}

// The ingest token must be readable only by the user. Install writes it into
// settings.json, and uninstall backs up the file that holds it, so neither
// may stay readable by group or others, even when the original file was.
func TestApply_KeepsTheTokenPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := []byte("{\n  \"model\": \"opus\"\n}\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil { // the mode a default umask gives
		t.Fatal(err)
	}
	o := install.Options{
		Exe:   "/home/u/.local/state/loadline/bin/loadline",
		Port:  8289,
		Token: strings.Repeat("ab", 32),
		Shell: install.ShellBash,
	}
	installed, _, _, err := install.Plan(original, o)
	if err != nil {
		t.Fatal(err)
	}
	reverted, _, err := install.Revert(installed, o.Exe)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	if _, err := install.Apply(path, original, installed, now); err != nil {
		t.Fatal(err)
	}
	assertPrivate(t, path)
	backup, err := install.Apply(path, installed, reverted, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	assertPrivate(t, backup)
	if _, err := install.Apply(path, reverted, installed, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	assertPrivate(t, path)
}

func assertPrivate(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("%s holds the ingest token with mode %o, readable by other users", path, perm)
	}
}
