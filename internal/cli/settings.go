package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/install"
)

// This file holds what install and uninstall share: both plan an edit of one
// or more Claude Code settings files, show it, ask, and write it.

// emptySettings stands in for a settings file that does not exist yet.
var emptySettings = []byte("{}\n")

// editFlags are the flags of a command that edits settings files.
type editFlags struct {
	dirs   configDirs
	dryRun bool
	yes    bool
}

func addEditFlags(flags *flag.FlagSet) *editFlags {
	f := &editFlags{}
	flags.Var(&f.dirs, "config-dir", "edit `dir`/settings.json; repeat for several Claude Code config dirs (default ~/.claude)")
	flags.BoolVar(&f.dryRun, "dry-run", false, "show the changes and write nothing")
	flags.BoolVar(&f.yes, "yes", false, "apply the changes without asking")
	return f
}

// configDirs collects every --config-dir.
type configDirs []string

func (d *configDirs) String() string { return strings.Join(*d, ", ") }

func (d *configDirs) Set(dir string) error {
	*d = append(*d, dir)
	return nil
}

// newFlagSet returns the flag set of the command name, which writes its
// errors and its usage to stderr. A command without flags is used by its name
// alone.
func newFlagSet(name string, stdio IO) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stdio.Err)
	flags.Usage = func() {
		hasFlags := false
		flags.VisitAll(func(*flag.Flag) { hasFlags = true })
		if !hasFlags {
			_, _ = fmt.Fprintf(stdio.Err, "Usage: loadline %s\n", name)
			return
		}
		_, _ = fmt.Fprintf(stdio.Err, "Usage: loadline %s [flags]\n\n", name)
		flags.PrintDefaults()
	}
	return flags
}

// parseFlags parses args, and when the command should not go on, returns
// false with the exit code: 0 after -h, 2 for a usage error, which is
// followed by the usage, as the flag package does for a flag it does not
// know.
func parseFlags(flags *flag.FlagSet, args []string) (int, bool) {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, false
		}
		return exitUsage, false
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(flags.Output(), "loadline %s: unexpected argument %q\n", flags.Name(), flags.Arg(0))
		flags.Usage()
		return exitUsage, false
	}
	return exitOK, true
}

// settingsFiles returns the settings.json in each config dir, or in ~/.claude
// when none is given, once each however the dir is spelled. Each dir must
// exist: a missing one is more likely a typo than an account.
func settingsFiles(dirs []string) ([]string, error) {
	if len(dirs) == 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find the home directory: %w", err)
		}
		dirs = []string{string(config.ResolveConfigDir("", home))}
	}

	files := make([]string, 0, len(dirs))
	seen := map[string]bool{}
	for _, dir := range dirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve config dir %s: %w", dir, err)
		}
		key := abs
		if runtime.GOOS == "windows" {
			key = strings.ToLower(abs)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		info, err := os.Stat(abs)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("config dir %s does not exist", abs)
		case err != nil:
			return nil, fmt.Errorf("read config dir %s: %w", abs, err)
		case !info.IsDir():
			return nil, fmt.Errorf("config dir %s is not a directory", abs)
		}
		files = append(files, filepath.Join(abs, "settings.json"))
	}
	return files, nil
}

// planner plans the edit of one settings file's contents.
type planner func(settings []byte) (out []byte, changes []install.Change, warnings []string, err error)

// settingsEdit is a settings file and what a command will write to it.
type settingsEdit struct {
	path     string
	original []byte // nil when the file does not exist
	out      []byte
	changes  []install.Change
	warnings []string
}

func (e settingsEdit) pending() bool { return install.HasEdits(e.changes) }

func (e *settingsEdit) plan(plan planner) error {
	input := e.original
	if input == nil {
		input = emptySettings
	}
	out, changes, warnings, err := plan(input)
	if err != nil {
		return fmt.Errorf("%s: %w", e.path, err)
	}
	e.out, e.changes, e.warnings = out, changes, warnings
	return nil
}

func planEdits(files []string, plan planner) ([]settingsEdit, error) {
	edits := make([]settingsEdit, 0, len(files))
	for _, path := range files {
		original, err := os.ReadFile(filepath.Clean(path))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			original = nil
		case err != nil:
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		edit := settingsEdit{path: path, original: original}
		if err := edit.plan(plan); err != nil {
			return nil, err
		}
		edits = append(edits, edit)
	}
	return edits, nil
}

