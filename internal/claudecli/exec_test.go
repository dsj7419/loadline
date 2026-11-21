package claudecli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dsj7419/loadline/internal/domain"
)

const (
	// defaultDir is where claude looks with no CLAUDE_CONFIG_DIR set, inherited
	// is what a parent shell for another account may have exported, and account
	// is the account being asked about.
	defaultDir domain.ConfigDir = `C:\x\.claude`
	inherited  domain.ConfigDir = `C:\x\.claude-b`
	account    domain.ConfigDir = `C:\x\.claude-work`
)

func TestExecEnv_DefaultDirUnsetsInherited(t *testing.T) {
	base := []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=" + string(inherited), "HOME=/home/x"}

	got := ExecEnv(base, defaultDir, defaultDir)

	want := []string{"PATH=/bin", "HOME=/home/x"}
	if !slices.Equal(got, want) {
		t.Errorf("env = %q, want %q", got, want)
	}
}

func TestExecEnv_OtherDirSetsExactlyOnce(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"CLAUDE_CONFIG_DIR=" + string(inherited),
		"HOME=/home/x",
		"CLAUDE_CONFIG_DIR=C:\\second",
	}

	got := ExecEnv(base, account, defaultDir)

	want := []string{"PATH=/bin", "HOME=/home/x", "CLAUDE_CONFIG_DIR=" + string(account)}
	if !slices.Equal(got, want) {
		t.Errorf("env = %q, want %q", got, want)
	}
}

func TestExecEnv_OtherDirAddsWhenNoneInherited(t *testing.T) {
	got := ExecEnv([]string{"PATH=/bin"}, account, defaultDir)

	want := []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=" + string(account)}
	if !slices.Equal(got, want) {
		t.Errorf("env = %q, want %q", got, want)
	}
}

func TestExecEnv_LeavesBaseAndLookalikesAlone(t *testing.T) {
	base := []string{
		"CLAUDE_CONFIG_DIR=" + string(inherited),
		"CLAUDE_CONFIG_DIRECTORY=keep",
		"MY_CLAUDE_CONFIG_DIR=keep",
		"=C:=C:\\work",
	}
	snapshot := slices.Clone(base)

	got := execEnv(base, account, defaultDir, false)

	if !slices.Equal(base, snapshot) {
		t.Errorf("base was modified: %q, want %q", base, snapshot)
	}
	want := []string{
		"CLAUDE_CONFIG_DIRECTORY=keep",
		"MY_CLAUDE_CONFIG_DIR=keep",
		"=C:=C:\\work",
		"CLAUDE_CONFIG_DIR=" + string(account),
	}
	if !slices.Equal(got, want) {
		t.Errorf("env = %q, want %q", got, want)
	}
}

func TestExecEnv_KeyCase(t *testing.T) {
	base := []string{"Claude_Config_Dir=" + string(inherited), "PATH=/bin"}

	folded := execEnv(base, defaultDir, defaultDir, true)
	if want := []string{"PATH=/bin"}; !slices.Equal(folded, want) {
		t.Errorf("case-insensitive env = %q, want %q", folded, want)
	}

	exact := execEnv(base, defaultDir, defaultDir, false)
	if !slices.Equal(exact, base) {
		t.Errorf("case-sensitive env = %q, want %q: a differently cased name is another variable", exact, base)
	}
}

func TestExecEnv_NamesFoldCaseOnWindowsOnly(t *testing.T) {
	got := ExecEnv([]string{"Claude_Config_Dir=" + string(inherited)}, defaultDir, defaultDir)

	if dropped, want := len(got) == 0, runtime.GOOS == "windows"; dropped != want {
		t.Errorf("env = %q on %s: dropped a differently cased name = %v, want %v", got, runtime.GOOS, dropped, want)
	}
}

