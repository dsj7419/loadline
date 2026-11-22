// Package doctor checks a Loadline setup: the Claude Code it relies on, the
// ingest token, the wiring of each settings file, the installed binary, the
// server, and the feeds that reach the server. It only reads; fixing what it
// finds is install's job.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/install"
	"github.com/dsj7419/loadline/internal/tracker"
)

// Level is how a check came out.
type Level uint8

const (
	// OK means the check passed.
	OK Level = iota
	// Warn means Loadline works, but not as well as it could.
	Warn
	// Fail means something Loadline needs is missing or broken.
	Fail
)

var levelNames = [...]string{OK: "ok", Warn: "warn", Fail: "fail"}

// String returns ok, warn or fail, or Level(n) for a value outside them.
func (l Level) String() string {
	if int(l) < len(levelNames) {
		return levelNames[l]
	}
	return fmt.Sprintf("Level(%d)", l)
}

// Check is the outcome of one check.
type Check struct {
	Name   string
	Status Level
	// Detail says what was found and, when it is not OK, what to do.
	Detail string
}

// Deps is the setup Run checks and the means to reach it. Shell and
// ClaudeVersion must be set.
type Deps struct {
	// SettingsFiles are the Claude Code settings files Loadline should be
	// wired into.
	SettingsFiles []string
	// Exe is the installed binary that the settings should run, with forward
	// slashes, as install.Options takes it.
	Exe string
	// Port is the port the settings should send telemetry to.
	Port int
	// Shell reports the shell Claude Code runs the status line of a settings
	// file with.
	Shell func(settings []byte) install.Shell
	// Client reaches the server. Run gives it the token from TokenFile.
	Client forward.Client
	// ClaudeVersion returns what `claude --version` prints.
	ClaudeVersion func(ctx context.Context) (string, error)
	// TokenFile holds the ingest token. Run never creates it.
	TokenFile string
	// Now reads the time. Nil means time.Now.
	Now func() time.Time
}

const (
	diagPath     = "/api/diag"
	snapshotPath = "/api/snapshot"
	// maxDiag and maxSnapshot cap the answers read: a few hundred bytes of
	// diagnostics, and far more sessions than any machine runs.
	maxDiag     = 64 << 10
	maxSnapshot = 32 << 20
	// apiTimeout bounds reading the server's diagnostics and sessions.
	apiTimeout = 5 * time.Second
	// feedWindow is how recently each feed must have been heard from while
	// any session is live.
	feedWindow = 10 * time.Minute
	// shownChanges is how many pending changes a settings check names before
	// it only counts the rest.
	shownChanges = 3
	// missingToken stands in for the token when there is none to read. The
	// settings cannot hold the right one then, so planning with a stand-in
	// shows their token as out of date, which install fixes together with
	// the token file.
	missingToken = "missing"

	installHint    = "run loadline install"
	powerShellNote = "the PowerShell wrapper starts a PowerShell process on every status refresh, which is slower " +
		"than the Git Bash pipe form; installing Git for Windows and running loadline install again avoids it"
)

// minClaude is the oldest Claude Code that Loadline works with.
var minClaude = version{2, 1, 300}

// leadingVersion matches the version `claude --version` starts with, such as
// 2.1.330 in "2.1.330 (Claude Code)".
var leadingVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)

// Run runs every check and returns their outcomes in order: claude, the
// token, each settings file, the binary, the server and each feed. A failed
// check never keeps a later one from reporting. The feeds are read only from
// a server that answers as Loadline, since reading them takes the token.
func Run(ctx context.Context, d Deps) []Check {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	token, tokenCheck := checkToken(d.TokenFile)
	checks := []Check{checkClaude(ctx, d.ClaudeVersion), tokenCheck}

	target := install.Options{Exe: d.Exe, Port: d.Port, Token: string(token)}
	if len(token) == 0 {
		target.Token = missingToken
	}
	for _, path := range d.SettingsFiles {
		checks = append(checks, checkSettings(path, target, d.Shell)...)
	}
	checks = append(checks, checkBinary(d.Exe))

	client := d.Client
	client.Token = token
	healthy := client.Healthy(ctx)
	checks = append(checks, serverCheck(client.BaseURL, healthy))
	if !healthy {
		// Whatever else answers on the port must not be sent the token.
		return append(checks, Check{Name: "feeds", Status: Fail, Detail: "cannot be checked while the server is down"})
	}
	return append(checks, checkFeeds(ctx, client, now())...)
}

