//go:build !windows

package cli_test

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// foreignDirVariable names a folder that another account owns, as
// sudo install -d -o nobody makes one, and so opts in to the test here.
const foreignDirVariable = "LOADLINE_IT_FOREIGN_DIR"

// Whoever owns a folder can always change its mode, so a data directory that
// another account owns is never the user's alone. Install, the server and
// doctor refuse it, naming the account.
func TestIntegration_InstallServeAndDoctorRefuseAForeignDataDirectory(t *testing.T) {
	dir, owner := foreignDir(t)
	t.Setenv("LOADLINE_HOME", dir)
	// The folder holds no config, and the test may not write one there, so
	// doctor checks against the defaults. It only asks port 8289 whether a
	// server runs there, having no token to ask more with, and with nothing
	// on PATH, it runs no claude.
	t.Setenv("PATH", t.TempDir())

	assertRefusedByAll(t, dir+" belongs to "+owner+", who can give themselves the right to delete or rename it")
}

// foreignDir returns the folder foreignDirVariable names and the account that
// owns it, by its user name, or by its user ID when no account here has that
// ID, or skips the test when the variable is not set.
func foreignDir(t *testing.T) (dir, owner string) {
	t.Helper()
	named := os.Getenv(foreignDirVariable)
	if named == "" {
		t.Skipf("%s is unset; set it to a folder another account owns, as sudo install -d -o nobody makes one, "+
			"to run this test (see CONTRIBUTING.md)", foreignDirVariable)
	}
	if !filepath.IsAbs(named) {
		t.Fatalf("%s (%s) is not an absolute path, which LOADLINE_HOME must be", foreignDirVariable, named)
	}
	info, err := os.Lstat(named) //nolint:gosec // G703: named is the folder whoever runs the tests sets aside for them
	if err != nil {
		t.Fatalf("%s (%s) names no folder: %v", foreignDirVariable, named, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok {
		t.Fatalf("%s (%s) names no folder whose owner can be read", foreignDirVariable, named)
	}
	switch {
	case stat.Uid == 0:
		t.Fatalf("%s (%s) names a folder of root, whom Loadline trusts, not of another account", foreignDirVariable, named)
	case int64(stat.Uid) == int64(os.Getuid()):
		t.Fatalf("%s (%s) names a folder of yours, not of another account", foreignDirVariable, named)
	}
	id := strconv.FormatUint(uint64(stat.Uid), 10)
	owner = "the account with user ID " + id
	if account, err := user.LookupId(id); err == nil {
		owner = account.Username
	}
	return filepath.Clean(named), owner
}
