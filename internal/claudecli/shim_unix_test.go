//go:build !windows

package claudecli

import (
	"os"
	"path/filepath"
	"testing"
)

// writeShim writes a stand-in for a claude launcher script on PATH. It prints
// the arguments it received and CLAUDE_CONFIG_DIR.
func writeShim(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\necho \"args=$* dir=$CLAUDE_CONFIG_DIR\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	return path
}
