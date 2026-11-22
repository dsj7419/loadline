package install_test

import (
	"runtime"
	"testing"

	"github.com/dsj7419/loadline/internal/install"
)

func TestDetectShell(t *testing.T) {
	windowsEnv := map[string]string{
		"ProgramFiles":      `C:\Program Files`,
		"ProgramFiles(x86)": `C:\Program Files (x86)`,
		"LOCALAPPDATA":      `C:\Users\example\AppData\Local`,
		"PATH":              `C:\Windows\system32;"D:\Tools\Git\cmd";C:\Windows`,
	}
	tests := []struct {
		name  string
		goos  string
		env   map[string]string
		files []string
		want  install.Shell
	}{
		{name: "linux", goos: "linux", want: install.ShellBash},
		{name: "macOS", goos: "darwin", want: install.ShellBash},
		{name: "linux ignores Windows settings", goos: "linux", env: windowsEnv, want: install.ShellBash},
		{name: "windows without Git", goos: "windows", env: windowsEnv, want: install.ShellPowerShell},
		{
			name: "windows with CLAUDE_CODE_GIT_BASH_PATH",
			goos: "windows",
			env:  map[string]string{"CLAUDE_CODE_GIT_BASH_PATH": `E:\git\bin\bash.exe`},
			want: install.ShellBash,
		},
		{
			name:  "windows with Git in Program Files",
			goos:  "windows",
			env:   windowsEnv,
			files: []string{`C:\Program Files\Git\bin\bash.exe`},
			want:  install.ShellBash,
		},
		{
			name:  "windows with 32-bit Git",
			goos:  "windows",
			env:   windowsEnv,
			files: []string{`C:\Program Files (x86)\Git\bin\bash.exe`},
			want:  install.ShellBash,
		},
		{
			name:  "windows with Git installed for the user",
			goos:  "windows",
			env:   windowsEnv,
			files: []string{`C:\Users\example\AppData\Local\Programs\Git\bin\bash.exe`},
			want:  install.ShellBash,
		},
		{
			name:  "windows with Git on PATH",
			goos:  "windows",
			env:   windowsEnv,
			files: []string{`D:\Tools\Git\cmd\git.exe`, `D:\Tools\Git\bin\bash.exe`},
			want:  install.ShellBash,
		},
		{
			name:  "windows with git on PATH but no bash beside it",
			goos:  "windows",
			env:   windowsEnv,
			files: []string{`D:\Tools\Git\cmd\git.exe`},
			want:  install.ShellPowerShell,
		},
		{
			name:  "windows with bash only somewhere unknown",
			goos:  "windows",
			env:   windowsEnv,
			files: []string{`C:\msys64\usr\bin\bash.exe`},
			want:  install.ShellPowerShell,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.env[key] }
			exists := func(path string) bool {
				for _, f := range tt.files {
					if f == path {
						return true
					}
				}
				return false
			}

			if got := install.DetectShellOn(tt.goos, getenv, exists); got != tt.want {
				t.Errorf("detectShell = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDetectShell_UsesThisSystem(t *testing.T) {
	want := install.ShellBash
	if runtime.GOOS == "windows" {
		want = install.ShellPowerShell
	}

	got := install.DetectShell(func(string) string { return "" }, func(string) bool { return false })

	if got != want {
		t.Errorf("DetectShell with no Git = %d, want %d on %s", got, want, runtime.GOOS)
	}
}
