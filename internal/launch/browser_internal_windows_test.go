package launch

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCandidates_WindowsInstallLocations(t *testing.T) {
	roots := map[string]string{
		"ProgramFiles":      `C:\Program Files`,
		"ProgramFiles(x86)": `C:\Program Files (x86)`,
		"LOCALAPPDATA":      `C:\Users\dev\AppData\Local`,
	}
	getenv := func(name string) string { return roots[name] }
	for name, tail := range map[string]string{
		"brave":   `BraveSoftware\Brave-Browser\Application\brave.exe`,
		"chrome":  `Google\Chrome\Application\chrome.exe`,
		"edge":    `Microsoft\Edge\Application\msedge.exe`,
		"firefox": `Mozilla Firefox\firefox.exe`,
	} {
		t.Run(name, func(t *testing.T) {
			want := []string{
				`C:\Program Files\` + tail,
				`C:\Program Files (x86)\` + tail,
				`C:\Users\dev\AppData\Local\` + tail,
			}

			if got := candidates(name, getenv); !slices.Equal(got, want) {
				t.Errorf("candidates(%q) = %q, want %q", name, got, want)
			}
		})
	}
}

// An unset root must not become a path relative to whatever directory the
// server runs in.
func TestCandidates_SkipUnsetRoots(t *testing.T) {
	getenv := func(name string) string {
		if name == "LOCALAPPDATA" {
			return `C:\Users\dev\AppData\Local`
		}
		return ""
	}

	got := candidates("brave", getenv)

	want := []string{`C:\Users\dev\AppData\Local\BraveSoftware\Brave-Browser\Application\brave.exe`}
	if !slices.Equal(got, want) {
		t.Errorf("candidates = %q, want %q", got, want)
	}
}

func TestFirstInstalled_FindsTheInstalledCopy(t *testing.T) {
	programFiles, localAppData := t.TempDir(), t.TempDir()
	getenv := func(name string) string {
		return map[string]string{"ProgramFiles": programFiles, "LOCALAPPDATA": localAppData}[name]
	}
	brave := filepath.Join(localAppData, "BraveSoftware", "Brave-Browser", "Application", "brave.exe")
	if err := os.MkdirAll(filepath.Dir(brave), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brave, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got, ok := firstInstalled(candidates("brave", getenv))

	if !ok || got != brave {
		t.Errorf("firstInstalled = %q, %v; want %q", got, ok, brave)
	}
	if got, ok := firstInstalled(candidates("chrome", getenv)); ok {
		t.Errorf("firstInstalled found chrome at %q, which is not installed", got)
	}
}
