//go:build !windows && !darwin

package config_test

import (
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
)

func wantUnixPaths(dataDir, configDir string) config.Paths {
	return config.Paths{
		DataDir:    dataDir,
		ConfigFile: filepath.Join(configDir, "config.json"),
		TokenFile:  filepath.Join(dataDir, "token"),
		SpoolDir:   filepath.Join(dataDir, "spool"),
		DBFile:     filepath.Join(dataDir, "loadline.db"),
	}
}

func TestDefaultPaths_HonorsXDGDirectories(t *testing.T) {
	state := t.TempDir()
	cfg := t.TempDir()
	t.Setenv("LOADLINE_HOME", "")
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", cfg)

	got, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}

	want := wantUnixPaths(filepath.Join(state, "loadline"), filepath.Join(cfg, "loadline"))
	if got != want {
		t.Errorf("DefaultPaths() = %+v, want %+v", got, want)
	}
}

func TestDefaultPaths_FallsBackToHomeWithoutXDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	got, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}

	want := wantUnixPaths(
		filepath.Join(home, ".local", "state", "loadline"),
		filepath.Join(home, ".config", "loadline"),
	)
	if got != want {
		t.Errorf("DefaultPaths() = %+v, want %+v", got, want)
	}
}

func TestDefaultPaths_IgnoresRelativeXDGDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "relative/state")
	t.Setenv("XDG_CONFIG_HOME", "relative/config")

	got, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}

	want := wantUnixPaths(
		filepath.Join(home, ".local", "state", "loadline"),
		filepath.Join(home, ".config", "loadline"),
	)
	if got != want {
		t.Errorf("DefaultPaths() = %+v, want %+v", got, want)
	}
}

func TestDefaultPaths_UnixNeedsHomeWithoutXDG(t *testing.T) {
	t.Setenv("LOADLINE_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	if _, err := config.DefaultPaths(); err == nil {
		t.Error("DefaultPaths succeeded with neither XDG directories nor HOME")
	}
}
