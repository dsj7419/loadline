// Command fakeclaude stands in for Claude Code's claude in the tests that
// run real Loadline processes, so that the real claude is never run. It
// answers the commands Loadline runs: --version, auth status --json, and
// agents --json --all, which prints agents.json from the folder fakeclaude
// is in, or no session when there is none.
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	switch command := strings.Join(args, " "); command {
	case "--version":
		_, _ = fmt.Fprintln(stdout, "2.1.330 (Claude Code)")
	case "auth status --json":
		_, _ = fmt.Fprintln(stdout, `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"e2e@example.com","subscriptionType":"max"}`)
	case "agents --json --all":
		agents, err := readAgents()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "fakeclaude: %v\n", err)
			return 1
		}
		_, _ = stdout.Write(agents)
	default:
		_, _ = fmt.Fprintf(stderr, "fakeclaude: unexpected command %q\n", command)
		return 2
	}
	return 0
}

// readAgents reads agents.json beside the executable, or no session when it
// is not there.
func readAgents() ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	agents, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "agents.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return []byte("[]"), nil
	}
	return agents, err
}
