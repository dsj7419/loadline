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
		{
			// Outside an MSYS shell, Git's own folders on PATH are where
			// the user put them, and the first git counts as it is.
			name: "windows outside an MSYS shell, with Git's mingw64 folder first on PATH",
			goos: "windows",
			env:  map[string]string{"PATH": `D:\Git\mingw64\bin;D:\Git\usr\bin;D:\Git\cmd`},
			files: []string{
				`D:\Git\mingw64\bin\git.exe`, `D:\Git\usr\bin\msys-2.0.dll`,
				`D:\Git\cmd\git.exe`, `D:\Git\bin\bash.exe`,
			},
			want: install.ShellPowerShell,
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

// userGit is where Git for Windows installs without administrator rights.
const userGit = `C:\Users\example\AppData\Local\Programs\Git`

// gitForWindows are the files of a Git for Windows at root that the shell
// detection may look for: its git.exe in cmd, bin and mingw64\bin, its
// bash.exe in bin and usr\bin, and the MSYS runtime beside the latter.
func gitForWindows(root string) []string {
	return []string{
		root + `\cmd\git.exe`,
		root + `\bin\git.exe`, root + `\bin\bash.exe`,
		root + `\mingw64\bin\git.exe`,
		root + `\usr\bin\bash.exe`, root + `\usr\bin\msys-2.0.dll`,
	}
}

// inGitBash is the environment a program started from Git Bash, which runs
// from the Git for Windows at root, sees when every other terminal starts it
// with windowsPath: the folders Git Bash puts in front of PATH, and behind
// it, written as Windows paths, and the variables Git Bash sets.
func inGitBash(root, windowsPath string) map[string]string {
	return map[string]string{
		"MSYSTEM": "MINGW64",
		"EXEPATH": root + `\bin`,
		"PATH": `C:\Users\example\bin;` + root + `\mingw64\bin;` + root + `\usr\local\bin;` + root + `\usr\bin;` +
			windowsPath + `;` + root + `\usr\bin\vendor_perl;` + root + `\usr\bin\core_perl`,
	}
}

// onWindowsDisk reports whether one of files exists, matching names without
// regard to case, as Windows does.
func onWindowsDisk(files []string) func(string) bool {
	return func(path string) bool {
		return slices.ContainsFunc(files, func(file string) bool { return strings.EqualFold(file, path) })
	}
}

// Git Bash, and any other MSYS shell, puts folders of its own in front of
// PATH, among them one with a git.exe and no bin\bash.exe beside it. Install
// and doctor, typed there or run through Claude Code's own Bash tool, must
// choose the shell that every other terminal, and 0.1.0, chooses on the same
// machine: a status line quoted for one shell is a syntax error under the
// other.
func TestDetectShell_TheTerminalInstallRunsInDoesNotChangeTheShell(t *testing.T) {
	const windowsPath = `C:\Windows\system32;C:\Windows`
	userGitPath := windowsPath + `;` + userGit + `\cmd`
	withoutExePath := inGitBash(userGit, userGitPath)
	delete(withoutExePath, "EXEPATH")
	gitBashInGitBash := inGitBash(userGit, inGitBash(userGit, userGitPath)["PATH"])
	spelledOtherwise := inGitBash(userGit, userGitPath)
	spelledOtherwise["PATH"] = `c:/users/example/appdata/local/programs/git/MINGW64/BIN/;` +
		`C:/Users/Example/AppData/Local/Programs/Git/usr/local/bin/;C:\USERS\EXAMPLE\APPDATA\LOCAL\PROGRAMS\GIT\USR\BIN\\;` + userGitPath
	msys2 := []string{`C:\msys64\usr\bin\bash.exe`, `C:\msys64\usr\bin\msys-2.0.dll`}
	inMSYS2 := func(windowsPath string) map[string]string {
		return map[string]string{
			"MSYSTEM": "UCRT64",
			"PATH":    `C:\msys64\ucrt64\bin;C:\msys64\usr\local\bin;C:\msys64\usr\bin;` + windowsPath,
		}
	}
	tests := []struct {
		name  string
		files []string
		// windowsPath is PATH as PowerShell, cmd and the Start menu start
		// a program with, and msys the environment of the same program
		// started from an MSYS shell.
		windowsPath string
		msys        map[string]string
		want        install.Shell
	}{
		{
			name:        "Git installed for the user, with its cmd folder later on PATH",
			files:       gitForWindows(userGit),
			windowsPath: userGitPath,
			msys:        inGitBash(userGit, userGitPath),
			want:        install.ShellBash,
		},
		{
			name:        "Git installed for the user, with its Unix tools on PATH as well",
			files:       gitForWindows(userGit),
			windowsPath: userGitPath + `;` + userGit + `\mingw64\bin;` + userGit + `\usr\bin`,
			msys:        inGitBash(userGit, userGitPath+`;`+userGit+`\mingw64\bin;`+userGit+`\usr\bin`),
			want:        install.ShellBash,
		},
		{
			name:        "a portable Git on another drive, with its bin folder on PATH",
			files:       gitForWindows(`D:\PortableGit`),
			windowsPath: windowsPath + `;D:\PortableGit\bin`,
			msys:        inGitBash(`D:\PortableGit`, windowsPath+`;D:\PortableGit\bin`),
			want:        install.ShellBash,
		},
		{
			name:        "Git installed for the user, not on PATH",
			files:       gitForWindows(userGit),
			windowsPath: windowsPath,
			msys:        inGitBash(userGit, windowsPath),
			want:        install.ShellPowerShell,
		},
		{
			name:        "Git Bash without EXEPATH",
			files:       gitForWindows(userGit),
			windowsPath: userGitPath,
			msys:        withoutExePath,
			want:        install.ShellBash,
		},
		{
			name:        "Git Bash started from Git Bash",
			files:       gitForWindows(userGit),
			windowsPath: userGitPath,
			msys:        gitBashInGitBash,
			want:        install.ShellBash,
		},
		{
			name:        "Git Bash's folders written with other slashes, case and trailing separators",
			files:       gitForWindows(userGit),
			windowsPath: userGitPath,
			msys:        spelledOtherwise,
			want:        install.ShellBash,
		},
		{
			// MSYS2's own git, in usr\bin, has bash.exe in the folder that
			// the bin beside it names.
			name:        "MSYS2 with its own git, without Git for Windows",
			files:       append([]string{`C:\msys64\usr\bin\git.exe`}, msys2...),
			windowsPath: windowsPath,
			msys:        inMSYS2(windowsPath),
			want:        install.ShellPowerShell,
		},
		{
			name:        "MSYS2 with its MinGW git, beside a Git for Windows installed for the user",
			files:       append(append([]string{`C:\msys64\ucrt64\bin\git.exe`}, msys2...), gitForWindows(userGit)...),
			windowsPath: userGitPath,
			msys:        inMSYS2(userGitPath),
			want:        install.ShellBash,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exists := onWindowsDisk(tt.files)

			fromWindows := install.DetectShellOn("windows", func(name string) string {
				if name == "PATH" {
					return tt.windowsPath
				}
				return ""
			}, exists)
			fromMSYS := install.DetectShellOn("windows", func(name string) string { return tt.msys[name] }, exists)

			if fromWindows != tt.want {
				t.Errorf("typed in PowerShell, install chose shell %d, want %d", fromWindows, tt.want)
			}
			if fromMSYS != fromWindows {
				t.Errorf("typed in an MSYS shell, install chose shell %d, and typed in PowerShell shell %d, on one machine",
					fromMSYS, fromWindows)
			}
		})
	}
}

