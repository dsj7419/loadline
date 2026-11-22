package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// sharedHome points LOADLINE_HOME at a directory inside one that lets every
// signed-in user modify what is below it, as the root of a second drive does,
// and returns it. The directory itself does not exist yet.
func sharedHome(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	fspermtest.Permissive(t, parent)
	home := filepath.Join(parent, "loadline")
	t.Setenv("LOADLINE_HOME", home)
	return home
}

func writeAll(t *testing.T, files map[string]string) {
	t.Helper()
	for path, contents := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertOnlyTheUser(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if others := fspermtest.Others(t, path, fspermtest.Any); len(others) > 0 {
			t.Errorf("%s admits %s, want only the user, SYSTEM and Administrators", path, strings.Join(others, ", "))
		}
	}
}

// An earlier Loadline left its data directory as the folder above it passed
// it down; installing again makes it, and all it holds, private.
func TestInstall_MakesTheDataDirPrivate(t *testing.T) {
	home := sharedHome(t)
	earlier := map[string]string{
		filepath.Join(home, "token"):               knownToken,
		filepath.Join(home, "spool", "event.json"): `{}`,
		filepath.Join(home, "loadline.log"):        "loadline started",
		filepath.Join(home, "loadline.db"):         "history",
	}
	writeAll(t, earlier)
	dir := t.TempDir()

	if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
		t.Fatalf("install = %d, stderr %q", got.code, got.stderr)
	}

	exe := filepath.FromSlash(installedExe(home))
	paths := []string{home, filepath.Join(home, "spool"), filepath.Dir(exe), exe}
	for path := range earlier {
		paths = append(paths, path)
	}
	assertOnlyTheUser(t, paths...)
}

// The doctor tells a user whose data directory admits others to install
// again, which must then fix it even with nothing else to change.
func TestInstall_WithNothingElseToChangeMakesTheDataDirPrivate(t *testing.T) {
	home := sharedHome(t)
	dir := t.TempDir()
	if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
		t.Fatalf("install = %d, stderr %q", got.code, got.stderr)
	}
	fspermtest.Permissive(t, home)

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 0 || !strings.Contains(got.stdout, "Already installed.") {
		t.Fatalf("install again = %d, stdout %q, stderr %q; want 0 and Already installed.", got.code, got.stdout, got.stderr)
	}
	exe := filepath.FromSlash(installedExe(home))
	assertOnlyTheUser(t, home, filepath.Join(home, "token"), filepath.Dir(exe), exe)
}

func TestInstall_DryRunLeavesTheDataDirAlone(t *testing.T) {
	home := sharedHome(t)
	writeAll(t, map[string]string{filepath.Join(home, "token"): knownToken})
	dir := t.TempDir()
	dryRun := func(when string) {
		t.Helper()
		if got := run(t, "install", "--config-dir", dir, "--dry-run"); got.code != 0 {
			t.Fatalf("install --dry-run %s = %d, stderr %q", when, got.code, got.stderr)
		}
		if others := fspermtest.Others(t, home, fspermtest.Any); len(others) == 0 {
			t.Errorf("a dry run %s made %s private, want it to write nothing", when, home)
		}
	}

	dryRun("before the install")
	if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
		t.Fatalf("install = %d, stderr %q", got.code, got.stderr)
	}
	fspermtest.Permissive(t, home)
	dryRun("with nothing else to change")
}

// Every server start makes the data directory private, before it writes
// the log and the history there.
func TestServe_MakesTheDataDirPrivate(t *testing.T) {
	f := newServeFixture(t)
	parent := t.TempDir()
	fspermtest.Permissive(t, parent)
	data := filepath.Join(parent, "data")
	f.paths = config.Paths{
		DataDir:    data,
		ConfigFile: f.paths.ConfigFile,
		TokenFile:  filepath.Join(data, "token"),
		SpoolDir:   filepath.Join(data, "spool"),
		DBFile:     filepath.Join(data, "loadline.db"),
	}
	// The token is there already, so it is the server that makes the
	// directory private, not the token's creation.
	writeAll(t, map[string]string{f.paths.TokenFile: knownToken})

	f.start()

	assertOnlyTheUser(t, data, f.paths.TokenFile, f.paths.DBFile, filepath.Join(data, "loadline.log"))
}

func TestDoctor_FailsWhileTheDataDirAdmitsOthers(t *testing.T) {
	home := sharedHome(t)
	writeAll(t, map[string]string{
		filepath.Join(home, "config.json"): fmt.Sprintf(`{"port":%d,"claudePath":%q}`, freePort(t), filepath.Join(home, "no-claude")),
	})

	got := run(t, "doctor", "--config-dir", t.TempDir())

	prefix := "✖ data directory: " + home + " is not private: its access list also admits "
	var line string
	for l := range strings.Lines(got.stdout) {
		if strings.HasPrefix(l, prefix) {
			line = l
		}
	}
	if got.code != 1 || !strings.Contains(line, fspermtest.AuthenticatedUsers(t)) || !strings.HasSuffix(line, "; run loadline install\n") {
		t.Errorf("doctor = %d, stdout:\n%s\nwant 1 and a line that starts %q, names %s and ends with the install hint",
			got.code, got.stdout, prefix, fspermtest.AuthenticatedUsers(t))
	}
}
