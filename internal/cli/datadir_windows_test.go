package cli_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// driveRoot returns a folder that lets every signed-in user modify what is
// below it, as the root of a second drive does, and that install, serve and
// doctor trust, with every folder above it, as nobody else can move a drive
// root.
func driveRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	fspermtest.Permissive(t, root)
	t.Cleanup(cli.TrustFoldersFrom(root))
	return root
}

// sharedHome points LOADLINE_HOME at a directory directly in a driveRoot,
// and returns it. The directory itself does not exist yet.
func sharedHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(driveRoot(t), "loadline")
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
	data := filepath.Join(driveRoot(t), "data")
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

// Another Loadline process writes a file in the data directory under a
// temporary name before it puts it in place, here one that brought an
// entry for every signed-in user from the drive root. Doctor does not take
// it for one of Loadline's files that others can get into.
func TestDoctor_PassesOverAFileAnotherProcessIsWritingInTheDataDir(t *testing.T) {
	home := sharedHome(t)
	writeAll(t, map[string]string{
		filepath.Join(home, "config.json"): fmt.Sprintf(`{"port":%d,"claudePath":%q}`, freePort(t), filepath.Join(home, "no-claude")),
	})
	dir := t.TempDir()
	if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
		t.Fatalf("install = %d, stderr %q", got.code, got.stderr)
	}
	outside := filepath.Join(filepath.Dir(home), "outside")
	writeAll(t, map[string]string{outside: knownToken})
	inFlight := filepath.Join(home, ".token-1234")
	if err := os.Rename(outside, inFlight); err != nil {
		t.Fatal(err)
	}

	got := run(t, "doctor", "--config-dir", dir)

	if want := "✔ data directory: " + home + "\n"; !strings.Contains(got.stdout, want) {
		t.Errorf("doctor stdout:\n%s\nwant the line %q", got.stdout, want)
	}
}