// The tests below run the test binary itself as a stand-in for claude: when
// helperModeEnv is set, TestMain plays the part the mode names and exits
// before any test runs.
const (
	helperModeEnv    = "LOADLINE_CLAUDECLI_HELPER"
	helperStdoutEnv  = "LOADLINE_CLAUDECLI_STDOUT"
	helperStderrEnv  = "LOADLINE_CLAUDECLI_STDERR"
	helperExitEnv    = "LOADLINE_CLAUDECLI_EXIT"
	helperBytesEnv   = "LOADLINE_CLAUDECLI_BYTES"
	helperStartedEnv = "LOADLINE_CLAUDECLI_STARTED"
	helperReleaseEnv = "LOADLINE_CLAUDECLI_RELEASE"
	helperDoneEnv    = "LOADLINE_CLAUDECLI_DONE"
	helperPIDEnv     = "LOADLINE_CLAUDECLI_PIDFILE"

	// helperPatience bounds how long a helper waits for the test to release
	// it, so that one that is never released cannot outlive the test run.
	helperPatience = 30 * time.Second
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperModeEnv); mode != "" {
		os.Exit(runHelper(mode))
	}
	os.Exit(m.Run())
}

func runHelper(mode string) int {
	switch mode {
	case "print":
		if err := printTo(os.Stdout, os.Getenv(helperStdoutEnv)); err != nil {
			return 2
		}
		if err := printTo(os.Stderr, os.Getenv(helperStderrEnv)); err != nil {
			return 2
		}
		code, _ := strconv.Atoi(os.Getenv(helperExitEnv))
		return code
	case "echo":
		_, set := os.LookupEnv("CLAUDE_CONFIG_DIR")
		echo := helperEcho{Args: os.Args[1:], ConfigDir: os.Getenv("CLAUDE_CONFIG_DIR"), ConfigDirSet: set}
		if err := json.NewEncoder(os.Stdout).Encode(echo); err != nil {
			return 2
		}
		return 0
	case "flood":
		count, _ := strconv.Atoi(os.Getenv(helperBytesEnv))
		if err := printTo(os.Stdout, strings.Repeat("x", count)); err != nil {
			return 2
		}
		if os.Getenv(helperExitEnv) == "hang" {
			time.Sleep(helperPatience)
		}
		return 0
	case "hang":
		if err := touch(os.Getenv(helperStartedEnv)); err != nil {
			return 2
		}
		time.Sleep(helperPatience)
		return 0
	case "grandchild":
		// Hand the grandchild our stdout and exit, so that the pipe stays open
		// after claude itself is gone.
		child := exec.CommandContext(context.Background(), os.Args[0]) //nolint:gosec // G204: re-executes the test binary as a stand-in for claude
		child.Env = append(os.Environ(), helperModeEnv+"=linger")
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			return 2
		}
		if err := printTo(os.Stdout, `["claude"]`); err != nil {
			return 2
		}
		return 0
	case "linger":
		if err := touch(os.Getenv(helperStartedEnv)); err != nil {
			return 2
		}
		waitForFile(os.Getenv(helperReleaseEnv))
		if err := touch(os.Getenv(helperDoneEnv)); err != nil {
			return 2
		}
		return 0
	case "tree":
		// Start a descendant that outlives a kill of this process alone, then
		// wait to be killed.
		child := exec.CommandContext(context.Background(), os.Args[0]) //nolint:gosec // G204: re-executes the test binary as a stand-in for claude
		child.Env = append(os.Environ(), helperModeEnv+"=sleeper")
		if err := child.Start(); err != nil {
			return 2
		}
		time.Sleep(helperPatience)
		return 0
	case "daemonize":
		// Leave a descendant running and exit at once, as a launcher that
		// starts a daemon on purpose does.
		child := exec.CommandContext(context.Background(), os.Args[0]) //nolint:gosec // G204: re-executes the test binary as a stand-in for claude
		child.Env = append(os.Environ(), helperModeEnv+"=sleeper")
		if err := child.Start(); err != nil {
			return 2
		}
		return 0
	case "sleeper":
		if err := writePIDFile(os.Getenv(helperPIDEnv)); err != nil {
			return 2
		}
		time.Sleep(helperPatience)
		return 0
	default:
		return 3
	}
}

type helperEcho struct {
	Args         []string
	ConfigDir    string
	ConfigDirSet bool
}

