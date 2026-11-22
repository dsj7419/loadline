package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/doctor"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/install"
)

// runDoctor checks the setup against the settings.json in each config dir
// and prints a line per check. It exits 1 when any check fails, and changes
// nothing.
func runDoctor(ctx context.Context, args []string, stdio IO) int {
	flags := newFlagSet("doctor", stdio)
	var dirs configDirs
	flags.Var(&dirs, "config-dir", "check `dir`/settings.json; repeat for several Claude Code config dirs (default ~/.claude)")
	if code, ok := parseFlags(flags, args); !ok {
		return code
	}

	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stdio, err)
	}
	// Without a home directory, Load refuses only an account that names its
	// config dir from home, and says so.
	home, _ := os.UserHomeDir()
	cfg, err := config.Load(paths.ConfigFile, home)
	if err != nil {
		return fail(stdio, err)
	}
	files, err := settingsFiles(dirs)
	if err != nil {
		return fail(stdio, err)
	}

	claude := claudeRunner(cfg, home)
	checks := doctor.Run(ctx, doctor.Deps{
		SettingsFiles: files,
		Exe:           install.InstalledPath(paths.DataDir),
		Port:          cfg.Port,
		Shell:         statusLineShell,
		Client:        forward.Client{BaseURL: forward.LoopbackURL(cfg.Port)},
		ClaudeVersion: func(ctx context.Context) (string, error) {
			out, err := claude.Run(ctx, claude.DefaultDir, "--version")
			return string(out), err
		},
		TokenFile: paths.TokenFile,
		Now:       time.Now,
	})
	return printChecks(stdio, checks)
}

// printChecks prints each check as a line marked ✔, ! or ✖ and returns 1
// when any check failed.
func printChecks(stdio IO, checks []doctor.Check) int {
	var out strings.Builder
	code := exitOK
	for _, c := range checks {
		if c.Status == doctor.Fail {
			code = exitFailure
		}
		// Names and details hold file paths and what claude printed.
		_, _ = fmt.Fprintf(&out, "%s %s: %s\n", mark(c.Status), printable(c.Name), printable(c.Detail))
	}
	if printStdout(stdio, out.String()) != exitOK {
		return exitFailure
	}
	return code
}

func mark(l doctor.Level) string {
	switch l {
	case doctor.OK:
		return "✔"
	case doctor.Warn:
		return "!"
	default:
		return "✖"
	}
}
