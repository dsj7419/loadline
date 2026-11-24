//go:build darwin

package config_test

import (
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
)

func TestDefaultPaths_Darwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", "")
	t.Setenv("HOME", home)

	got, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}

	dir := filepath.Join(home, "Library", "Application Support", "loadline")
	want := config.Paths{
		DataDir:           dir,
		ConfigFile:        filepath.Join(dir, "config.json"),
		TokenFile:         filepath.Join(dir, "token"),
		SpoolDir:          filepath.Join(dir, "spool"),
		DBFile:            filepath.Join(dir, "loadline.db"),
		BrowserProfileDir: filepath.Join(dir, "browser"),
		ServerLock:        filepath.Join(dir, "server.lock"),
	}
	if got != want {
		t.Errorf("DefaultPaths() = %+v, want %+v", got, want)
	}
}

func TestDefaultPaths_DarwinNeedsHome(t *testing.T) {
	t.Setenv("LOADLINE_HOME", "")
	t.Setenv("HOME", "")

	if _, err := config.DefaultPaths(); err == nil {
		t.Error("DefaultPaths succeeded with HOME unset")
	}
}
