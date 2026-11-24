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

// A browser path named without its extension runs the file that PATHEXT
// finds for it first, as exec.LookPath and Windows find it, so it is a batch
// file only when that file is one.
func TestResolve_KnowsABatchFileByTheFileWindowsRuns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  browserKind
	}{
		{name: "a .cmd beside the name", files: []string{"browser.cmd"}, want: batchFileKind},
		{name: "a .BAT beside the name", files: []string{"browser.BAT"}, want: batchFileKind},
		{name: "an .exe, which PATHEXT names before .cmd", files: []string{"browser.cmd", "browser.exe"}, want: chromiumKind},
		{name: "nothing beside the name", want: chromiumKind},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
			dir := t.TempDir()
			for _, file := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, file), nil, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			pref := filepath.Join(dir, "browser")

			program, kind := resolve(Browser{Pref: pref, AppWindow: true}, func(string) (string, bool) { return "", false })

			if program != pref || kind != tc.want {
				t.Errorf("resolve(%q) = %q, %v; want %q, %v", pref, program, kind, pref, tc.want)
			}
		})
	}
}
