package claudecli

import (
	"os"
	"path/filepath"
	"testing"
)

// writeShim writes a stand-in for the claude.cmd that an npm install leaves
// on PATH. It prints the arguments it received and CLAUDE_CONFIG_DIR.
func writeShim(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "claude.cmd")
	script := "@echo off\r\necho args=%* dir=%CLAUDE_CONFIG_DIR%\r\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	return path
}