// writePIDFile records the process's ID at path. It writes a temporary file
// and renames it, so a reader that sees path sees the whole ID.
func writePIDFile(path string) error {
	temp := path + ".tmp"
	if err := os.WriteFile(temp, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil { //nolint:gosec // G703: path is a file in the test's own temp dir
		return err
	}
	return os.Rename(temp, path) //nolint:gosec // G703: path is a file in the test's own temp dir
}

func printTo(f *os.File, text string) error {
	_, err := f.WriteString(text)
	return err
}

// touch creates an empty file at path, and does nothing for an empty path.
func touch(path string) error {
	if path == "" {
		return nil
	}
	return os.WriteFile(path, nil, 0o600) //nolint:gosec // G703: path is a file in the test's own temp dir
}

func exists(path string) bool {
	_, err := os.Stat(path) //nolint:gosec // G703: path is a file in the test's own temp dir
	return err == nil
}

// waitForFile polls until path exists or helperPatience runs out, and reports
// whether it exists.
func waitForFile(path string) bool {
	for deadline := time.Now().Add(helperPatience); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if exists(path) {
			return true
		}
	}
	return exists(path)
}

// helperExec returns an Exec that runs the test binary in the given helper
// mode.
func helperExec(t *testing.T, mode string) Exec {
	t.Helper()

	t.Setenv(helperModeEnv, mode)
	return Exec{Path: os.Args[0], DefaultDir: defaultDir, Timeout: 30 * time.Second}
}

func TestExec_RunPassesArgsAndEnvironment(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", `C:\inherited\from\parent`)
	e := helperExec(t, "echo")

	run := func(dir domain.ConfigDir) helperEcho {
		t.Helper()
		out, err := e.Run(context.Background(), dir, "agents", "--json", "--all")
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		var echo helperEcho
		if err := json.Unmarshal(out, &echo); err != nil {
			t.Fatalf("decode %q: %v", out, err)
		}
		return echo
	}

	other := run(account)
	if want := []string{"agents", "--json", "--all"}; !slices.Equal(other.Args, want) {
		t.Errorf("args = %q, want %q", other.Args, want)
	}
	if other.ConfigDir != string(account) {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want %q", other.ConfigDir, account)
	}

	if def := run(defaultDir); def.ConfigDirSet {
		t.Errorf("CLAUDE_CONFIG_DIR = %q for the default dir, want it unset", def.ConfigDir)
	}
}

func TestExec_RunNonZeroExitReturnsOutputAndStderr(t *testing.T) {
	t.Setenv(helperStdoutEnv, `{"loggedIn":false}`)
	t.Setenv(helperStderrEnv, "not logged in\nrun /login\n")
	t.Setenv(helperExitEnv, "1")

	out, err := helperExec(t, "print").Run(context.Background(), account, "auth", "status", "--json")

	if string(out) != `{"loggedIn":false}` {
		t.Errorf("stdout = %q, want the output printed before exiting", out)
	}
	var exit exitCoder
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("err = %v, want an error carrying exit code 1", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("err = %v, want it to wrap an *exec.ExitError", err)
	}
	const want = "claude auth status --json: exit status 1: not logged in run /login"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
}

