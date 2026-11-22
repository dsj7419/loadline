package launch

import (
	"slices"
	"testing"
)

func TestCandidates_MacApplications(t *testing.T) {
	for name, want := range map[string]string{
		"brave":   "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		"chrome":  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"edge":    "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"firefox": "/Applications/Firefox.app/Contents/MacOS/firefox",
	} {
		if got := candidates(name); !slices.Equal(got, []string{want}) {
			t.Errorf("candidates(%q) = %q, want %q", name, got, want)
		}
	}
}
