package launch

import (
	"slices"
	"testing"
)

func TestCandidates_LinuxCommandsOnPath(t *testing.T) {
	for name, want := range map[string]string{
		"brave":   "brave-browser",
		"chrome":  "google-chrome",
		"edge":    "microsoft-edge",
		"firefox": "firefox",
	} {
		if got := candidates(name); !slices.Equal(got, []string{want}) {
			t.Errorf("candidates(%q) = %q, want %q", name, got, want)
		}
	}
}
