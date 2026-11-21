package claudecli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
)

const (
	binaryName     = "claude"
	configDirVar   = "CLAUDE_CONFIG_DIR"
	defaultTimeout = 10 * time.Second
	// defaultWaitDelay bounds how long Run waits for the output pipes to close
	// once claude has exited or been killed. A background process that claude
	// leaves behind with the pipes still open would otherwise hold Run until
	// it exits itself.
	defaultWaitDelay = 2 * time.Second

	// maxOutputBytes caps the stdout Run keeps: far more than the list of
	// sessions on any machine, and little enough that a runaway child cannot
	// fill memory.
	maxOutputBytes = 4 << 20
	// maxStderrBytes caps the stderr that an error message quotes.
	maxStderrBytes = 512
)

// Exec is a Runner that runs the claude binary.
type Exec struct {
	// Path is the claude executable. Empty means whatever "claude" resolves to
	// on PATH when Run is called, so an install or upgrade after Loadline
	// starts is picked up.
	Path string
	// DefaultDir is the configuration directory that claude uses with no
	// CLAUDE_CONFIG_DIR set. Running for it clears the variable, so that a
	// value inherited from Loadline's own environment cannot redirect the
	// call to another account.
	DefaultDir domain.ConfigDir
	// Timeout bounds each call, including starting the process. Zero means
	// ten seconds.
	Timeout time.Duration

	// waitDelay overrides defaultWaitDelay when positive.
	waitDelay time.Duration
}

// Run runs claude with args for the account in dir and returns what it wrote
// to stdout. The child's stdin is empty, and its stderr is quoted, cut short,
// in the error.
//
// When Run gives up on the child, because of the timeout, a canceled context
// or too much output, it stops the child's descendants as well: an npm-installed
// claude is a script that runs the real program as a child of its own. A child
// that exits by itself is not followed: what it left running stays.
//
// When claude exits with a non-zero code Run returns its output along with an
// error that wraps the *exec.ExitError, because some commands report through
// the exit code and the output together. For every other failure (a start
// that failed, a timeout, a canceled context, output beyond four MiB) it
// returns no output.
func (e Exec) Run(ctx context.Context, dir domain.ConfigDir, args ...string) ([]byte, error) {
	path, err := e.binary()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()

	stdout := cappedBuffer{limit: maxOutputBytes, onOverflow: cancel}
	stderr := cappedBuffer{limit: maxStderrBytes}

	// Callers pass fixed arguments and Path comes from Loadline's own
	// configuration, so nothing here is attacker-controlled.
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // G204: see above
	cmd.Env = ExecEnv(os.Environ(), dir, e.DefaultDir)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = e.waitDelayOrDefault()
	hideWindow(cmd)
	tree := newProcessTree(cmd)
	defer tree.close()

	runErr := cmd.Start()
	if runErr == nil {
		tree.adopt(cmd.Process)
		runErr = cmd.Wait()
	}

	command := describe(args)
	switch {
	case stdout.truncated:
		return nil, fmt.Errorf("%s: output exceeds %d bytes", command, maxOutputBytes)
	case runErr == nil:
		return stdout.Bytes(), nil
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%s: %w", command, ctx.Err())
	case errors.Is(runErr, exec.ErrWaitDelay):
		// claude exited cleanly and what it printed is complete; something it
		// started is still holding the pipes.
		return stdout.Bytes(), nil
	default:
		return stdout.Bytes(), fmt.Errorf("%s: %w%s", command, runErr, stderr.quote())
	}
}

func (e Exec) binary() (string, error) {
	if e.Path != "" {
		return e.Path, nil
	}
	path, err := exec.LookPath(binaryName)
	if err != nil {
		return "", fmt.Errorf("locate %s: %w", binaryName, err)
	}
	return path, nil
}

func (e Exec) timeout() time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	return defaultTimeout
}

func (e Exec) waitDelayOrDefault() time.Duration {
	if e.waitDelay > 0 {
		return e.waitDelay
	}
	return defaultWaitDelay
}

func describe(args []string) string {
	return strings.Join(append([]string{binaryName}, args...), " ")
}

// ExecEnv returns base with the account's configuration directory applied: it
// drops every CLAUDE_CONFIG_DIR entry, then sets CLAUDE_CONFIG_DIR to dir
// unless dir is defaultDir, where claude looks without being told. It does not
// modify base.
//
// Windows treats variable names as case-insensitive, so there Claude_Config_Dir
// is dropped too; elsewhere it is another variable and stays.
func ExecEnv(base []string, dir, defaultDir domain.ConfigDir) []string {
	return execEnv(base, dir, defaultDir, caseInsensitiveEnv)
}

func execEnv(base []string, dir, defaultDir domain.ConfigDir, foldCase bool) []string {
	env := make([]string, 0, len(base)+1)
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if name == configDirVar || (foldCase && strings.EqualFold(name, configDirVar)) {
			continue
		}
		env = append(env, entry)
	}
	if dir != defaultDir {
		env = append(env, configDirVar+"="+string(dir))
	}
	return env
}

// cappedBuffer keeps the first limit bytes written to it and discards the
// rest. Discarding, rather than failing the write, keeps the child draining
// into it instead of blocking on a full pipe.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
	// onOverflow, when set, runs once when a write first exceeds the limit.
	onOverflow func()
	truncated  bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	if len(p) <= room {
		return b.buf.Write(p)
	}
	b.buf.Write(p[:room])
	if !b.truncated {
		b.truncated = true
		if b.onOverflow != nil {
			b.onOverflow()
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

// quote returns the captured text as a suffix for an error message: a colon
// and the text on a single line, marked with an ellipsis when it was cut, or
// nothing when there is none.
func (b *cappedBuffer) quote() string {
	text := strings.Join(strings.Fields(strings.ToValidUTF8(b.buf.String(), "")), " ")
	if text == "" {
		return ""
	}
	if b.truncated {
		text += "…"
	}
	return ": " + text
}