// A folder between a drive root and LOADLINE_HOME, as D:\tools is above
// D:\tools\loadline, inherits the root's "Authenticated Users: Modify",
// which lets them move it aside and put a binary of their own at the path
// every hook runs. Install refuses before it shows or asks or writes
// anything, since nothing could be installed there.
func TestInstall_RefusesADataDirOthersCanMoveAside(t *testing.T) {
	tools := filepath.Join(driveRoot(t), "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tools, "loadline")
	t.Setenv("LOADLINE_HOME", home)
	dir := t.TempDir()

	got := runWithInput(t, "y\n", "install", "--config-dir", dir)

	if got.code != 1 || !strings.Contains(got.stderr, tools+" lets ") || !strings.Contains(got.stderr, "delete or rename it") ||
		!strings.Contains(got.stderr, "set LOADLINE_HOME to a folder in your user profile") {
		t.Errorf("install = %d, stderr %q; want 1 and %s named as a folder others can move aside", got.code, got.stderr, tools)
	}
	if got.stdout != "" {
		t.Errorf("install showed %q before refusing, want nothing shown or asked", got.stdout)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("install created %s although it refused it", home)
	}
	if names := entries(t, dir); len(names) != 0 {
		t.Errorf("install wrote %q to the config dir although it refused", names)
	}
}

func TestServe_RefusesADataDirOthersCanMoveAside(t *testing.T) {
	f := newServeFixture(t)
	tools := filepath.Join(driveRoot(t), "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(tools, "data")
	f.paths = config.Paths{
		DataDir:    data,
		ConfigFile: f.paths.ConfigFile,
		TokenFile:  filepath.Join(data, "token"),
		SpoolDir:   filepath.Join(data, "spool"),
		DBFile:     filepath.Join(data, "loadline.db"),
	}
	var stderr strings.Builder
	// Bounded, so that a server that wrongly starts gives way to the check.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	code := cli.Serve(ctx, f.options(), cli.IO{In: strings.NewReader(""), Out: io.Discard, Err: &stderr})

	if code != 1 || !strings.Contains(stderr.String(), tools+" lets ") {
		t.Errorf("Serve = %d, stderr %q; want 1 and %s named as a folder others can move aside", code, stderr.String(), tools)
	}
}

func TestDoctor_ReportsADataDirOthersCanMoveAside(t *testing.T) {
	tools := filepath.Join(driveRoot(t), "tools")
	home := filepath.Join(tools, "loadline")
	writeAll(t, map[string]string{
		filepath.Join(home, "config.json"): fmt.Sprintf(`{"port":%d,"claudePath":%q}`, freePort(t), filepath.Join(home, "no-claude")),
	})
	t.Setenv("LOADLINE_HOME", home)

	got := run(t, "doctor", "--config-dir", t.TempDir())

	want := "✖ data directory: " + tools + " lets "
	if got.code != 1 || !strings.Contains(got.stdout, want) || !strings.Contains(got.stdout, "replace the binary every hook runs") {
		t.Errorf("doctor = %d, stdout:\n%s\nwant 1 and a line that starts %q", got.code, got.stdout, want)
	}
}

// junction makes link a directory junction to target, as any user may in a
// folder they can create directories in.
func junction(t *testing.T, link, target string) {
	t.Helper()
	//nolint:gosec // G204: the paths are the test's own temporary folders
	if out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
}

// linkedHome points LOADLINE_HOME at a junction, in a private folder, to a
// folder made in what stands for the root of a second drive, which lets
// every signed-in user modify what it holds, and returns both.
func linkedHome(t *testing.T) (home, target string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	for _, dir := range []string{root, filepath.Join(root, "drive"), filepath.Join(root, "profile")} {
		if err := fsperm.MkdirAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(cli.TrustFoldersFrom(root))
	fspermtest.Grant(t, filepath.Join(root, "drive"), 0x1301bf, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT|windows.INHERIT_ONLY)
	target = filepath.Join(root, "drive", "loadline-data")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(root, "profile", "loadline")
	junction(t, home, target)
	t.Setenv("LOADLINE_HOME", home)
	return home, target
}

// Install would make the link private and leave the folder it leads to,
// which would hold the token and the binary, as open as it is. It refuses,
// saying where the link leads, and writes nothing.
func TestInstall_RefusesADataDirThatIsALink(t *testing.T) {
	home, target := linkedHome(t)
	dir := t.TempDir()

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 1 || !strings.Contains(got.stderr, home+" is a link to "+target) {
		t.Errorf("install = %d, stderr %q; want 1 and %s refused as a link to %s", got.code, got.stderr, home, target)
	}
	for _, path := range []string{filepath.Join(target, "token"), filepath.Join(target, "bin"), filepath.Join(dir, "settings.json")} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("install wrote %s although it refused", path)
		}
	}
}

// A link is no folder others could move aside, which a demo lets pass: the
// demo refuses it too.
func TestServe_DemoRefusesADataDirThatIsALink(t *testing.T) {
	f := newServeFixture(t)
	target := filepath.Join(t.TempDir(), "real")
	if err := fsperm.MkdirAll(target); err != nil {
		t.Fatal(err)
	}
	// The token is there already, so it is the server's own check that
	// finds the link, not the token's creation.
	writeAll(t, map[string]string{filepath.Join(target, "token"): knownToken})
	data := filepath.Join(filepath.Dir(f.paths.DataDir), "linked")
	junction(t, data, target)
	f.paths = config.Paths{
		DataDir:    data,
		ConfigFile: f.paths.ConfigFile,
		TokenFile:  filepath.Join(data, "token"),
		SpoolDir:   filepath.Join(data, "spool"),
		DBFile:     filepath.Join(data, "loadline.db"),
	}
	o := f.options()
	o.Demo = true
	var stderr strings.Builder
	// Bounded, so that a demo that wrongly starts gives way to the check.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	code := cli.Serve(ctx, o, cli.IO{In: strings.NewReader(""), Out: io.Discard, Err: &stderr})

	if code != 1 || !strings.Contains(stderr.String(), data+" is a link to "+target) {
		t.Errorf("demo Serve = %d, stderr %q; want 1 and %s refused as a link", code, stderr.String(), data)
	}
}
