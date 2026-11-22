//go:build !windows

package fsperm_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/fsperm"
)

func TestPrivate_TakesGroupAndOthersOffTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loadline")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "token")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := fsperm.Private(dir); err != nil {
		t.Fatalf("Private: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %#o, want 0700", got)
	}
	info, err = os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("the file inside has mode %#o, want its own 0600 left alone", got)
	}
}

func TestPrivate_MissingDirFails(t *testing.T) {
	err := fsperm.Private(filepath.Join(t.TempDir(), "missing"))

	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Private = %v, want an error that matches fs.ErrNotExist", err)
	}
}

func TestExposure_NamesWhoElseTheModeAdmits(t *testing.T) {
	tests := []struct {
		mode fs.FileMode
		want string
	}{
		{mode: 0o700, want: ""},
		{mode: 0o600, want: ""},
		{mode: 0o750, want: "its mode 0750 also admits group"},
		{mode: 0o701, want: "its mode 0701 also admits others"},
		{mode: 0o755, want: "its mode 0755 also admits group and others"},
		{mode: 0o644, want: "its mode 0644 also admits group and others"},
	}
	for _, tt := range tests {
		path := filepath.Join(t.TempDir(), "loadline")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, tt.mode); err != nil {
			t.Fatal(err)
		}

		got, err := fsperm.Exposure(path)

		if err != nil || got != tt.want {
			t.Errorf("Exposure of mode %#o = %q, %v; want %q", tt.mode, got, err, tt.want)
		}
	}
}

func TestExposure_MissingPathFails(t *testing.T) {
	_, err := fsperm.Exposure(filepath.Join(t.TempDir(), "missing"))

	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Exposure = %v, want an error that matches fs.ErrNotExist", err)
	}
}

func TestMkdirAll_CreatesEachMissingFolderWithMode0700(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "tools", "loadline")

	if err := fsperm.MkdirAll(dir); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	for _, path := range []string{filepath.Join(parent, "tools"), dir} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got&0o077 != 0 {
			t.Errorf("%s has mode %#o, want no permission for group or others", path, got)
		}
	}
}

func TestMovableAncestor_NamesAFolderOthersCanWriteWithoutTheStickyBit(t *testing.T) {
	top := t.TempDir()
	shared := filepath.Join(top, "shared")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		mode fs.FileMode
		want string
	}{
		{mode: 0o777, want: shared + " lets group and others rename the folders in it (mode 0777, without the sticky bit)"},
		{mode: 0o770, want: shared + " lets group rename the folders in it (mode 0770, without the sticky bit)"},
		{mode: 0o703, want: shared + " lets others rename the folders in it (mode 0703, without the sticky bit)"},
		{mode: 0o777 | fs.ModeSticky, want: ""},
		{mode: 0o755, want: ""},
	}
	for _, tt := range tests {
		if err := os.Chmod(shared, tt.mode); err != nil {
			t.Fatal(err)
		}

		got, err := fsperm.MovableAncestor(filepath.Join(shared, "loadline"), top)

		if err != nil || got != tt.want {
			t.Errorf("MovableAncestor under mode %v = %q, %v; want %q", tt.mode, got, err, tt.want)
		}
	}
}

// A symbolic link on the way puts the data directory in the folders that
// hold the link's target, which are judged too.
func TestMovableAncestor_JudgesTheFoldersThatReallyHoldTheDirectory(t *testing.T) {
	top := t.TempDir()
	shared := filepath.Join(top, "shared")
	target := filepath.Join(shared, "real")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(top, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	got, err := fsperm.MovableAncestor(filepath.Join(link, "loadline"), top)

	if want := " lets group and others rename the folders in it"; err != nil || !strings.Contains(got, " is really ") || !strings.Contains(got, want) {
		t.Errorf("MovableAncestor = %q, %v; want it to name the folder that really holds the directory, which%s", got, err, want)
	}
}

func TestIsLink_TellsASymlinkFromAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "token")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bin")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]bool{link: true, file: false, dir: false} {
		got, err := fsperm.IsLink(path)
		if err != nil || got != want {
			t.Errorf("IsLink(%s) = %v, %v; want %v", path, got, err, want)
		}
	}
}

func TestInspect_TellsWhatAPathIs(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "token")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, filepath.Join(dir, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bin")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}

	if got, err := fsperm.Inspect(file); err != nil || got != (fsperm.Entry{Names: 2}) {
		t.Errorf("Inspect(%s) = %+v, %v; want a file of two names", file, got, err)
	}
	if got, err := fsperm.Inspect(link); err != nil || !got.Link {
		t.Errorf("Inspect(%s) = %+v, %v; want a link", link, got, err)
	}
	if got, err := fsperm.Inspect(dir); err != nil || !got.Folder || got.Link {
		t.Errorf("Inspect(%s) = %+v, %v; want a folder", dir, got, err)
	}
}
