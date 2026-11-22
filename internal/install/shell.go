package install

import (
	"runtime"
	"strings"
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
// reads an environment variable and exists reports whether a file exists.
func DetectShell(getenv func(string) string, exists func(string) bool) Shell {
	return detectShell(runtime.GOOS, getenv, exists)
}

// detectShell follows Claude Code: on Windows it uses Git Bash when
// CLAUDE_CODE_GIT_BASH_PATH names it or Git for Windows is installed, and
// PowerShell otherwise.
func detectShell(goos string, getenv func(string) string, exists func(string) bool) Shell {
	if goos != "windows" || getenv("CLAUDE_CODE_GIT_BASH_PATH") != "" {
		return ShellBash
	}
	for _, bash := range gitBashCandidates(getenv, exists) {
		if exists(bash) {
			return ShellBash
		}
	}
	return ShellPowerShell
}

// gitBashCandidates lists where Git for Windows keeps bash.exe: beside the
// git.exe found on PATH, and in its standard install directories.
func gitBashCandidates(getenv func(string) string, exists func(string) bool) []string {
	var candidates []string
	for _, dir := range strings.Split(getenv("PATH"), ";") {
		dir = trimDir(strings.Trim(dir, `"`))
		if dir == "" || !exists(dir+`\git.exe`) {
			continue
		}
		if i := strings.LastIndexAny(dir, `\/`); i > 0 {
			candidates = append(candidates, dir[:i]+`\bin\bash.exe`)
		}
	}
	for _, location := range []struct{ env, dir string }{
		{env: "ProgramFiles", dir: `Git`},
		{env: "ProgramFiles(x86)", dir: `Git`},
		{env: "LOCALAPPDATA", dir: `Programs\Git`},
	} {
		if root := trimDir(getenv(location.env)); root != "" {
			candidates = append(candidates, root+`\`+location.dir+`\bin\bash.exe`)
		}
	}
	return candidates
}

func trimDir(dir string) string {
	return strings.TrimRight(strings.TrimSpace(dir), `\/`)
}
