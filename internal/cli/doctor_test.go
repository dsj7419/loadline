package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/clocktest"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/install"
)

// writeClaude writes a stand-in for claude that prints output, whatever its
// arguments, and returns its path.
func writeClaude(t *testing.T, output string) string {
	t.Helper()

	path, script := filepath.Join(t.TempDir(), "claude"), "#!/bin/sh\necho '"+output+"'\n"
	if runtime.GOOS == "windows" {
		path, script = path+".cmd", "@echo off\r\necho "+output+"\r\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDoctor_PassesEveryCheckOnceInstalled(t *testing.T) {
	t.Setenv("LOADLINE_HOME", t.TempDir())
	f := newServeFixture(t)
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	f.paths = paths
	f.writeConfig(fmt.Sprintf(`{"port":%d,"claudePath":%q}`, f.port, writeClaude(t, "2.1.330 (Claude Code)")))
	// Behind by an hour, the server's clock makes what it hears as it starts
	// too old to count, so that no feed's age depends on the real time.
	f.clock = clocktest.New(time.Now().Add(-time.Hour))
	f.start()
	dir := t.TempDir()
	if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
		t.Fatalf("install = %d, stderr %q", got.code, got.stderr)
	}

	got := run(t, "doctor", "--config-dir", dir)

	want := "" +
		"✔ claude: 2.1.330\n" +
		"✔ token: " + paths.TokenFile + "\n" +
		"✔ settings " + filepath.Join(dir, "settings.json") + ": wired\n" +
		"✔ binary: " + filepath.FromSlash(install.InstalledPath(paths.DataDir)) + "\n" +
		"✔ server: running at " + forward.LoopbackURL(f.port) + "\n" +
		"✔ hook feed: no active sessions\n" +
		"✔ status line feed: no active sessions\n" +
		"✔ OpenTelemetry feed: no active sessions\n" +
		"✔ poll feed: no active sessions\n"
	if got.code != 0 || got.stdout != want || got.stderr != "" {
		t.Errorf("doctor = %d, stderr %q, stdout:\n%s\nwant 0 and:\n%s", got.code, got.stderr, got.stdout, want)
	}
}

func TestDoctor_FailsBeforeInstallAndChangesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", home)
	port := freePort(t)
	contents := fmt.Sprintf(`{"port":%d,"claudePath":%q}`, port, filepath.Join(home, "no-claude"))
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	missing, unwired := t.TempDir(), t.TempDir()
	writeSettings(t, unwired, `{"model": "opus"}`)

	got := run(t, "doctor", "--config-dir", missing, "--config-dir", unwired)

	want := "" +
		"✖ claude: not found; install Claude Code, or set claudePath in the Loadline config\n" +
		"✖ token: " + filepath.Join(home, "token") + " does not exist; run loadline install\n" +
		"✖ settings " + filepath.Join(missing, "settings.json") + ": not wired: the file does not exist; run loadline install\n" +
		"✖ settings " + filepath.Join(unwired, "settings.json") + ": not wired: install would change " +
		"hooks.SessionStart, hooks.SessionEnd, hooks.UserPromptSubmit and 18 more; run loadline install\n" +
		"✖ binary: " + filepath.FromSlash(install.InstalledPath(home)) + " does not exist; run loadline install\n" +
		"✖ server: no Loadline server answers at " + forward.LoopbackURL(port) +
		"; the next Claude Code session starts it, or run loadline serve\n" +
		"✖ feeds: cannot be checked while the server is down\n"
	if got.code != 1 || got.stdout != want || got.stderr != "" {
		t.Errorf("doctor = %d, stderr %q, stdout:\n%s\nwant 1 and:\n%s", got.code, got.stderr, got.stdout, want)
	}
	if names := entries(t, home); !slices.Equal(names, []string{"config.json"}) {
		t.Errorf("Loadline's home holds %q after doctor, want only config.json", names)
	}
	if names := entries(t, missing); len(names) != 0 {
		t.Errorf("doctor created %q in a config dir", names)
	}
	if settings := readSettings(t, unwired); settings != `{"model": "opus"}` {
		t.Errorf("doctor changed the settings to %q", settings)
	}
}

func TestDoctor_ABadConfigFailsAndTheOtherChecksUseTheDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", home)
	// By default claude comes from PATH, and there is none on this one.
	t.Setenv("PATH", t.TempDir())
	configFile := filepath.Join(home, "config.json")
	if err := os.WriteFile(configFile, []byte(`{"theme": "dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, loadErr := config.Load(configFile, "")
	if loadErr == nil {
		t.Fatal("the config loads; want it refused")
	}
	dir := t.TempDir()

	got := run(t, "doctor", "--config-dir", dir)

	// The lines after the binary's depend on whatever answers on the default
	// port, so only the start is compared.
	want := "" +
		"✖ config: " + loadErr.Error() + "; the checks below use the defaults\n" +
		"✖ claude: not found; install Claude Code, or set claudePath in the Loadline config\n" +
		"✖ token: " + filepath.Join(home, "token") + " does not exist; run loadline install\n" +
		"✖ settings " + filepath.Join(dir, "settings.json") + ": not wired: the file does not exist; run loadline install\n" +
		"✖ binary: " + filepath.FromSlash(install.InstalledPath(home)) + " does not exist; run loadline install\n"
	if got.code != 1 || got.stderr != "" || !strings.HasPrefix(got.stdout, want) {
		t.Errorf("doctor = %d, stderr %q, stdout:\n%s\nwant 1, starting with:\n%s", got.code, got.stderr, got.stdout, want)
	}
}

func TestDoctor_RejectsArguments(t *testing.T) {
	tests := []struct {
		args   []string
		stderr string
	}{
		{args: []string{"doctor", "extra"}, stderr: `loadline doctor: unexpected argument "extra"`},
		{args: []string{"doctor", "--yes"}, stderr: "flag provided but not defined: -yes"},
	}
	for _, tt := range tests {
		got := run(t, tt.args...)

		if got.code != 2 || got.stdout != "" || !strings.Contains(got.stderr, tt.stderr) {
			t.Errorf("%q = %d, stdout %q, stderr %q; want 2 and %q", tt.args, got.code, got.stdout, got.stderr, tt.stderr)
		}
	}
}

func TestRun_HelpListsDoctor(t *testing.T) {
	if got := run(t, "help"); !strings.Contains(got.stdout, "\n  doctor ") {
		t.Errorf("usage lacks doctor:\n%s", got.stdout)
	}
}
