//go:build !windows

package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
)

// openDir creates dir with mode 0755, as a user who made LOADLINE_HOME
// with mkdir would have it.
func openDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertMode0700(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("%s has mode %#o, want 0700", dir, got)
	}
}

func TestInstall_TakesGroupAndOthersOffTheDataDir(t *testing.T) {
	home := filepath.Join(t.TempDir(), "loadline")
	openDir(t, home)
	t.Setenv("LOADLINE_HOME", home)

	if got := run(t, "install", "--config-dir", t.TempDir(), "--yes"); got.code != 0 {
		t.Fatalf("install = %d, stderr %q", got.code, got.stderr)
	}

	assertMode0700(t, home)
}

func TestServe_TakesGroupAndOthersOffTheDataDir(t *testing.T) {
	f := newServeFixture(t)
	data := f.paths.DataDir
	openDir(t, data)
	if _, err := config.EnsureToken(f.paths.TokenFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(data, 0o755); err != nil {
		t.Fatal(err)
	}

	f.start()

	assertMode0700(t, data)
}

func TestDoctor_FailsWhileTheDataDirAdmitsOthers(t *testing.T) {
	home := filepath.Join(t.TempDir(), "loadline")
	openDir(t, home)
	t.Setenv("LOADLINE_HOME", home)
	contents := fmt.Sprintf(`{"port":%d,"claudePath":%q}`, freePort(t), filepath.Join(home, "no-claude"))
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	got := run(t, "doctor", "--config-dir", t.TempDir())

	want := "✖ data directory: " + home + " is not private: its mode 0755 also admits group and others; run loadline install\n"
	if got.code != 1 || !strings.Contains(got.stdout, want) {
		t.Errorf("doctor = %d, stdout:\n%s\nwant 1 and %q", got.code, got.stdout, want)
	}
}
