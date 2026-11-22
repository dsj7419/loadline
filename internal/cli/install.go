package cli

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/datadir"
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
)

// runInstall wires Loadline into each settings file and puts the running
// binary where the settings point. It shows the changes first and asks
// unless --yes; --dry-run writes nothing at all: not the settings, not the
// binary, not the token.
func runInstall(args []string, stdio IO) int {
	flags := newFlagSet("install", stdio)
	edit := addEditFlags(flags)
	port := flags.Int("port", 0, "send telemetry to the dashboard on `port` (default from the Loadline config)")
	if code, ok := parseFlags(flags, args); !ok {
		return code
	}
	if isSet(flags, "port") {
		if err := config.ValidatePort(*port); err != nil {
			_, _ = fmt.Fprintf(stdio.Err, "loadline install: --port: %v\n", err)
			return exitUsage
		}
	}

	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stdio, err)
	}
	// Refused before anything is shown or asked, since nothing could be
	// installed there.
	if err := datadir.Verify(paths.DataDir, trustFrom); err != nil {
		return fail(stdio, err)
	}
	// Put right before the token and the binary are read, even with nothing
	// else to change: the doctor sends the user here for a data directory
	// that lets others in, or keeps them out of either. A new one is made
	// only once the user has agreed.
	if _, err := os.Stat(paths.DataDir); err == nil && !edit.dryRun {
		if err := secureDataDir(paths.DataDir); err != nil {
			return fail(stdio, err)
		}
	}
	src, err := os.Executable()
	if err != nil {
		return fail(stdio, fmt.Errorf("find the running binary: %w", err))
	}
	binary, err := install.PlanSelfInstall(src, paths.DataDir)
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
		binary: binary, binaryPath: filepath.FromSlash(opts.Exe),
	}); !proceed {
		return code
	}

	if opts, err = prepareInstall(src, paths, opts); err != nil {
		return fail(stdio, err)
	}
	if install.HasEdits(binary) {
		out.printf("Installed %s\n", filepath.FromSlash(opts.Exe))
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
		// Without a home directory, Load refuses only an account that names
		// its config dir from home, and says so.
		home, _ := os.UserHomeDir()
		cfg, err := config.Load(paths.ConfigFile, home)
		if err != nil {
			return install.Options{}, err
		}
		port = cfg.Port
	}
	token, err := existingToken(paths.TokenFile)
	if err != nil {
		return install.Options{}, err
	}
	return install.Options{Exe: install.InstalledPath(paths.DataDir), Port: port, Token: token}, nil
}

// installPlanner plans each settings file for the shell Claude Code will run
// its status line with.
func installPlanner(opts install.Options) planner {
	return func(settings []byte) ([]byte, []install.Change, []string, error) {
		o := opts
		o.Shell = statusLineShell(settings)
		return install.Plan(settings, o)
	}
}

// statusLineShell is the shell Claude Code will run the status line of
// settings with, which the file's own env can change.
func statusLineShell(settings []byte) install.Shell {
	return install.DetectShell(install.SettingsEnv(settings, os.Getenv), isFile)
}

// prepareInstall makes the data directory private, copies the running binary
// into place and creates the token, which the settings are about to refer
// to.
func prepareInstall(src string, paths config.Paths, opts install.Options) (install.Options, error) {
	if err := secureDataDir(paths.DataDir); err != nil {
		return opts, err
	}
	exe, err := install.SelfInstall(src, paths.DataDir)
	if err != nil {
		return opts, err
	}
	token, err := config.EnsureToken(paths.TokenFile)
	if err != nil {
		return opts, err
	}
	opts.Exe, opts.Token = exe, string(token)
	return opts, nil
}

// existingToken returns the ingest token, held to the rule the server holds
// it to, or the placeholder when there is none yet.
func existingToken(path string) (string, error) {
	token, err := config.ReadToken(path)
	if errors.Is(err, fs.ErrNotExist) {
		return tokenPlaceholder, nil
	}
	if err != nil {
		return "", err
	}
	return string(token), nil
}

func isFile(path string) bool {
	info, err := os.Stat(filepath.Clean(path))
	return err == nil && !info.IsDir()
}
