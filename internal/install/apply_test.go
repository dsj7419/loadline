package install_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/install"
)

var applyTime = time.Date(2026, 11, 21, 9, 30, 5, 0, time.UTC)

const backupSuffix = ".loadline-20261121-093005.bak"

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// dirEntries lists the names in dir, sorted.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

func TestApply_BackupAndAtomicRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := readFile(t, "testdata/settings_owner.in.json")
	out := readFile(t, "testdata/settings_owner.out.json")
	writeFile(t, path, original)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
	}

	backup, err := install.Apply(path, original, out, applyTime)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if want := path + backupSuffix; backup != want {
		t.Errorf("backup = %s, want %s", backup, want)
	}
	if got := readFile(t, backup); !bytes.Equal(got, original) {
		t.Error("the backup does not hold the original bytes")
	}
	if got := readFile(t, path); !bytes.Equal(got, out) {
		t.Error("the settings file does not hold the new bytes")
	}
	if got, want := dirEntries(t, dir), []string{"settings.json", "settings.json" + backupSuffix}; !slices.Equal(got, want) {
		t.Errorf("dir holds %q, want %q and no temporary files", got, want)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{path, backup} {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o640 {
				t.Errorf("%s has mode %o, want the original 640", p, got)
			}
		}
	}
}

func TestApply_RefusesWhenFileChangedSincePlan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := readFile(t, "testdata/settings_owner.in.json")
	out := readFile(t, "testdata/settings_owner.out.json")
	edited := bytes.Replace(original, []byte(`"opus"`), []byte(`"sonnet"`), 1)
	writeFile(t, path, edited)

	backup, err := install.Apply(path, original, out, applyTime)

	if !errors.Is(err, install.ErrChangedSincePlan) {
		t.Fatalf("err = %v, want ErrChangedSincePlan", err)
	}
	if backup != "" {
		t.Errorf("backup = %q, want none", backup)
	}
	if got := readFile(t, path); !bytes.Equal(got, edited) {
		t.Error("Apply overwrote the newer edit")
	}
	if got := dirEntries(t, dir); !slices.Equal(got, []string{"settings.json"}) {
		t.Errorf("dir holds %q, want only settings.json", got)
	}
}

func TestApply_CreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	out := readFile(t, "testdata/settings_empty.out.json")

	backup, err := install.Apply(path, nil, out, applyTime)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if backup != "" {
		t.Errorf("backup = %q, want none for a new file", backup)
	}
	if got := readFile(t, path); !bytes.Equal(got, out) {
		t.Error("the new settings file does not hold the planned bytes")
	}
	if got := dirEntries(t, dir); !slices.Equal(got, []string{"settings.json"}) {
		t.Errorf("dir holds %q, want only settings.json", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("new file has mode %o, want 600 because it holds the token", got)
		}
	}
}

func TestApply_RefusesWhenMissingFileAppeared(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeFile(t, path, []byte(`{"model": "opus"}`))

	_, err := install.Apply(path, nil, []byte(`{}`), applyTime)

	if !errors.Is(err, install.ErrChangedSincePlan) {
		t.Fatalf("err = %v, want ErrChangedSincePlan", err)
	}
	if got := readFile(t, path); string(got) != `{"model": "opus"}` {
		t.Errorf("Apply overwrote a file that appeared after the plan: %s", got)
	}
}

func TestApply_RefusesMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "settings.json")

	if _, err := install.Apply(path, nil, []byte(`{}`), applyTime); err == nil {
		t.Error("Apply created a settings file in a directory that does not exist")
	}
}

func TestApply_KeepsEarlierBackupFromTheSameSecond(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	first := []byte(`{"model": "opus"}`)
	second := []byte(`{"model": "sonnet"}`)
	writeFile(t, path, first)

	firstBackup, err := install.Apply(path, first, second, applyTime)
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	secondBackup, err := install.Apply(path, second, first, applyTime)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	if firstBackup == secondBackup {
		t.Fatalf("both applies wrote the backup %s", firstBackup)
	}
	if got := readFile(t, firstBackup); !bytes.Equal(got, first) {
		t.Errorf("first backup holds %s, want %s", got, first)
	}
	if got := readFile(t, secondBackup); !bytes.Equal(got, second) {
		t.Errorf("second backup holds %s, want %s", got, second)
	}
}

func TestApply_WritesThroughASymlink(t *testing.T) {
	dotfiles := t.TempDir()
	target := filepath.Join(dotfiles, "settings.json")
	link := filepath.Join(t.TempDir(), "settings.json")
	original := []byte(`{"model": "opus"}`)
	writeFile(t, target, original)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	if _, err := install.Apply(link, original, []byte(`{}`), applyTime); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("Apply replaced the symlink with a regular file")
	}
	if got := readFile(t, target); string(got) != `{}` {
		t.Errorf("link target holds %s, want the new settings", got)
	}
	if got := dirEntries(t, dotfiles); !slices.Equal(got, []string{"settings.json"}) {
		t.Errorf("link target's dir holds %q, want only settings.json", got)
	}
}
