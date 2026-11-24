// Package cli implements the loadline command line.
package cli

import (
	"context"
	"fmt"
	"io"
)

// Version is the release version. The build stamps it with
// -ldflags "-X github.com/dsj7419/loadline/internal/cli.Version=<version>".
var Version = "dev"

// IO carries the standard streams a command reads from and writes to.
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const usage = `Usage: loadline <command>

Commands:
  serve      Run the dashboard server for this machine's sessions
  status     Summarize accounts and sessions from the running server
  open       Open the dashboard in the browser, starting the server if need be
  stop       Stop the running server
  hook       Forward the Claude Code hook event on stdin; prints nothing
  tap        Pass a status line's input through and forward it
  install    Wire Loadline into Claude Code's settings
  uninstall  Remove Loadline from Claude Code's settings
  doctor     Check the setup, the server and the feeds that reach it
  version    Print the version
  help       Show this help
`

// Run executes the command named by args[0] and returns the process exit
// code: 0 on success, 1 on failure and 2 for a usage error.
func Run(ctx context.Context, args []string, stdio IO) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stdio.Err, usage)
		return exitUsage
	}

	switch args[0] {
	case "serve":
		return RunServe(ctx, args[1:], stdio)
	case "status":
		return runStatusCommand(ctx, args[1:], stdio)
	case "open":
		return runOpenCommand(ctx, args[1:], stdio)
	case "stop":
		return runStopCommand(ctx, args[1:], stdio)
	case "hook":
		return runHookCommand(ctx, stdio)
	case "tap":
		return runTapCommand(ctx, args[1:], stdio)
	case "install":
		return runInstall(ctx, args[1:], stdio)
	case "uninstall":
		return runUninstall(args[1:], stdio)
	case "doctor":
		return runDoctor(ctx, args[1:], stdio)
	case "version":
		return runVersionCommand(args[1:], stdio)
	case "help", "-h", "--help":
		return printStdout(stdio, usage)
	default:
		_, _ = fmt.Fprintf(stdio.Err, "loadline: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

// runVersionCommand prints the version. It takes no arguments.
func runVersionCommand(args []string, stdio IO) int {
	if code, ok := parseFlags(newFlagSet("version", stdio), args); !ok {
		return code
	}
	return printStdout(stdio, "loadline "+Version+"\n")
}

// printStdout writes text to stdout and maps a failed write to exit code 1.
func printStdout(stdio IO, text string) int {
	if _, err := io.WriteString(stdio.Out, text); err != nil {
		_, _ = fmt.Fprintf(stdio.Err, "loadline: write output: %v\n", err)
		return exitFailure
	}
	return exitOK
}
