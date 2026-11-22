// Package sessionenv takes out of an environment what belongs to the Claude
// Code session whose hook started Loadline. A hook runs with its session's
// environment, which carries that session's settings file's env: its
// account's API key or token, the ingest token in the OpenTelemetry headers,
// and the variables that tie a process to the session. The processes
// Loadline starts for itself, the server and the browser and claude for
// every account, have no use for any of it, and claude would take another
// account's credentials for its own.
package sessionenv

import (
	"runtime"
	"strings"
)

// sessionPrefixes start the names of the variables Strip removes. Claude
// Code reads its configuration and credentials from ANTHROPIC_* and
// CLAUDE_*, marks its child processes with CLAUDECODE, and exports
// telemetry as OTEL_* says. Cloud providers' own variables, such as AWS_*,
// stay: a Bedrock or Vertex account's own claude may need them, and without
// CLAUDE_CODE_USE_* no other account uses them.
var sessionPrefixes = []string{"ANTHROPIC_", "CLAUDE_", "CLAUDECODE", "OTEL_"}

// gitBashPathVar is the one Claude Code variable Strip keeps. It holds no
// credential, and Claude Code on Windows finds a Git Bash installed
// somewhere unusual only through it: without it, every claude Loadline runs
// would fail there.
const gitBashPathVar = "CLAUDE_CODE_GIT_BASH_PATH"

// Strip returns environ, in the form os.Environ gives, without the entries
// whose names start with ANTHROPIC_, CLAUDE_, CLAUDECODE or OTEL_, except
// CLAUDE_CODE_GIT_BASH_PATH. Windows treats variable names as
// case-insensitive, so there the names match in any case. It does not modify
// environ.
func Strip(environ []string) []string {
	return strip(environ, runtime.GOOS == "windows")
}

func strip(environ []string, foldCase bool) []string {
	kept := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if !isSessionVar(name, foldCase) {
			kept = append(kept, entry)
		}
	}
	return kept
}

func isSessionVar(name string, foldCase bool) bool {
	if foldCase {
		name = strings.ToUpper(name)
	}
	if name == gitBashPathVar {
		return false
	}
	for _, prefix := range sessionPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
