//go:build !windows

package claudecli

import "os/exec"

// caseInsensitiveEnv is whether environment variable names differ only by case
// on this platform.
const caseInsensitiveEnv = false

// hideWindow does nothing: only Windows gives a child process a console.
func hideWindow(*exec.Cmd) {}
