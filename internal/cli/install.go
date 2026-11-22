package cli

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/install"
)

const (
	// restartNotice ends a successful install: a session reads its hooks,
	// status line and environment when it starts.
	restartNotice = "Claude Code sessions that are already running will show up fully after you restart them."
	// tokenPlaceholder stands in for the ingest token before there is one. The
	// token is created only when install writes the settings, never in a dry
	// run.
	tokenPlaceholder = "<created-on-install>" //nolint:gosec // G101: a placeholder shown instead of a token, not a credential
	// minPort and maxPort are the ports the Loadline config accepts.
	minPort = 1024
	maxPort = 65535
)

// runInstall wires Loadline into each settings file. It shows the changes
// first and asks unless --yes; --dry-run writes nothing at all: not the
// settings, not the binary, not the token.
func runInstall(args []string, stdio IO) int {
	flags := newFlagSet("install", stdio)
	edit := addEditFlags(flags)
	port := flags.Int("port", 0, "send telemetry to the dashboard on `port` (default from the Loadline config)")
	if code, ok := parseFlags(flags, args); !ok {
		return code
	}
	if isSet(flags, "port") && (*port < minPort || *port > maxPort) {
		_, _ = fmt.Fprintf(stdio.Err, "loadline install: --port %d is outside %d-%d\n", *port, minPort, maxPort)
		return exitUsage
	}

	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stdio, err)
	}
	opts, err := installOptions(paths, *port)
	if err != nil {
		return fail(stdio, err)
	}
	files, err := settingsFiles(edit.dirs)
	if err != nil {
		return fail(stdio, err)
	}
	edits, err := planEdits(files, installPlanner(opts))
	if err != nil {
		return fail(stdio, err)
	}

	out := &printer{w: stdio.Out}
	if code, proceed := review(out, stdio, edits, reviewOptions{
		dryRun: edit.dryRun, yes: edit.yes, nothingToDo: "Already installed.",
	}); !proceed {
		return code
	}

	if opts, err = prepareInstall(paths, opts); err != nil {
		return fail(stdio, err)
	}
	// The same plans again, now with the real token in place of the
	// placeholder the review showed masked.
	for i := range edits {
		if err := edits[i].plan(installPlanner(opts)); err != nil {
			return fail(stdio, err)
		}
	}
	if err := writeEdits(out, edits); err != nil {
		return fail(stdio, err)
	}
	out.printf("%s\n", restartNotice)
	return out.exit(stdio, exitOK)
}

func isSet(flags *flag.FlagSet, name string) bool {
	set := false
	flags.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

// installOptions describes the installation without creating any of it: the
// binary's planned path, and the existing token or a placeholder.
func installOptions(paths config.Paths, port int) (install.Options, error) {
	if port == 0 {
		cfg, err := config.Load(paths.ConfigFile)
		if err != nil {
			return install.Options{}, err
		}
		port = cfg.Port
	}
	token, err := existingToken(paths.TokenFile)
	if err != nil {
		return install.Options{}, err
	}
	return install.Options{
		Exe:   install.InstalledPath(paths.DataDir),
		Port:  port,
		Token: token,
		Shell: install.DetectShell(os.Getenv, isFile),
	}, nil
}

func installPlanner(opts install.Options) planner {
	return func(settings []byte) ([]byte, []install.Change, []string, error) {
		return install.Plan(settings, opts)
	}
}

// prepareInstall copies the running binary into place and creates the token,
// which the settings are about to refer to.
func prepareInstall(paths config.Paths, opts install.Options) (install.Options, error) {
	src, err := os.Executable()
	if err != nil {
		return opts, fmt.Errorf("find the running binary: %w", err)
	}
	if opts.Exe, err = install.SelfInstall(src, paths.DataDir); err != nil {
		return opts, err
	}
	token, err := config.EnsureToken(paths.TokenFile)
	if err != nil {
		return opts, err
	}
	opts.Token = string(token)
	return opts, nil
}

func existingToken(path string) (string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return tokenPlaceholder, nil
	}
	if err != nil {
		return "", fmt.Errorf("read the token: %w", err)
	}
	if token := strings.TrimSpace(string(data)); token != "" {
		return token, nil
	}
	return tokenPlaceholder, nil
}

func isFile(path string) bool {
	info, err := os.Stat(filepath.Clean(path))
	return err == nil && !info.IsDir()
}
