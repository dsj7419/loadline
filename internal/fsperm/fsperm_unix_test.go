//go:build !windows

package fsperm_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
