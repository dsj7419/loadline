package cli_test

import (
	"os"
	"path/filepath"
	"testing"
)

// movableFolder returns a folder that every signed-in user may delete or
// rename, as one directly in the root of a second drive is.
func movableFolder(t *testing.T) string {
	t.Helper()
	folder := filepath.Join(driveRoot(t), "tools")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	return folder
}