func checkClaude(ctx context.Context, claudeVersion func(context.Context) (string, error)) Check {
	const name = "claude"
	out, err := claudeVersion(ctx)
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return Check{Name: name, Status: Fail, Detail: "not found; install Claude Code, or set claudePath in the Loadline config"}
	case err != nil:
		return Check{Name: name, Status: Fail, Detail: fmt.Sprintf("failed: %v", err)}
	}

	v, ok := parseVersion(out)
	if !ok {
		first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
		return Check{Name: name, Status: Fail, Detail: fmt.Sprintf("claude --version printed %q, which holds no version",
			strings.TrimSpace(first))}
	}
	if v.less(minClaude) {
		return Check{Name: name, Status: Fail, Detail: fmt.Sprintf("%s is older than %s; update Claude Code", v, minClaude)}
	}
	return Check{Name: name, Status: OK, Detail: v.String()}
}

// version is a major, minor and patch number.
type version [3]int

func parseVersion(out string) (version, bool) {
	match := leadingVersion.FindStringSubmatch(strings.TrimSpace(out))
	if match == nil {
		return version{}, false
	}
	var v version
	for i, part := range match[1:] {
		n, err := strconv.Atoi(part)
		if err != nil {
			return version{}, false
		}
		v[i] = n
	}
	return v, true
}

func (v version) less(than version) bool { return slices.Compare(v[:], than[:]) < 0 }

func (v version) String() string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }

// checkToken returns the ingest token, or nil when it cannot be read.
func checkToken(path string) ([]byte, Check) {
	const name = "token"
	token, err := config.ReadToken(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, Check{Name: name, Status: Fail, Detail: path + " does not exist; " + installHint}
	case err != nil:
		return nil, Check{Name: name, Status: Fail, Detail: err.Error()}
	}
	return token, Check{Name: name, Status: OK, Detail: path}
}

// checkSettings reports whether the settings file at path is wired to
// target as install would wire it, and warns when its status line runs
// through the PowerShell wrapper.
func checkSettings(path string, target install.Options, shell func([]byte) install.Shell) []Check {
	name := "settings " + path
	settings, err := os.ReadFile(filepath.Clean(path))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return []Check{{Name: name, Status: Fail, Detail: "not wired: the file does not exist; " + installHint}}
	case err != nil:
		return []Check{{Name: name, Status: Fail, Detail: err.Error()}}
	}

	target.Shell = shell(settings)
	_, changes, warnings, err := install.Plan(settings, target)
	checks := []Check{wiring(name, changes, warnings, err)}
	if install.UsesPowerShellWrapper(settings, target.Exe) {
		checks = append(checks, Check{Name: "status line " + path, Status: Warn, Detail: powerShellNote})
	}
	return checks
}

// wiring turns what install would do to a settings file into its check: a
// file that install would not edit is wired.
func wiring(name string, changes []install.Change, warnings []string, err error) Check {
	switch {
	case err != nil:
		return Check{Name: name, Status: Fail, Detail: err.Error()}
	case install.HasEdits(changes):
		return Check{Name: name, Status: Fail, Detail: "not wired: install would change " + pending(changes) + "; " + installHint}
	case len(warnings) > 0:
		return Check{Name: name, Status: Warn, Detail: "wired, but " + strings.Join(warnings, "; ")}
	default:
		return Check{Name: name, Status: OK, Detail: "wired"}
	}
}

