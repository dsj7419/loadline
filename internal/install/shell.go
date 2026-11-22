package install

import (
	"bytes"
	"runtime"
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

// detectShell follows Claude Code: on Windows it uses Git Bash when
// CLAUDE_CODE_GIT_BASH_PATH names an existing bash or sh, or Git for Windows
// is installed, and PowerShell otherwise.
func detectShell(goos string, getenv func(string) string, exists func(string) bool) Shell {
	if goos != "windows" {
		return ShellBash
	}
	if bash := getenv("CLAUDE_CODE_GIT_BASH_PATH"); isBashName(bash) && exists(bash) {
		return ShellBash
	}
	for _, bash := range gitBashCandidates(getenv, exists) {
		if exists(bash) {
			return ShellBash
		}
	}
	return ShellPowerShell
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