// 0.1.0 wrote the Git Bash form of the status line, which a Claude Code
// started outside Git Bash runs with Git Bash. An upgrade typed in Git Bash,
// or run through Claude Code's own Bash tool, must leave it as it is, not
// rewrite it into the PowerShell form that Git Bash refuses.
func TestPlan_AnUpgradeTypedInGitBashKeepsTheWorkingStatusLine(t *testing.T) {
	env := inGitBash(userGit, `C:\Windows\system32;C:\Windows;`+userGit+`\cmd`)
	const exe = "C:/Users/example/AppData/Local/loadline/bin/loadline.exe"
	working := `"` + exe + `" tap | ~/.claude/statusline.sh`
	settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, working) + `}}`)
	shell := install.DetectShellOn("windows", install.SettingsEnv(settings, func(name string) string { return env[name] }),
		onWindowsDisk(gitForWindows(userGit)))

	out, _, _ := plan(t, settings, install.Options{Exe: exe, Port: 8289, Token: "token", Shell: shell})

	if got := statusCommand(t, out); got != working {
		t.Errorf("install made the status line %q, want it left as %q", got, working)
	}
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
		// msystem is MSYSTEM, which an MSYS shell such as Git Bash sets.
		msystem string
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
		{
			// The shell detection passes over the folders an MSYS shell puts
			// in front of PATH for itself, so the list does not end there.
			name:    "relative entries past the folders Git Bash puts in front of PATH",
			workDir: workDir,
			path:    `C:\Git\mingw64\bin;C:\Git\usr\bin;tools;C:\Git\cmd;bin`,
			msystem: "MINGW64",
			files: []string{
				`C:\Git\mingw64\bin\git.exe`, `C:\Git\usr\bin\msys-2.0.dll`,
				`D:\work\project\tools\git.exe`, `C:\Git\cmd\git.exe`, `D:\work\project\bin\git.exe`,
			},
			want: []string{`tools`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string {
				return map[string]string{"PATH": tt.path, "MSYSTEM": tt.msystem}[key]
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