func TestExec_RunNonZeroExitWithoutStderr(t *testing.T) {
	t.Setenv(helperExitEnv, "3")

	_, err := helperExec(t, "print").Run(context.Background(), account, "agents")

	if want := "claude agents: exit status 3"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

func TestExec_RunTruncatesStderrInTheError(t *testing.T) {
	t.Setenv(helperStderrEnv, strings.Repeat("Q", 2000))
	t.Setenv(helperExitEnv, "1")

	_, err := helperExec(t, "print").Run(context.Background(), account, "agents")

	if err == nil {
		t.Fatal("err = nil, want an error")
	}
	if got := strings.Count(err.Error(), "Q"); got != maxStderrBytes {
		t.Errorf("error holds %d bytes of stderr, want %d", got, maxStderrBytes)
	}
	if !strings.HasSuffix(err.Error(), "…") {
		t.Errorf("err = %q, want a trailing ellipsis marking the truncation", err)
	}
}

func TestExec_RunTruncatedStderrStaysValidUTF8(t *testing.T) {
	// 512 bytes cut a run of 3-byte characters in the middle of one.
	t.Setenv(helperStderrEnv, strings.Repeat("€", 400))
	t.Setenv(helperExitEnv, "1")

	_, err := helperExec(t, "print").Run(context.Background(), account, "agents")

	if err == nil || !utf8.ValidString(err.Error()) || strings.ContainsRune(err.Error(), utf8.RuneError) {
		t.Fatalf("err = %q, want valid UTF-8 without replacement characters", err)
	}
	if got, want := strings.Count(err.Error(), "€"), maxStderrBytes/len("€"); got != want {
		t.Errorf("error holds %d characters of stderr, want the %d that fit in %d bytes", got, want, maxStderrBytes)
	}
}

func TestExec_RunSuccessIgnoresStderr(t *testing.T) {
	t.Setenv(helperStdoutEnv, "[]")
	t.Setenv(helperStderrEnv, "a warning")

	out, err := helperExec(t, "print").Run(context.Background(), account, "agents")

	if err != nil || string(out) != "[]" {
		t.Errorf("Run = %q, %v, want %q and no error", out, err, "[]")
	}
}

func TestExec_RunStdoutCap(t *testing.T) {
	t.Run("exactly at the cap", func(t *testing.T) {
		t.Setenv(helperBytesEnv, strconv.Itoa(maxOutputBytes))

		out, err := helperExec(t, "flood").Run(context.Background(), account, "agents")

		if err != nil || len(out) != maxOutputBytes {
			t.Errorf("Run = %d bytes, %v, want %d bytes and no error", len(out), err, maxOutputBytes)
		}
	})

	t.Run("beyond the cap kills the child", func(t *testing.T) {
		t.Setenv(helperBytesEnv, strconv.Itoa(maxOutputBytes+1))
		t.Setenv(helperExitEnv, "hang")

		start := time.Now()
		out, err := helperExec(t, "flood").Run(context.Background(), account, "agents")

		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("err = %v, want an error saying the output exceeds the cap", err)
		}
		if out != nil {
			t.Errorf("got %d bytes alongside the error, want none", len(out))
		}
		if elapsed := time.Since(start); elapsed > helperPatience/2 {
			t.Errorf("Run took %v: the child was left running to the end of its sleep", elapsed)
		}
	})
}

func TestExec_RunTimesOutAndKillsTheChild(t *testing.T) {
	e := helperExec(t, "hang")
	e.Timeout = 300 * time.Millisecond

	start := time.Now()
	out, err := e.Run(context.Background(), account, "agents")

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if out != nil {
		t.Errorf("got %q alongside the error, want none", out)
	}
	if elapsed := time.Since(start); elapsed > helperPatience/2 {
		t.Errorf("Run took %v, want it to return soon after the %v timeout", elapsed, e.Timeout)
	}
}

func TestExec_RunStopsWhenTheCallerCancels(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv(helperStartedEnv, started)
	e := helperExec(t, "hang")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Cancel once the child is running, so that this cancels a live
		// process and not just a start that never happened.
		waitForFile(started)
		cancel()
	}()

	start := time.Now()
	_, err := e.Run(ctx, account, "agents")

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > helperPatience/2 {
		t.Errorf("Run took %v after the child started, want it to stop at once", elapsed)
	}
}

func TestExec_RunDoesNotWaitForAGrandchildHoldingThePipes(t *testing.T) {
	dir := t.TempDir()
	started, release, done := filepath.Join(dir, "started"), filepath.Join(dir, "release"), filepath.Join(dir, "done")
	t.Setenv(helperStartedEnv, started)
	t.Setenv(helperReleaseEnv, release)
	t.Setenv(helperDoneEnv, done)
	e := helperExec(t, "grandchild")
	e.waitDelay = 100 * time.Millisecond

	start := time.Now()
	out, err := e.Run(context.Background(), account, "agents")
	elapsed := time.Since(start)

	// Let the grandchild go and wait for it to finish, so that no process
	// outlives the test.
	if !waitForFile(started) {
		t.Fatal("the grandchild never started")
	}
	if err := touch(release); err != nil {
		t.Fatalf("release the grandchild: %v", err)
	}
	if !waitForFile(done) {
		t.Error("the grandchild did not finish")
	}

	if err != nil || string(out) != `["claude"]` {
		t.Errorf("Run = %q, %v, want what claude printed and no error", out, err)
	}
	if elapsed > helperPatience/2 {
		t.Errorf("Run took %v: it waited for the grandchild to release the pipes", elapsed)
	}
}

