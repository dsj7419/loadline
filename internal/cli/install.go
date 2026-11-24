package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/install"
	"github.com/dsj7419/loadline/internal/launch"
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
// binary, not the token. Once it has replaced the binary, it hands a server
// that answers on the configured port over to the new one (handOver).
func runInstall(ctx context.Context, args []string, stdio IO) int {
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
	code, proceed := review(out, stdio, edits, reviewOptions{
		dryRun: edit.dryRun, yes: edit.yes, nothingToDo: "Already installed.",
		binary: binary, binaryPath: filepath.FromSlash(opts.Exe),
	})
	// The binaries earlier upgrades moved aside go as housekeeping, apart
	// from the changes: the review neither shows nor counts them, so an
	// install with nothing else to do neither asks nor claims to install.
	switch {
	case code != exitOK:
		return code
	case edit.dryRun:
		noteAside(out, stdio, paths.DataDir)
		return out.exit(stdio, exitOK)
	case !proceed:
		// Already installed.
		removeAsideAndSay(out, stdio, paths.DataDir)
		return out.exit(stdio, exitOK)
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
	var stoppedOld bool
	var handoverErr error
	if install.HasEdits(binary) {
		// The port the hooks start the server on. Without a home directory,
		// the config's port and the defaults' are the same.
		home, _ := os.UserHomeDir()
		stoppedOld, handoverErr = handOver(ctx, out, handoverOptions{
			port: loadConfig(paths, home).Port, token: []byte(opts.Token), exe: filepath.FromSlash(opts.Exe),
			dataDir: paths.DataDir, spawn: spawnServer, elevated: isElevated(),
		})
	}
	if stoppedOld {
		removeAsideSettlingAndSay(out, stdio, paths.DataDir)
	} else {
		removeAsideAndSay(out, stdio, paths.DataDir)
	}
	out.printf("%s\n", restartNotice)
	if handoverErr != nil {
		return out.exit(stdio, fail(stdio, handoverErr))
	}
	return out.exit(stdio, exitOK)
}

// asideBinaries and removeAside are install's. Tests replace them, since
// nothing on disk makes finding or removing those binaries fail on demand.
var (
	asideBinaries = install.AsideBinaries
	removeAside   = install.RemoveAside
)

// spawnServer starts the server install hands over to, and isElevated says
// whether install runs elevated. Tests replace them.
var (
	spawnServer = launch.SpawnDetached
	isElevated  = processElevated
)

// removeAsideAndSay removes the binaries earlier upgrades moved aside in
// dataDir that nothing runs any more, and says which went. One that still
// runs goes on a later run, and needs no word. What fails is a warning: it
// never fails the install.
func removeAsideAndSay(out *printer, stdio IO, dataDir string) {
	removed, err := removeAside(dataDir)
	sayAsideRemoved(out, dataDir, removed)
	warnAside(stdio, err)
}

// sayAsideRemoved says which binaries earlier upgrades moved aside in
// dataDir went.
func sayAsideRemoved(out *printer, dataDir string, removed []string) {
	for _, path := range removed {
		out.printf("Removed %s, which an earlier upgrade replaced.\n", dataDirName(dataDir, path))
	}
}

// noteAside says, in a dry run, which binaries earlier upgrades moved aside
// in dataDir an install would remove.
func noteAside(out *printer, stdio IO, dataDir string) {
	found, err := asideBinaries(dataDir)
	for _, path := range found {
		out.printf("Note: %s, left by an earlier upgrade, goes once nothing runs it.\n", dataDirName(dataDir, path))
	}
	warnAside(stdio, err)
}

// warnAside reports, as a warning, what finding or removing those binaries
// could not do.
func warnAside(stdio IO, err error) {
	if err != nil {
		_, _ = fmt.Fprintf(stdio.Err, "loadline: warning: %v\n", err)
	}
}

// dataDirName is how path, in dataDir, is named to the user: from the data
// directory, as the README names the binary's folder.
func dataDirName(dataDir, path string) string {
	if name, err := filepath.Rel(dataDir, path); err == nil {
		return name
	}
	return path
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
