package launch

import (
	"os"
	"os/exec"
	"testing"
)

// assertNullStreamsInHome checks that cmd runs in the home directory, which
// holds no project open, with nothing of its caller's standard streams:
// exec gives a nil stream the null device.
func assertNullStreamsInHome(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
		t.Errorf("streams = %v, %v, %v; want all nil for the null device", cmd.Stdin, cmd.Stdout, cmd.Stderr)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Dir != home {
		t.Errorf("Dir = %q, want the home directory %q", cmd.Dir, home)
	}
}

func TestCommands_FailWithoutAHomeDirectory(t *testing.T) {
	for _, name := range []string{"HOME", "USERPROFILE", "home"} {
		t.Setenv(name, "")
	}

	if _, err := serverCommand("loadline", "serve"); err == nil {
		t.Error("serverCommand without a home directory succeeded, want an error")
	}
	if _, err := browserCommand("browser", "http://127.0.0.1:8289/"); err == nil {
		t.Error("browserCommand without a home directory succeeded, want an error")
	}
}