// pending names the values the changes edit, the first few by path.
func pending(changes []install.Change) string {
	var paths []string
	for _, c := range changes {
		if c.Action != install.ActionSkip {
			paths = append(paths, c.Path)
		}
	}
	if len(paths) <= shownChanges {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:shownChanges], ", "), len(paths)-shownChanges)
}

func checkBinary(exe string) Check {
	const name = "binary"
	path := filepath.FromSlash(exe)
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Check{Name: name, Status: Fail, Detail: path + " does not exist; " + installHint}
	case err != nil:
		return Check{Name: name, Status: Fail, Detail: err.Error()}
	case !info.Mode().IsRegular():
		return Check{Name: name, Status: Fail, Detail: path + " is not a file; " + installHint}
	}
	return Check{Name: name, Status: OK, Detail: path}
}

func serverCheck(url string, healthy bool) Check {
	if !healthy {
		return Check{Name: "server", Status: Fail, Detail: "no Loadline server answers at " + url +
			"; the next Claude Code session starts it, or run loadline serve"}
	}
	return Check{Name: "server", Status: OK, Detail: "running at " + url}
}

// diag is the part of the server's diagnostics the feed checks read: when
// each feed last delivered something, null for never.
type diag struct {
	Feeds struct {
		Hook   tracker.Timestamp `json:"hook"`
		Status tracker.Timestamp `json:"status"`
		OTLP   tracker.Timestamp `json:"otlp"`
		Poll   tracker.Timestamp `json:"poll"`
	} `json:"feeds"`
}

// snapshot is the part of the server's snapshot that tells whether any
// session is live.
type snapshot struct {
	Sessions []session `json:"sessions"`
}

type session struct {
	State domain.State `json:"state"`
}

// live reports whether the session has a process that sends events: one
// that is neither ended nor parked.
func (s session) live() bool { return s.State != domain.StateEnded && s.State != domain.StateParked }

func checkFeeds(ctx context.Context, client forward.Client, now time.Time) []Check {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	d, err := getJSON[diag](ctx, client, diagPath, maxDiag)
	if err != nil {
		return []Check{{Name: "feeds", Status: Fail, Detail: "cannot read the server's diagnostics: " + err.Error()}}
	}
	s, err := getJSON[snapshot](ctx, client, snapshotPath, maxSnapshot)
	if err != nil {
		return []Check{{Name: "feeds", Status: Fail, Detail: "cannot read the server's sessions: " + err.Error()}}
	}

	live := slices.ContainsFunc(s.Sessions, session.live)
	return []Check{
		feedCheck("hook feed", time.Time(d.Feeds.Hook), now, live),
		feedCheck("status line feed", time.Time(d.Feeds.Status), now, live),
		feedCheck("OpenTelemetry feed", time.Time(d.Feeds.OTLP), now, live),
		feedCheck("poll feed", time.Time(d.Feeds.Poll), now, live),
	}
}

// feedCheck judges a feed last heard from at, the zero time for never. A
// feed may well go quiet while no session is live.
func feedCheck(name string, at, now time.Time, live bool) Check {
	age := max(now.Sub(at), 0)
	seen := "last seen " + age.Round(time.Second).String() + " ago"
	switch {
	case !at.IsZero() && age <= feedWindow:
		return Check{Name: name, Status: OK, Detail: seen}
	case !live:
		return Check{Name: name, Status: OK, Detail: "no live sessions"}
	case at.IsZero():
		return Check{Name: name, Status: Warn, Detail: "not seen since the server started, while sessions are live"}
	default:
		return Check{Name: name, Status: Warn, Detail: seen + ", while sessions are live"}
	}
}

func getJSON[T any](ctx context.Context, client forward.Client, path string, limit int64) (T, error) {
	var v T
	body, err := client.Get(ctx, path, limit)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return v, fmt.Errorf("decode %s: %w", path, err)
	}
	return v, nil
}
