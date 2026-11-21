// Command loadline shows every running Claude Code session on this machine in
// a live dashboard served on the loopback interface.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/dsj7419/loadline/internal/cli"
)

func main() {
	os.Exit(run())
}

// run exists so the signal context is released before os.Exit, which skips
// deferred calls.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return cli.Run(ctx, os.Args[1:], cli.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
}
