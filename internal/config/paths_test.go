package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/domain"
)

func TestDefaultPaths_LoadlineHomeHoldsEverything(t *testing.T) {
	home := filepath.Join(t.TempDir(), "loadline-home")
	t.Setenv("LOADLINE_HOME", home)

	got, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}

	want := config.Paths{
		DataDir:           home,
		ConfigFile:        filepath.Join(home, "config.json"),
		TokenFile:         filepath.Join(home, "token"),
		SpoolDir:          filepath.Join(home, "spool"),
		DBFile:            filepath.Join(home, "loadline.db"),
		BrowserProfileDir: filepath.Join(home, "browser"),
		ServerLock:        filepath.Join(home, "server.lock"),
	}
	if got != want {
		t.Errorf("DefaultPaths() = %+v, want %+v", got, want)
	}
}

// The dashboard's browser profile is private only because the data
// directory is, wherever that is.
func TestPaths_KeepTheBrowserProfileInTheDataDirectory(t *testing.T) {
	for name, home := range map[string]string{
		"the platform's location": "",
		"LOADLINE_HOME":           filepath.Join(t.TempDir(), "loadline-home"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("LOADLINE_HOME", home)
			for _, variable := range []string{"LOCALAPPDATA", "APPDATA", "HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
				t.Setenv(variable, t.TempDir())
			}

			paths, err := config.DefaultPaths()
			if err != nil {
				t.Fatalf("DefaultPaths: %v", err)
			}

			if want := filepath.Join(paths.DataDir, "browser"); paths.BrowserProfileDir != want {
				t.Errorf("BrowserProfileDir = %q, want %q", paths.BrowserProfileDir, want)
			}
			if home != "" && paths.DataDir != home {
				t.Errorf("DataDir = %q, want LOADLINE_HOME, %q", paths.DataDir, home)
			}
		})
	}
}

func TestDefaultPaths_LoadlineHomeIsCleaned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", home+string(filepath.Separator)+"a"+string(filepath.Separator)+"..")

	got, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if got.DataDir != home {
		t.Errorf("DataDir = %q, want %q", got.DataDir, home)
	}
}

func TestDefaultPaths_RelativeLoadlineHomeIsRejected(t *testing.T) {
	t.Setenv("LOADLINE_HOME", filepath.Join("relative", "home"))

	_, err := config.DefaultPaths()
	if err == nil {
		t.Fatal("DefaultPaths accepted a relative LOADLINE_HOME")
	}
	if !strings.Contains(err.Error(), "LOADLINE_HOME") {
		t.Errorf("error %q does not name LOADLINE_HOME", err)
	}
}

func TestDefaultPaths_CreatesNothing(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-created")
	t.Setenv("LOADLINE_HOME", home)

	if _, err := config.DefaultPaths(); err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("DefaultPaths touched %s (stat error: %v), want it left alone", home, err)
	}
}

func TestResolveConfigDir_EmptyEnvUsesDotClaudeUnderHome(t *testing.T) {
	home := t.TempDir()

	got := config.ResolveConfigDir("", home)

	if want := filepath.Join(home, ".claude"); !sameDir(got, want) {
		t.Errorf("ResolveConfigDir(\"\", home) = %q, want %q", got, want)
	}
}

func TestResolveConfigDir_AbsoluteEnvWins(t *testing.T) {
	home := t.TempDir()
	env := filepath.Join(t.TempDir(), "elsewhere", ".claude-b")

	got := config.ResolveConfigDir(env, home)

	if !sameDir(got, env) {
		t.Errorf("ResolveConfigDir(%q, home) = %q, want the env value", env, got)
	}
}

func TestResolveConfigDir_RelativeEnvIsMadeAbsolute(t *testing.T) {
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	got := config.ResolveConfigDir(filepath.Join("accounts", "b"), t.TempDir())

	if want := filepath.Join(cwd, "accounts", "b"); !sameDir(got, want) {
		t.Errorf("ResolveConfigDir(relative) = %q, want %q", got, want)
	}
}

func TestResolveConfigDir_TildeIsNotExpanded(t *testing.T) {
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	got := config.ResolveConfigDir(filepath.Join("~", ".claude"), t.TempDir())

	if want := filepath.Join(cwd, "~", ".claude"); !sameDir(got, want) {
		t.Errorf("ResolveConfigDir(~/.claude) = %q, want the literal %q", got, want)
	}
}

func TestResolveConfigDir_TrailingSeparatorAndDotSegmentsAreRemoved(t *testing.T) {
	root := t.TempDir()
	sep := string(filepath.Separator)
	want := filepath.Join(root, ".claude-b")

	for name, env := range map[string]string{
		"trailing separator": want + sep,
		"dot segments":       root + sep + "x" + sep + ".." + sep + ".claude-b" + sep + "." + sep,
		"doubled separator":  root + sep + sep + ".claude-b",
	} {
		t.Run(name, func(t *testing.T) {
			if got := config.ResolveConfigDir(env, t.TempDir()); !sameDir(got, want) {
				t.Errorf("ResolveConfigDir(%q) = %q, want %q", env, got, want)
			}
		})
	}
}

func TestResolveConfigDir_WindowsSpellingsOfOneDirShareAKey(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case-insensitive keys apply to Windows paths only")
	}
	const want = domain.ConfigDir(`c:\users\x\.claude-b`)

	for _, env := range []string{
		`C:\Users\X\.claude-b\`,
		`c:/users/x/.claude-b`,
		`C:/Users/X/.claude-b/`,
		`c:\USERS\x\..\X\.Claude-B`,
	} {
		if got := config.ResolveConfigDir(env, `C:\Users\X`); got != want {
			t.Errorf("ResolveConfigDir(%q) = %q, want %q", env, got, want)
		}
	}
}

func TestResolveConfigDir_CaseIsPreservedOutsideWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows paths are case-folded")
	}
	root := t.TempDir()

	upper := config.ResolveConfigDir(filepath.Join(root, "Claude-B"), root)
	lower := config.ResolveConfigDir(filepath.Join(root, "claude-b"), root)

	if upper == lower {
		t.Errorf("directories differing only by case share the key %q", upper)
	}
}

// sameDir compares a resolved key with an expected path, folding case where
// the key does.
func sameDir(got domain.ConfigDir, want string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(string(got), want)
	}
	return string(got) == want
}

// What the layout puts in the data directory must be what the data
// directory's check takes for Loadline's own.
func TestDefaultPaths_TheDataDirectoryOwnsEveryFileTheLayoutPutsThere(t *testing.T) {
	t.Setenv("LOADLINE_HOME", filepath.Join(t.TempDir(), "loadline-home"))

	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}

	for _, path := range []string{paths.ConfigFile, paths.TokenFile, paths.SpoolDir, paths.DBFile, paths.BrowserProfileDir} {
		if !datadir.Owns(filepath.Base(path)) {
			t.Errorf("datadir.Owns(%q) = false, want true", filepath.Base(path))
		}
	}
}
