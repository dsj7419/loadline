//go:build !windows

package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/cli"
)

// movableFolder returns a folder that group and others may write to
// without the sticky bit, and so rename what it holds.
func movableFolder(t *testing.T) string {
	t.Helper()
	top := t.TempDir()
	t.Cleanup(cli.TrustFoldersFrom(top))
	folder := filepath.Join(top, "shared")
	openDir(t, folder)
	if err := os.Chmod(folder, 0o777); err != nil {
		t.Fatal(err)
	}
	return folder
}
