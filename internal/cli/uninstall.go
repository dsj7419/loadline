package cli

import (
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/install"
)

// runUninstall takes Loadline's entries out of each settings file and
// restores the user's own status line. Like install it shows the changes and
// asks unless --yes, and --dry-run writes nothing.
func runUninstall(args []string, stdio IO) int {
	flags := newFlagSet("uninstall", stdio)
	edit := addEditFlags(flags)
	if code, ok := parseFlags(flags, args); !ok {
		return code
	}

	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stdio, err)
	}
	files, err := settingsFiles(edit.dirs)
	if err != nil {
		return fail(stdio, err)
	}
	exe := install.InstalledPath(paths.DataDir)
	edits, err := planEdits(files, func(settings []byte) ([]byte, []install.Change, []string, error) {
		out, changes, err := install.Revert(settings, exe)
		return out, changes, nil, err
	})
	if err != nil {
		return fail(stdio, err)
	}

	out := &printer{w: stdio.Out}
	if code, proceed := review(out, stdio, edits, reviewOptions{
		dryRun: edit.dryRun, yes: edit.yes, nothingToDo: "Nothing to remove.",
	}); !proceed {
		return code
	}
	if err := writeEdits(out, edits); err != nil {
		return fail(stdio, err)
	}
	return out.exit(stdio, exitOK)
}
