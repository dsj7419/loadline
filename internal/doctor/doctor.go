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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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

// Deps is the setup Run checks and the means to reach it. Shell,
// ClaudeVersion and Exposure must be set.
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
	// Client reaches the server. Run gives it the token from TokenFile and
	// its own, longer health probe.
	Client forward.Client
	// ClaudeVersion returns what `claude --version` prints.
	ClaudeVersion func(ctx context.Context) (string, error)
	// TokenFile holds the ingest token. Run never creates it.
	TokenFile string
	// DataDir holds the token, the installed binary, the spool and the
	// history.
	DataDir string
	// Exposure says who besides the user can get into a directory, as
	// fsperm.Exposure does, or "" when nobody can.
	Exposure func(path string) (string, error)
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
	// healthProbe bounds the server check. Nobody waits on the doctor the
	// way a session waits on a hook, so a busy server gets longer to answer
	// than the hook path's probe allows.
	healthProbe = 2 * time.Second
	// feedWindow is how recently each feed must have been heard from while
	// any session is active.
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
var minClaude = version{release: [3]int{2, 1, 300}}

// leadingVersion matches the version `claude --version` starts with, such as
// 2.1.330 in "2.1.330 (Claude Code)", with its pre-release label if any.
var leadingVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?`)

// Run runs every check and returns their outcomes in order: claude, the data
// directory, the token, each settings file, the binary, the server and each
// feed. A failed check never keeps a later one from reporting. The feeds are
// read only from a server that answers as Loadline, since reading them takes
// the token.
func Run(ctx context.Context, d Deps) []Check {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	token, tokenCheck := checkToken(d.TokenFile)
	checks := []Check{checkClaude(ctx, d.ClaudeVersion), checkDataDir(d.DataDir, d.Exposure), tokenCheck}

	target := install.Options{Exe: d.Exe, Port: d.Port, Token: string(token)}
	if len(token) == 0 {
		target.Token = missingToken
	}
	for _, path := range d.SettingsFiles {
		checks = append(checks, checkSettings(path, target, d.Shell)...)
	}
	checks = append(checks, checkBinary(d.Exe))

	client := d.Client
	client.Token, client.HealthTimeout = token, healthProbe
	healthy := client.Healthy(ctx)
	checks = append(checks, serverCheck(client.BaseURL, healthy))
	switch {
	case !healthy:
		// Whatever else answers on the port must not be sent the token.
		return append(checks, Check{Name: "feeds", Status: Fail, Detail: "cannot be checked while the server is down"})
	case token == nil:
		return append(checks, Check{Name: "feeds", Status: Fail, Detail: "cannot be checked without the token file"})
	}
	return append(checks, checkFeeds(ctx, client, now(), d.TokenFile)...)
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

// version is a major, minor and patch number, and the label of a
// pre-release, such as rc.1.
type version struct {
	release [3]int
	pre     string
}

func parseVersion(out string) (version, bool) {
	match := leadingVersion.FindStringSubmatch(strings.TrimSpace(out))
	if match == nil {
		return version{}, false
	}
	v := version{pre: match[4]}
	for i, part := range match[1:4] {
		n, err := strconv.Atoi(part)
		if err != nil {
			return version{}, false
		}
		v.release[i] = n
	}
	return v, true
}

// less reports whether v comes before than. A pre-release comes before the
// release it leads up to; two pre-releases of one release are not ordered,
// as the minimum Loadline compares with is a release.
func (v version) less(than version) bool {
	if c := slices.Compare(v.release[:], than.release[:]); c != 0 {
		return c < 0
	}
	return v.pre != "" && than.pre == ""
}

func (v version) String() string {
	text := fmt.Sprintf("%d.%d.%d", v.release[0], v.release[1], v.release[2])
	if v.pre != "" {
		text += "-" + v.pre
	}
	return text
}

// checkDataDir fails unless the data directory admits only the user: anyone
// else it lets in can read the token, and replace the binary every hook
// runs.
func checkDataDir(dir string, exposure func(string) (string, error)) Check {
	const name = "data directory"
	others, err := exposure(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Check{Name: name, Status: Fail, Detail: dir + " does not exist; " + installHint}
	case err != nil:
		return Check{Name: name, Status: Fail, Detail: err.Error()}
	case others != "":
		return Check{Name: name, Status: Fail, Detail: dir + " is not private: " + others + "; " + installHint}
	}
	return Check{Name: name, Status: OK, Detail: dir}
}

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
// target as install would wire it, warns when its status line runs through
// the PowerShell wrapper, and warns where the copy of the token it holds may
// leak.
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
	wired := wiring(name, settings, target)
	checks := []Check{wired}
	if install.UsesPowerShellWrapper(settings, target.Exe) {
		checks = append(checks, Check{Name: "status line " + path, Status: Warn, Detail: powerShellNote})
	}
	checks = append(checks, symlinked(path)...)
	if wired.Status != Fail {
		// Installing again would not edit the file, so would not narrow its
		// mode either.
		checks = append(checks, openToOthers(path)...)
	}
	return checks
}

// symlinked warns when the settings file at path is a symlink: install
// writes the ingest token into the file it points to, which may sit in a
// dotfiles repository that its owner publishes.
func symlinked(path string) []Check {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return nil
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return []Check{{Name: "token in " + path, Status: Warn, Detail: "the file is a symlink that cannot be followed: " + err.Error()}}
	}
	return []Check{{
		Name:   "token in " + path,
		Status: Warn,
		Detail: "the file is a symlink to " + target + ", so install writes the ingest token there; " +
			"keep that file out of any repository you share",
	}}
}

// openToOthers warns, outside Windows, when the mode of the settings file at
// path lets group or others in. Install takes those permissions off the
// file it writes, but Claude Code rewrites the file when a setting changes,
// with a mode of its own choosing. On Windows the file takes the access of
// the folder it is in.
func openToOthers(path string) []Check {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	mode := info.Mode().Perm()
	var who string
	switch group, others := mode&0o070 != 0, mode&0o007 != 0; {
	case group && others:
		who = "group and others"
	case group:
		who = "group"
	case others:
		who = "others"
	default:
		return nil
	}
	return []Check{{
		Name:   "token in " + path,
		Status: Warn,
		Detail: fmt.Sprintf("its mode %#o lets %s in, and it holds the ingest token; a rewrite by Claude Code can leave it so; "+
			"run chmod go-rwx %s", mode, who, path),
	}}
}

// wiring turns what install would do to settings into their check: settings
// that install would not edit are wired. Settings that are not say which
// other binary their hooks run, if any, and keep install's warnings, which
// installing again does not clear.
func wiring(name string, settings []byte, target install.Options) Check {
	_, changes, warnings, err := install.Plan(settings, target)
	switch {
	case err != nil:
		return Check{Name: name, Status: Fail, Detail: err.Error()}
	case install.HasEdits(changes):
		parts := []string{"install would change " + pending(changes), installHint}
		others := slices.DeleteFunc(install.HookExes(settings), func(exe string) bool { return exe == target.Exe })
		if len(others) > 0 {
			parts = slices.Insert(parts, 0, "its hooks run "+strings.Join(others, " and ")+" instead of "+target.Exe)
		}
		if len(warnings) > 0 {
			parts = append(parts, "also, "+strings.Join(warnings, "; "))
		}
		return Check{Name: name, Status: Fail, Detail: "not wired: " + strings.Join(parts, "; ")}
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
// session is active.
type snapshot struct {
	Sessions []session `json:"sessions"`
}

type session struct {
	State domain.State `json:"state"`
}

// active reports whether the session sends events as it goes: it is
// starting, working or compacting. A session that waits, for the user or for
// nothing, may stay quiet for as long as it waits.
func (s session) active() bool {
	switch s.State {
	case domain.StateStarting, domain.StateWorking, domain.StateCompacting:
		return true
	default:
		return false
	}
}

func checkFeeds(ctx context.Context, client forward.Client, now time.Time, tokenFile string) []Check {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	d, err := getJSON[diag](ctx, client, diagPath, maxDiag)
	if err != nil {
		return []Check{unreadable("diagnostics", err, tokenFile)}
	}
	s, err := getJSON[snapshot](ctx, client, snapshotPath, maxSnapshot)
	if err != nil {
		return []Check{unreadable("sessions", err, tokenFile)}
	}

	active := slices.ContainsFunc(s.Sessions, session.active)
	return []Check{
		feedCheck("hook feed", time.Time(d.Feeds.Hook), now, active),
		feedCheck("status line feed", time.Time(d.Feeds.Status), now, active),
		feedCheck("OpenTelemetry feed", time.Time(d.Feeds.OTLP), now, active),
		feedCheck("poll feed", time.Time(d.Feeds.Poll), now, active),
	}
}

// unreadable is the feed check when the server would not give the doctor
// the state it reads. A server that refuses the token in tokenFile holds
// another one, most likely because it started before the file was replaced.
func unreadable(what string, err error, tokenFile string) Check {
	if refused, ok := errors.AsType[*forward.StatusError](err); ok && refused.Code == http.StatusUnauthorized {
		return Check{
			Name:   "feeds",
			Status: Fail,
			Detail: "cannot be checked: the server holds a different token than " + tokenFile + "; restart it",
		}
	}
	return Check{Name: "feeds", Status: Fail, Detail: "cannot read the server's " + what + ": " + err.Error()}
}

// feedCheck judges a feed last heard from at, the zero time for never. A
// feed may well go quiet while no session is active.
func feedCheck(name string, at, now time.Time, active bool) Check {
	age := max(now.Sub(at), 0)
	seen := "last seen " + age.Round(time.Second).String() + " ago"
	switch {
	case !at.IsZero() && age <= feedWindow:
		return Check{Name: name, Status: OK, Detail: seen}
	case !active:
		return Check{Name: name, Status: OK, Detail: "no active sessions"}
	case at.IsZero():
		return Check{Name: name, Status: Warn, Detail: "not seen since the server started, while sessions are active"}
	default:
		return Check{Name: name, Status: Warn, Detail: seen + ", while sessions are active"}
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
