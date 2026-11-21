//go:build windows

package config_test

import (
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
)

func TestDefaultPaths_Windows(t *testing.T) {
	local := filepath.Join(t.TempDir(), "Local")
	roaming := filepath.Join(t.TempDir(), "Roaming")
	t.Setenv("LOADLINE_HOME", "")
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", roaming)

	got, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}

	dataDir := filepath.Join(local, "loadline")
	want := config.Paths{
		DataDir:    dataDir,
		ConfigFile: filepath.Join(roaming, "loadline", "config.json"),
		TokenFile:  filepath.Join(dataDir, "token"),
		SpoolDir:   filepath.Join(dataDir, "spool"),
		DBFile:     filepath.Join(dataDir, "loadline.db"),
	}
	if got != want {
		t.Errorf("DefaultPaths() = %+v, want %+v", got, want)
	}
}

func TestDefaultPaths_WindowsNeedsAppDataVariables(t *testing.T) {
	for _, missing := range []string{"LOCALAPPDATA", "APPDATA"} {
		t.Run(missing, func(t *testing.T) {
			t.Setenv("LOADLINE_HOME", "")
			t.Setenv("LOCALAPPDATA", t.TempDir())
			t.Setenv("APPDATA", t.TempDir())
			t.Setenv(missing, "")

			if _, err := config.DefaultPaths(); err == nil {
				t.Errorf("DefaultPaths succeeded with %s unset", missing)
			}
		})
	}
}