// descendantPID returns the process ID that the descendant recorded in
// pidFile, and makes sure that no helper outlives the test, whatever the
// outcome.
func descendantPID(t *testing.T, pidFile string) int {
	t.Helper()

	if !waitForFile(pidFile) {
		t.Fatal("the descendant never reported its process ID")
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read the descendant's process ID: %v", err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatalf("descendant process ID %q: %v", data, err)
	}
	t.Cleanup(func() {
		if process, err := os.FindProcess(pid); err == nil && processAlive(pid) {
			_ = process.Kill()
		}
	})
	return pid
}

// awaitGone fails the test unless the process has exited within a few
// seconds.
func awaitGone(t *testing.T, pid int) {
	t.Helper()

	for deadline := time.Now().Add(10 * time.Second); processAlive(pid); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d, a descendant of the killed child, is still running", pid)
		}
	}
}

// runUntilDescendantStarts runs e until the descendant that pidFile belongs
// to has started, then cancels the call, as a timeout would, and returns the
// call's error.
func runUntilDescendantStarts(t *testing.T, e Exec, pidFile string) error {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Cancel only once the descendant exists, so that the call kills a
		// tree and not a start that never happened.
		waitForFile(pidFile)
		cancel()
	}()

	_, err := e.Run(ctx, account, "agents")
	return err
}

func TestExec_RunKillsTheWholeProcessTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	t.Setenv(helperPIDEnv, pidFile)

	err := runUntilDescendantStarts(t, helperExec(t, "tree"), pidFile)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	awaitGone(t, descendantPID(t, pidFile))
}

func TestExec_RunLeavesWhatAChildStartedBeforeExitingByItself(t *testing.T) {
	// A launcher can start a daemon on purpose, such as the one that
	// supervises background sessions. Stopping the tree is for a call that is
	// given up on, not for one that finished.
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	t.Setenv(helperPIDEnv, pidFile)

	_, err := helperExec(t, "daemonize").Run(context.Background(), account, "agents")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	pid := descendantPID(t, pidFile)
	if !processAlive(pid) {
		t.Errorf("process %d, left running by a child that exited by itself, was stopped", pid)
	}
}

func TestExec_RunReportsAMissingBinary(t *testing.T) {
	e := Exec{Path: filepath.Join(t.TempDir(), "no-such-claude")}

	out, err := e.Run(context.Background(), account, "agents")

	if err == nil || out != nil {
		t.Errorf("Run = %q, %v, want an error and no output", out, err)
	}
}

func TestExec_RunWithoutPathLooksUpClaudeOnPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := Exec{}.Run(context.Background(), account, "agents")

	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v, want exec.ErrNotFound when claude is not on PATH", err)
	}
}

func TestExec_RunRunsAShim(t *testing.T) {
	shim := writeShim(t, t.TempDir())
	e := Exec{Path: shim, DefaultDir: defaultDir}

	out, err := e.Run(context.Background(), account, "agents", "--json", "--all")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "args=agents --json --all dir=" + string(account)
	if got := strings.TrimSpace(string(out)); got != want {
		t.Errorf("shim printed %q, want %q", got, want)
	}
}

func TestExec_RunWithoutPathFindsAShimOnPATH(t *testing.T) {
	t.Setenv("PATH", filepath.Dir(writeShim(t, t.TempDir())))

	out, err := Exec{DefaultDir: defaultDir}.Run(context.Background(), account, "auth", "status", "--json")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "args=auth status --json dir=" + string(account)
	if got := strings.TrimSpace(string(out)); got != want {
		t.Errorf("shim printed %q, want %q", got, want)
	}
}

func TestExec_RunDefaultsToTenSecondTimeout(t *testing.T) {
	if got := (Exec{}).timeout(); got != 10*time.Second {
		t.Errorf("timeout = %v, want 10s", got)
	}
	if got := (Exec{Timeout: time.Second}).timeout(); got != time.Second {
		t.Errorf("timeout = %v, want the configured 1s", got)
	}
}
