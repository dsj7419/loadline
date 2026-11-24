package install_test

import (
	"runtime"
	"slices"
	"strings"
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
			name:  "windows with CLAUDE_CODE_GIT_BASH_PATH",
			goos:  "windows",
			env:   map[string]string{"CLAUDE_CODE_GIT_BASH_PATH": `E:\git\bin\bash.exe`},
			files: []string{`E:\git\bin\bash.exe`},
			want:  install.ShellBash,
		},
		{
			name:  "windows with CLAUDE_CODE_GIT_BASH_PATH naming sh in any case",
			goos:  "windows",
			env:   map[string]string{"CLAUDE_CODE_GIT_BASH_PATH": `E:/git/bin/SH.EXE`},
			files: []string{`E:/git/bin/SH.EXE`},
			want:  install.ShellBash,
		},
		{
			name:  "windows with CLAUDE_CODE_GIT_BASH_PATH naming bash without .exe",
			goos:  "windows",
			env:   map[string]string{"CLAUDE_CODE_GIT_BASH_PATH": `E:\msys\usr\bin\bash`},
			files: []string{`E:\msys\usr\bin\bash`},
			want:  install.ShellBash,
		},
		{
			name: "windows with CLAUDE_CODE_GIT_BASH_PATH naming a missing file",
			goos: "windows",
			env:  map[string]string{"CLAUDE_CODE_GIT_BASH_PATH": `E:\git\bin\bash.exe`},
			want: install.ShellPowerShell,
		},
		{
			name:  "windows with CLAUDE_CODE_GIT_BASH_PATH naming another shell",
			goos:  "windows",
			env:   map[string]string{"CLAUDE_CODE_GIT_BASH_PATH": `E:\tools\zsh.exe`},
			files: []string{`E:\tools\zsh.exe`},
			want:  install.ShellPowerShell,
		},
		{
			name:  "windows with an invalid CLAUDE_CODE_GIT_BASH_PATH but Git installed",
			goos:  "windows",
			env:   map[string]string{"CLAUDE_CODE_GIT_BASH_PATH": `E:\nowhere\bash.exe`, "ProgramFiles": `C:\Program Files`},
			files: []string{`C:\Program Files\Git\bin\bash.exe`},
			want:  install.ShellBash,
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
			name:  "windows with Git in Program Files, whatever ProgramFiles says",
			goos:  "windows",
			env:   map[string]string{"ProgramFiles": `D:\Program Files`, "ProgramFiles(x86)": `D:\Program Files (x86)`},
			files: []string{`C:\Program Files\Git\bin\bash.exe`},
			want:  install.ShellBash,
		},
		{
			// Claude Code looks only at the literal C:\Program Files\Git and
			// C:\Program Files (x86)\Git before PATH, not where ProgramFiles
			// points.
			name:  "windows with Git only under a ProgramFiles on another drive",
			goos:  "windows",
			env:   map[string]string{"ProgramFiles": `D:\Program Files`},
			files: []string{`D:\Program Files\Git\bin\bash.exe`},
			want:  install.ShellPowerShell,
		},
		{
			// Claude Code looks only at the literal C:\Program Files\Git and
			// C:\Program Files (x86)\Git before PATH, so a per-user install
			// counts only through its git on PATH, as its installer puts it.
			name:  "windows with Git installed for the user but not on PATH",
			goos:  "windows",
			env:   windowsEnv,
			files: []string{`C:\Users\example\AppData\Local\Programs\Git\bin\bash.exe`},
			want:  install.ShellPowerShell,
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
			name: "windows where only the first git on PATH counts",
			goos: "windows",
			env:  map[string]string{"PATH": `D:\Tools\Git\cmd;E:\Git\cmd`},
			files: []string{
				`D:\Tools\Git\cmd\git.exe`,
				`E:\Git\cmd\git.exe`, `E:\Git\bin\bash.exe`,
			},
			want: install.ShellPowerShell,
		},
		{
			name: "windows skips a root-relative PATH entry for the next git, with bash beside it",
			goos: "windows",
			env:  map[string]string{"PATH": `\cmd;D:\Tools\Git\cmd`},
			files: []string{
				`\cmd\git.exe`, `\bin\bash.exe`,
				`D:\Tools\Git\cmd\git.exe`, `D:\Tools\Git\bin\bash.exe`,
			},
			want: install.ShellBash,
		},
		{
			name: "windows skips a root-relative PATH entry for the next git, without bash beside it",
			goos: "windows",
			env:  map[string]string{"PATH": `\cmd;D:\Tools\Git\cmd`},
			files: []string{
				`\cmd\git.exe`, `\bin\bash.exe`,
				`D:\Tools\Git\cmd\git.exe`,
			},
			want: install.ShellPowerShell,
		},
		{
			name:  "windows skips a deeper root-relative PATH entry",
			goos:  "windows",
			env:   map[string]string{"PATH": `\Tools\Git\cmd`},
			files: []string{`\Tools\Git\cmd\git.exe`, `\Tools\Git\bin\bash.exe`},
			want:  install.ShellPowerShell,
		},
		{
			// Git is there when the entry is read from the root of the
			// current drive, and when it is read as it is written.
			name: "windows skips a relative PATH entry",
			goos: "windows",
			env:  map[string]string{"PATH": `Tools\Git\cmd`},
			files: []string{
				`Tools\Git\cmd\git.exe`, `Tools\Git\bin\bash.exe`,
				`\Tools\Git\cmd\git.exe`, `\Tools\Git\bin\bash.exe`,
			},
			want: install.ShellPowerShell,
		},
		{
			// Git is there when the root is the folder current on D:, and
			// when the entry is read as it is written.
			name: "windows skips a drive-relative PATH entry",
			goos: "windows",
			env:  map[string]string{"PATH": `D:Tools\Git\cmd`},
			files: []string{
				`D:Tools\Git\cmd\git.exe`, `D:Tools\Git\bin\bash.exe`,
				`D:\Tools\Git\cmd\git.exe`, `D:\Tools\Git\bin\bash.exe`,
			},
			want: install.ShellPowerShell,
		},
		{
			name:  "windows with git at the root of a drive on PATH",
			goos:  "windows",
			env:   map[string]string{"PATH": `E:\`},
			files: []string{`E:\git.exe`, `E:\bin\bash.exe`},
			want:  install.ShellBash,
		},
		{
			name:  "windows with Git on a share on PATH",
			goos:  "windows",
			env:   map[string]string{"PATH": `\\host\share\Git\cmd\`},
			files: []string{`\\host\share\Git\cmd\git.exe`, `\\host\share\Git\bin\bash.exe`},
			want:  install.ShellBash,
		},
		// Windows takes a drive letter in either case, from a to z.
		{
			name:  "windows with Git on PATH on drive a:",
			goos:  "windows",
			env:   map[string]string{"PATH": `a:\Git\cmd`},
			files: []string{`a:\Git\cmd\git.exe`, `a:\Git\bin\bash.exe`},
			want:  install.ShellBash,
		},
		{
			name:  "windows with Git on PATH on drive z:",
			goos:  "windows",
			env:   map[string]string{"PATH": `z:\Git\cmd`},
			files: []string{`z:\Git\cmd\git.exe`, `z:\Git\bin\bash.exe`},
			want:  install.ShellBash,
		},
		{
			name:  "windows with Git on PATH on drive A:",
			goos:  "windows",
			env:   map[string]string{"PATH": `A:\Git\cmd`},
			files: []string{`A:\Git\cmd\git.exe`, `A:\Git\bin\bash.exe`},
			want:  install.ShellBash,
		},
		{
			name:  "windows with Git on PATH on drive Z:",
			goos:  "windows",
			env:   map[string]string{"PATH": `Z:\Git\cmd`},
			files: []string{`Z:\Git\cmd\git.exe`, `Z:\Git\bin\bash.exe`},
			want:  install.ShellBash,
		},
		{
			name:  "windows with Git on PATH under a path with the long-path prefix",
			goos:  "windows",
			env:   map[string]string{"PATH": `\\?\C:\Tools\Git\cmd`},
			files: []string{`\\?\C:\Tools\Git\cmd\git.exe`, `\\?\C:\Tools\Git\bin\bash.exe`},
			want:  install.ShellBash,
		},
		{
			name:  "windows resolves dots in a PATH entry as Windows does",
			goos:  "windows",
			env:   map[string]string{"PATH": `D:/Tools/Git/mingw64/../cmd/.`},
			files: []string{`D:\Tools\Git\cmd\git.exe`, `D:\Tools\Git\bin\bash.exe`},
			want:  install.ShellBash,
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

// Windows reads a doubled separator after a share's host as one, so a PATH
// entry that has one still names that share, and the Git Bash for its git
// is one folder up within the share, not below the entry.
func TestDetectShell_ReadsADoubledSeparatorAfterAShareHostAsOne(t *testing.T) {
	files := []string{`\\host\share\Git\cmd\git.exe`, `\\host\share\Git\bin\bash.exe`}
	getenv := func(key string) string {
		if key == "PATH" {
			return `\\host\\share\Git\cmd`
		}
		return ""
	}
	exists := func(path string) bool { return slices.Contains(files, oneSeparatorEach(path)) }

	if got := install.DetectShellOn("windows", getenv, exists); got != install.ShellBash {
		t.Errorf("detectShell = %d, want %d (Git Bash)", got, install.ShellBash)
	}
}

// oneSeparatorEach reads the Windows path p as Windows does: after the two
// separators that open a share, each run of separators counts as one.
func oneSeparatorEach(p string) string {
	if len(p) < 2 {
		return p
	}
	rest := p[2:]
	for strings.Contains(rest, `\\`) {
		rest = strings.ReplaceAll(rest, `\\`, `\`)
	}
	return p[:2] + rest
}

func TestDetectShell_ReadsTheSettingsEnvFirst(t *testing.T) {
	const bash = `E:\git\bin\bash.exe`
	exists := func(path string) bool { return path == bash }
	tests := []struct {
		name     string
		settings string
		process  map[string]string
		want     install.Shell
	}{
		{
			name:     "set only in the settings file",
			settings: `{"env": {"CLAUDE_CODE_GIT_BASH_PATH": "E:\\git\\bin\\bash.exe"}}`,
			want:     install.ShellBash,
		},
		{
			name:     "set in the settings file under another case",
			settings: `{"env": {"claude_code_git_bash_path": "E:\\git\\bin\\bash.exe"}}`,
			want:     install.ShellBash,
		},
		{
			name:     "the settings file overrides the environment",
			settings: `{"env": {"CLAUDE_CODE_GIT_BASH_PATH": "E:\\nowhere\\bash.exe"}}`,
			process:  map[string]string{"CLAUDE_CODE_GIT_BASH_PATH": bash},
			want:     install.ShellPowerShell,
		},
		{
			name:     "the environment applies when the settings file does not set it",
			settings: `{"env": {"EDITOR": "vim"}}`,
			process:  map[string]string{"CLAUDE_CODE_GIT_BASH_PATH": bash},
			want:     install.ShellBash,
		},
		{
			name:     "unreadable settings leave the environment",
			settings: `{"env": `,
			process:  map[string]string{"CLAUDE_CODE_GIT_BASH_PATH": bash},
			want:     install.ShellBash,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := install.SettingsEnv([]byte(tt.settings), func(key string) string { return tt.process[key] })

			if got := install.DetectShellOn("windows", getenv, exists); got != tt.want {
				t.Errorf("shell = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRelativeGitEntries(t *testing.T) {
	const workDir = `D:\work\project`
	tests := []struct {
		name    string
		workDir string
		path    string
		files   []string
		want    []string
	}{
		{name: "no PATH", workDir: workDir},
		{
			name:    "an absolute entry",
			workDir: workDir,
			path:    `C:\Git\cmd;\\host\share\Git\cmd`,
			files:   []string{`C:\Git\cmd\git.exe`, `\\host\share\Git\cmd\git.exe`},
		},
		{
			// Claude Code stops at the first git it finds, so no entry after
			// one that holds git.exe can lead it anywhere.
			name:    "relative entries up to the first absolute entry that holds git",
			workDir: workDir,
			path:    `tools;C:\Windows;C:\Git\cmd;bin`,
			files:   []string{`D:\work\project\tools\git.exe`, `C:\Git\cmd\git.exe`, `D:\work\project\bin\git.exe`},
			want:    []string{`tools`},
		},
		{
			name:    "a root-relative entry, from the root of the working directory's drive",
			workDir: workDir,
			path:    `C:\Windows;\cmd`,
			files:   []string{`D:\cmd\git.exe`},
			want:    []string{`\cmd`},
		},
		{
			name:    "a relative entry, from the working directory",
			workDir: workDir,
			path:    `"tools\git";C:\Windows`,
			files:   []string{`D:\work\project\tools\git\git.exe`},
			want:    []string{`tools\git`},
		},
		{
			name:    "relative entries with dots, in PATH's order",
			workDir: workDir,
			path:    `..\shared;.\bin\`,
			files:   []string{`D:\work\project\bin\git.exe`, `D:\work\shared\git.exe`},
			want:    []string{`..\shared`, `.\bin\`},
		},
		{
			name:    "a relative entry without a git",
			workDir: workDir,
			path:    `tools`,
			files:   []string{`D:\work\project\tools\bash.exe`, `tools\git.exe`},
		},
		{
			name:    "a drive-relative entry on the working directory's drive, from the working directory",
			workDir: workDir,
			path:    `d:tools`,
			files:   []string{`D:\work\project\tools\git.exe`},
			want:    []string{`d:tools`},
		},
		{
			// Which folder is current on another drive is not known, so
			// the entry resolves from that drive's root, Windows' default.
			name:    "a drive-relative entry on another drive, from that drive's root",
			workDir: workDir,
			path:    `E:tools`,
			files:   []string{`E:\tools\git.exe`},
			want:    []string{`E:tools`},
		},
		{
			// A drive alone names the folder current on it.
			name:    "a bare drive, the working directory's, from the working directory",
			workDir: workDir,
			path:    `d:`,
			files:   []string{`D:\work\project\git.exe`},
			want:    []string{`d:`},
		},
		{
			name:    "a bare drive, another, from that drive's root",
			workDir: workDir,
			path:    `E:`,
			files:   []string{`E:\git.exe`},
			want:    []string{`E:`},
		},
		{
			name:    "a working directory on a share",
			workDir: `\\host\share\project`,
			path:    `\cmd;tools`,
			files:   []string{`\\host\share\cmd\git.exe`, `\\host\share\project\tools\git.exe`},
			want:    []string{`\cmd`, `tools`},
		},
		{
			name:    "a working directory that is not absolute",
			workDir: `work\project`,
			path:    `tools;\cmd`,
			files:   []string{`work\project\tools\git.exe`, `tools\git.exe`, `\cmd\git.exe`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string {
				if key == "PATH" {
					return tt.path
				}
				return ""
			}
			exists := func(path string) bool { return slices.Contains(tt.files, path) }

			got := install.RelativeGitEntries(getenv, exists, tt.workDir)

			if !slices.Equal(got, tt.want) {
				t.Errorf("RelativeGitEntries = %q, want %q", got, tt.want)
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