type reviewOptions struct {
	dryRun bool
	yes    bool
	// nothingToDo is printed when nothing needs a change.
	nothingToDo string
	// binary lists what installing the binary at binaryPath changes.
	binary     []install.Change
	binaryPath string
}

// review shows the changes and their warnings and decides whether to make
// them. When the command should stop instead, because there is nothing to
// change, the run is dry or the user declines, it returns false with the
// exit code.
func review(out *printer, stdio IO, edits []settingsEdit, o reviewOptions) (int, bool) {
	showChanges(out, o.binaryPath, o.binary)
	for _, e := range edits {
		show(out, e)
		for _, warning := range e.warnings {
			_, _ = fmt.Fprintf(stdio.Err, "loadline: warning: %s: %s\n", e.path, warning)
		}
	}

	switch {
	case out.err != nil:
		// Nobody saw the changes, so they must not be made.
		return out.exit(stdio, exitFailure), false
	case upToDate(o.binary, edits):
		out.printf("%s\n", o.nothingToDo)
		return out.exit(stdio, exitOK), false
	case o.dryRun:
		out.printf("Dry run: nothing was written.\n")
		return out.exit(stdio, exitOK), false
	case o.yes:
		return exitOK, true
	}

	yes, err := confirm(out, stdio.In)
	if err != nil {
		return fail(stdio, err), false
	}
	if !yes {
		out.printf("Nothing was changed.\n")
		return out.exit(stdio, exitFailure), false
	}
	return exitOK, true
}

// upToDate reports whether neither the binary nor any settings file needs a
// change.
func upToDate(binary []install.Change, edits []settingsEdit) bool {
	return !install.HasEdits(binary) && !slices.ContainsFunc(edits, settingsEdit.pending)
}

// show prints an edit's changes as a list and as a unified diff, with the
// ingest token masked.
func show(out *printer, e settingsEdit) {
	if len(e.changes) == 0 {
		return
	}
	showChanges(out, e.path, e.changes)
	out.printf("%s", install.Diff(e.path, install.MaskToken(e.original), install.MaskToken(e.out)))
}

// showChanges prints changes as a list under header.
func showChanges(out *printer, header string, changes []install.Change) {
	if len(changes) == 0 {
		return
	}
	out.printf("%s\n", header)
	list := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, c := range changes {
		_, _ = fmt.Fprintf(list, "  %s\t%s\t%s\n", c.Action, c.Path, install.MaskToken([]byte(c.Detail)))
	}
	_ = list.Flush()
}

// confirm asks whether to apply the changes and reports whether the answer
// was y or yes.
func confirm(out *printer, in io.Reader) (bool, error) {
	out.printf("Apply these changes? [y/N] ")
	if out.err != nil {
		return false, fmt.Errorf("write output: %w", out.err)
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read the answer: %w", err)
	}
	if !strings.HasSuffix(line, "\n") {
		out.printf("\n")
	}
	answer := strings.TrimSpace(line)
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}

// writeEdits applies each edit that changes its file and says where the
// backup went.
func writeEdits(out *printer, edits []settingsEdit) error {
	for _, e := range edits {
		if !e.pending() {
			continue
		}
		backup, err := install.Apply(e.path, e.original, e.out, time.Now())
		if errors.Is(err, install.ErrChangedSincePlan) {
			return fmt.Errorf("%w; nothing was written to it, so run the command again", err)
		}
		if err != nil {
			return err
		}
		if backup == "" {
			out.printf("Created %s\n", e.path)
		} else {
			out.printf("Updated %s (backup: %s)\n", e.path, backup)
		}
	}
	return nil
}

// printer writes to w and keeps the first error, so a command can print
// freely and check once.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) Write(b []byte) (int, error) {
	if p.err != nil {
		return 0, p.err
	}
	n, err := p.w.Write(b)
	p.err = err
	return n, err
}

func (p *printer) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(p, format, args...)
}

// exit returns code, or a failure when output could not be written.
func (p *printer) exit(stdio IO, code int) int {
	if p.err != nil {
		return fail(stdio, fmt.Errorf("write output: %w", p.err))
	}
	return code
}

func fail(stdio IO, err error) int {
	_, _ = fmt.Fprintf(stdio.Err, "loadline: %v\n", err)
	return exitFailure
}
