package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/datadir"
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
	files, err := settingsFiles(dirs)
	if err != nil {
		return fail(stdio, err)
	}
	// Without a home directory, Load refuses only an account that names its
	// config dir from home, and says so.
	home, _ := os.UserHomeDir()
	cfg, checks := doctorConfig(paths.ConfigFile, home)

	claude := claudeRunner(cfg, home)
	checks = append(checks, doctor.Run(ctx, doctor.Deps{
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
		DataDir:   paths.DataDir,
		CheckDataDir: func(dir string) (string, error) {
			return datadir.Check(dir, trustFrom)
		},
		Now: time.Now,
	})...)
	return printChecks(stdio, checks)
}

// doctorFallback is the config the doctor checks with when the config file
// cannot be loaded.
var doctorFallback = config.Defaults

// doctorConfig loads the config file at path. A broken file must not hide
// the other findings, so it becomes a failed check and the rest are checked
// against doctorFallback: the defaults, which Loadline uses once the file is
// gone.
func doctorConfig(path, home string) (config.Config, []doctor.Check) {
	cfg, err := config.Load(path, home)
	if err != nil {
		return doctorFallback(), []doctor.Check{{
			Name:   "config",
			Status: doctor.Fail,
			Detail: err.Error() + "; the checks below use the defaults",
		}}
	}
	return cfg, nil
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
