package install

import (
	"bytes"
	"runtime"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

// Shell is the shell Claude Code runs the status-line command with, which
// decides how install has to quote it.
type Shell uint8

// The zero Shell is not valid, so that a caller has to choose one: a command
// quoted for the wrong shell breaks the user's status line.
const (
	// ShellBash is Git Bash on Windows and the shell everywhere else.
	ShellBash Shell = iota + 1
	// ShellPowerShell is what Claude Code uses on Windows without Git Bash.
	ShellPowerShell
)

// DetectShell reports the shell Claude Code uses on this system. getenv
// reads an environment variable, and should see the settings file's env
// through SettingsEnv; exists reports whether a file exists.
func DetectShell(getenv func(string) string, exists func(string) bool) Shell {
	return detectShell(runtime.GOOS, getenv, exists)
}

// SettingsEnv returns a getenv that reads the env object of settings first
// and getenv after, as Claude Code applies a settings file's env on top of
// the environment it starts in. Names match without regard to case, as they
// do on Windows, where the shell depends on them.
func SettingsEnv(settings []byte, getenv func(string) string) func(string) string {
	var env gjson.Result
	if body := bytes.TrimPrefix(settings, []byte(utf8BOM)); gjson.ValidBytes(body) {
		env = gjson.GetBytes(body, "env")
	}
	return func(name string) string {
		value, found := "", false
		env.ForEach(func(key, v gjson.Result) bool {
			if strings.EqualFold(key.Str, name) && v.Type == gjson.String {
				value, found = v.Str, true
				return false
			}
			return true
		})
		if found {
			return value
		}
		return getenv(name)
	}
}

// detectShell follows Claude Code's own lookup. On Windows, Claude Code uses
// Git Bash when it finds one before it looks on PATH, or in the bin folder
// beside the folder that holds the first git.exe on PATH, and PowerShell
// otherwise.
func detectShell(goos string, getenv func(string) string, exists func(string) bool) Shell {
	if goos != "windows" {
		return ShellBash
	}
	if GitBashBeforePath(getenv, exists) || gitBashOnPath(getenv, exists) {
		return ShellBash
	}
	return ShellPowerShell
}

// GitBashBeforePath reports whether Claude Code on Windows finds Git Bash
// before it looks on PATH: when CLAUDE_CODE_GIT_BASH_PATH names an existing
// bash or sh, or when Git Bash is at C:\Program Files\Git\bin\bash.exe or
// C:\Program Files (x86)\Git\bin\bash.exe. Claude Code passes over a
// CLAUDE_CODE_GIT_BASH_PATH that names anything else. getenv and exists are
// as DetectShell takes them.
func GitBashBeforePath(getenv func(string) string, exists func(string) bool) bool {
	if bash := getenv("CLAUDE_CODE_GIT_BASH_PATH"); isBashName(bash) && exists(bash) {
		return true
	}
	return slices.ContainsFunc(programFilesGitBash[:], exists)
}

// isBashName reports whether path names a shell Claude Code accepts in
// CLAUDE_CODE_GIT_BASH_PATH.
func isBashName(path string) bool {
	base := path[strings.LastIndexAny(path, `\/`)+1:]
	for _, name := range []string{"bash.exe", "sh.exe", "bash", "sh"} {
		if strings.EqualFold(base, name) {
			return true
		}
	}
	return false
}

// programFilesGitBash are where Claude Code looks for Git Bash before PATH,
// after CLAUDE_CODE_GIT_BASH_PATH. They are literal paths: Claude Code looks
// there whatever ProgramFiles says, and nowhere else, so a per-user install
// in %LOCALAPPDATA%\Programs\Git counts only through its git on PATH.
var programFilesGitBash = [...]string{`C:\Program Files\Git\bin\bash.exe`, `C:\Program Files (x86)\Git\bin\bash.exe`}

// gitBashOnPath reports whether bash.exe is where Git for Windows keeps it
// for the first git.exe on PATH: in the bin folder beside the one that holds
// git.exe. Only that first git counts, as it does for Claude Code.
//
// Only absolute entries are searched. Claude Code resolves an entry such as
// cmd, \cmd or C:cmd against each session's own working directory, so what
// it leads to depends on where each session runs, which install cannot know.
// loadline doctor warns of such an entry that leads to a git.
//
// PATH is read as searchedEntries returns it, the same from any terminal.
func gitBashOnPath(getenv func(string) string, exists func(string) bool) bool {
	for _, entry := range searchedEntries(getenv, exists) {
		if !isWindowsAbs(entry) {
			continue
		}
		volume, rest := splitWindowsVolume(entry)
		if holdsGit(exists, volume, rest) {
			return exists(joinWindows(volume, rest, "..", "bin", "bash.exe"))
		}
	}
	return false
}

// RelativeGitEntries returns the entries of the Windows PATH that getenv
// reads which are not absolute, such as cmd, \cmd or D:cmd, and hold git.exe
// when resolved against workDir, in PATH's order. The shell detection skips
// them, since Claude Code resolves them against each session's own working
// directory. The list ends at the first absolute entry that holds git.exe
// among those the shell detection searches: Claude Code stops at the first
// git it finds, so it never reaches the entries after that one. It returns
// nil when workDir is not an absolute Windows path.
func RelativeGitEntries(getenv func(string) string, exists func(string) bool, workDir string) []string {
	if !isWindowsAbs(workDir) {
		return nil
	}
	var entries []string
	for _, entry := range searchedEntries(getenv, exists) {
		if isWindowsAbs(entry) {
			if volume, rest := splitWindowsVolume(entry); holdsGit(exists, volume, rest) {
				return entries
			}
			continue
		}
		if volume, rest := resolveWindows(workDir, entry); holdsGit(exists, volume, rest) {
			entries = append(entries, entry)
		}
	}
	return entries
}

// searchedEntries returns the entries of the Windows PATH that getenv reads,
// as the shell detection searches them. Inside an MSYS shell, such as Git
// Bash, it leaves out the folders that shell puts in front of PATH for its
// own use (msysFolders). Among them is Git's mingw64\bin, with a git.exe
// that has no bin\bash.exe beside its folder, and a Claude Code started from
// PowerShell, cmd or the Start menu sees none of them. Without them, install
// and doctor answer for that Claude Code from whichever terminal they run
// in, and a status line written from Git Bash keeps working there.
func searchedEntries(getenv func(string) string, exists func(string) bool) []string {
	entries := pathEntries(getenv("PATH"))
	own := msysFolders(getenv("MSYSTEM"), entries, exists)
	if len(own) == 0 {
		return entries
	}
	return slices.DeleteFunc(entries, func(entry string) bool {
		if !isWindowsAbs(entry) {
			return false
		}
		folder := cleanWindows(entry)
		return slices.ContainsFunc(own, func(o string) bool { return strings.EqualFold(folder, o) })
	})
}

// msysRuntime is the MSYS runtime, which every MSYS shell, Git Bash's among
// them, keeps in the usr\bin folder under its root.
const msysRuntime = "msys-2.0.dll"

// msysFolders returns the folders an MSYS shell puts in front of PATH for its
// own use, as cleanWindows writes them: the bin folder of the environment
// msystem names (mingw64\bin for MINGW64), usr\local\bin and usr\bin, under
// the shell's root. msystem is MSYSTEM, which every MSYS shell sets, and
// msysFolders returns nil when it is empty.
//
// The root is the folder above the first usr\bin among entries that holds
// the MSYS runtime, since the shell puts its own usr\bin ahead of the PATH
// it was started with. That finds it wherever Git for Windows is installed,
// and for MSYS2 too, which does not set EXEPATH as Git's own launcher does.
// It returns nil when no entry is such a folder.
func msysFolders(msystem string, entries []string, exists func(string) bool) []string {
	if msystem == "" {
		return nil
	}
	for _, entry := range entries {
		if !isWindowsAbs(entry) {
			continue
		}
		usrBin := cleanWindows(entry)
		root, ok := cutSuffixFold(usrBin, `\usr\bin`)
		if ok && exists(usrBin+`\`+msysRuntime) {
			return []string{root + `\` + msystem + `\bin`, root + `\usr\local\bin`, usrBin}
		}
	}
	return nil
}

// cutSuffixFold returns s without suffix, matched without regard to case,
// and whether s ends with it.
func cutSuffixFold(s, suffix string) (before string, found bool) {
	if len(s) < len(suffix) || !strings.EqualFold(s[len(s)-len(suffix):], suffix) {
		return s, false
	}
	return s[:len(s)-len(suffix)], true
}

// holdsGit reports whether the folder rest on volume holds git.exe.
func holdsGit(exists func(string) bool, volume, rest string) bool {
	return exists(joinWindows(volume, rest, "git.exe"))
}

// pathEntries returns the folders the Windows PATH list names, without their
// quotes, surrounding spaces or empty entries.
func pathEntries(list string) []string {
	var entries []string
	for entry := range strings.SplitSeq(list, ";") {
		if entry = strings.TrimSpace(strings.Trim(entry, `"`)); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}
