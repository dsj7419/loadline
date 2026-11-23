package cli_test

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// standardProfile points the commands at a standard profile of the test's
// own, LOADLINE_HOME unset, and returns the default data directory in it.
func standardProfile(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "Users", "me")
	local, roaming := filepath.Join(home, "AppData", "Local"), filepath.Join(home, "AppData", "Roaming")
	for _, dir := range []string{local, filepath.Join(roaming, "loadline")} {
		if err := fsperm.MkdirAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("LOADLINE_HOME", "")
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", roaming)
	// No claude runs: the config names one that is not there.
	contents := fmt.Sprintf(`{"port":%d,"claudePath":%q}`, freePort(t), filepath.Join(home, "no-claude"))
	if err := os.WriteFile(filepath.Join(roaming, "loadline", "config.json"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := config.DefaultPaths()
	if err != nil || paths.DataDir != filepath.Join(local, "loadline") {
		t.Fatalf("DefaultPaths = %+v, %v; want the data directory in %s", paths, err, local)
	}
	return paths.DataDir
}

// dataDirLine is doctor's line about the data directory in out.
func dataDirLine(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "data directory") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// admitsNobodyElse checks that nothing in dir admits anyone else.
func admitsNobodyElse(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if others := fspermtest.Others(t, path, fspermtest.Any); len(others) != 0 {
			t.Errorf("%s admits %v", path, others)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

// serveTheDefault runs a real server on the default paths until it is
// stopped, and checks that it started.
func serveTheDefault(t *testing.T) {
	t.Helper()
	f := newServeFixture(t)
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	f.paths = paths
	f.start().stop(t)
	if log := f.logText(); !strings.Contains(log, "loadline started") {
		t.Errorf("the server's log in %s: %q", paths.DataDir, log)
	}
}

// The default setup through the commands: install, doctor, a real server
// and install again all go through, and leave everything in the data
// directory to the user alone.
func TestDefaultSetup_GoesThroughEveryCommand(t *testing.T) {
	data := standardProfile(t)
	settings := t.TempDir()

	if got := run(t, "install", "--config-dir", settings, "--yes"); got.code != 0 {
		t.Fatalf("install = %d, stderr %q", got.code, got.stderr)
	}
	if line := dataDirLine(run(t, "doctor", "--config-dir", settings).stdout); !strings.HasPrefix(line, "✔") {
		t.Errorf("doctor after install: %q, want the data directory to pass", line)
	}
	if got := run(t, "install", "--config-dir", settings, "--yes"); got.code != 0 || !strings.Contains(got.stdout, "Already installed.") {
		t.Errorf("install again = %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	serveTheDefault(t)
	serveTheDefault(t)
	if got := run(t, "install", "--config-dir", settings, "--yes"); got.code != 0 || !strings.Contains(got.stdout, "Already installed.") {
		t.Errorf("install after the server ran = %d, stdout %q, stderr %q", got.code, got.stdout, got.stderr)
	}
	if line := dataDirLine(run(t, "doctor", "--config-dir", settings).stdout); !strings.HasPrefix(line, "✔") {
		t.Errorf("doctor after the server ran: %q, want the data directory to pass", line)
	}
	admitsNobodyElse(t, data)
}

// The same, with the token's temporary name left behind, as when the
// process that made the token was ended between linking it into place and
// removing that name, or could not remove it: install, doctor and a real
// server go through as before, and install removes the name.
func TestDefaultSetup_GoesThroughWithTheTokensTemporaryNameLeftBehind(t *testing.T) {
	data := standardProfile(t)
	settings := t.TempDir()
	if got := run(t, "install", "--config-dir", settings, "--yes"); got.code != 0 {
		t.Fatalf("install = %d, stderr %q", got.code, got.stderr)
	}
	leftover := filepath.Join(data, ".token-1234567890")
	if err := os.Link(filepath.Join(data, "token"), leftover); err != nil {
		t.Fatal(err)
	}

	if line := dataDirLine(run(t, "doctor", "--config-dir", settings).stdout); !strings.HasPrefix(line, "✔") {
		t.Errorf("doctor with the name left behind: %q, want the data directory to pass", line)
	}
	if got := run(t, "install", "--config-dir", settings, "--yes"); got.code != 0 {
		t.Errorf("install again = %d, stderr %q; want it to go through as before", got.code, got.stderr)
	}
	if _, err := os.Lstat(leftover); err == nil {
		t.Errorf("install left %s, the token's temporary name, in place", leftover)
	}
	if err := os.Link(filepath.Join(data, "token"), leftover); err != nil {
		t.Fatal(err)
	}
	f := newServeFixture(t)
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	f.paths = paths
	// Bounded: a server that starts, as it should, runs until the time is up.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var stderr strings.Builder
	if code := cli.Serve(ctx, f.options(), cli.IO{In: strings.NewReader(""), Out: io.Discard, Err: &stderr}); code != 0 || stderr.Len() != 0 {
		t.Errorf("a real server = %d, stderr %q; want it to start as before", code, stderr.String())
	}
}

// A token whose only name left is its temporary one is no token: install
// makes a new one, and doctor then finds the data directory as it should be.
func TestDefaultSetup_GoesThroughWithOnlyTheTokensTemporaryNameLeft(t *testing.T) {
	data := standardProfile(t)
	settings := t.TempDir()
	if got := run(t, "install", "--config-dir", settings, "--yes"); got.code != 0 {
		t.Fatalf("install = %d, stderr %q", got.code, got.stderr)
	}
	token := filepath.Join(data, "token")
	if err := os.Link(token, filepath.Join(data, ".token-1234567890")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(token); err != nil {
		t.Fatal(err)
	}

	again := run(t, "install", "--config-dir", settings, "--yes")
	line := dataDirLine(run(t, "doctor", "--config-dir", settings).stdout)

	if again.code != 0 || !strings.HasPrefix(line, "✔") {
		t.Errorf("install = %d (%q), and doctor says %q; want both to go through", again.code, again.stderr, line)
	}
}
