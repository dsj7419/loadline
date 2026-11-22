package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/protocol"
)

const (
	// maxStatusInput is as much of a status line's input as the tap holds.
	// Claude Code's is a few KiB. A longer input is still passed through,
	// but not forwarded.
	maxStatusInput = 1 << 20
	// tapSendTimeout bounds the tap's post to the server. The status line
	// already has its output by then; this bounds how long the tap process
	// lingers after it.
	tapSendTimeout = 100 * time.Millisecond
	// ensureEvery spaces the tap's attempts to start a server that does not
	// answer, so that a status line refreshing every second does not start
	// one each time.
	ensureEvery     = 30 * time.Second
	ensureStampName = "ensure.stamp"
)

const tapUsage = `Usage: loadline tap [--print | --run <command> | --run-b64 <command>]

Copies the status-line JSON on stdin to stdout unchanged, then forwards the
fields the dashboard uses to the loadline server.

Options:
  --print              Print "<model> · ctx <n>% · 7d <n>%" instead of the
                       JSON, leaving out what is not known yet.
  --run <command>      Run <command> as the status line, with the JSON on its
                       stdin, and pass its output and exit code through. It
                       runs through powershell on Windows and sh elsewhere.
                       Everything after --run is the command, joined with
                       single spaces. Windows PowerShell 5.1 does not escape
                       double quotes in the arguments it passes to programs,
                       so a command containing " can arrive split and lose
                       its quotes; use --run-b64 there.
  --run-b64 <command>  Like --run, with the command as unpadded base64url,
                       which no quoting can change. A value that does not
                       decode copies the JSON through instead.
`

// TapMode says what the tap prints in place of its input.
type TapMode struct {
	// Print replaces the input with a one-line summary.
	Print bool
	// Run, when set, is a status line command to run with the input on its
	// stdin; its output replaces the input. It takes precedence over Print.
	Run string
	// Stderr receives Run's standard error. Nil discards it.
	Stderr io.Writer
}

var errTapHelp = errors.New("help requested")

// runTapCommand runs the tap command for this process.
func runTapCommand(ctx context.Context, args []string, stdio IO) int {
	mode, err := parseTapArgs(args)
	if errors.Is(err, errTapHelp) {
		return printStdout(stdio, tapUsage)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stdio.Err, "loadline tap: %v\n\n%s", err, tapUsage)
		return exitUsage
	}
	mode.Stderr = stdio.Err
	// A status line whose reader has gone would otherwise end the tap at the
	// echo, by SIGPIPE, before it forwards anything. With the signal
	// caught, the write fails instead. It is caught rather than ignored: a
	// --run command then starts with the default disposition, as it would
	// without the tap, while an ignored signal stays ignored across exec.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
	paths, err := config.DefaultPaths()
	if err != nil {
		// With no Loadline directories there is no token to send with, so
		// the tap only passes its input through.
		paths = config.Paths{}
	}
	return RunTap(ctx, stdio.In, stdio.Out, processEnv(), paths, loadConfig(paths), mode)
}

// parseTapArgs reads the tap command's arguments: none, --print, --run
// <command> or --run-b64 <command>. Everything after --run is its command,
// for the reason the usage gives. A --run-b64 value that does not decode is
// no command at all.
func parseTapArgs(args []string) (TapMode, error) {
	if len(args) == 0 {
		return TapMode{}, nil
	}
	option, isOption := strings.CutPrefix(args[0], "-")
	if !isOption {
		return TapMode{}, fmt.Errorf("unexpected argument %q", args[0])
	}
	name, value, hasValue := strings.Cut(strings.TrimPrefix(option, "-"), "=")
	operands := args[1:]
	if hasValue {
		operands = append([]string{value}, operands...)
	}
	switch name {
	case "h", "help":
		return TapMode{}, errTapHelp
	case "print":
		if len(operands) > 0 {
			return TapMode{}, errors.New("--print takes no argument")
		}
		return TapMode{Print: true}, nil
	case "run":
		command := strings.Join(operands, " ")
		if strings.TrimSpace(command) == "" {
			return TapMode{}, errors.New("--run needs a command")
		}
		return TapMode{Run: command}, nil
	case "run-b64":
		if len(operands) != 1 {
			return TapMode{}, errors.New("--run-b64 takes one argument")
		}
		return TapMode{Run: decodeCommand(operands[0])}, nil
	default:
		return TapMode{}, fmt.Errorf("unknown option %q", args[0])
	}
}

// decodeCommand decodes a --run-b64 value, or returns "" when it is not
// unpadded base64url of UTF-8 text.
func decodeCommand(value string) string {
	command, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || !utf8.Valid(command) {
		return ""
	}
	return string(command)
}

// RunTap passes the status-line input on in through to out, or what mode
// puts in its place, and then forwards the allowlisted fields of the input,
// protocol.StatusInput, to the server. The output comes first and out is
// closed, when it is a file, before the tap talks to the server, so the
// status line never waits for the server: the forward has 100 ms, and a
// status event that misses the server is dropped, never spooled, since a
// newer one follows. When the forward fails, the tap starts the server, at
// most once in 30 s across all taps.
//
// It returns the exit code of a Run command, and otherwise 0, or 1 when the
// input could not be read or the output not written.
func RunTap(ctx context.Context, in io.Reader, out io.Writer, env Env, paths config.Paths, cfg config.Config, mode TapMode) int {
	head, readErr := io.ReadAll(io.LimitReader(in, maxStatusInput+1))
	input := io.Reader(bytes.NewReader(head))
	if len(head) > maxStatusInput {
		input = io.MultiReader(input, in)
	}
	complete := readErr == nil && len(head) <= maxStatusInput

	// --print needs the input decoded before it writes. Every other mode
	// writes first and decodes after.
	printing := mode.Run == "" && mode.Print
	var status protocol.StatusInput
	decoded := false
	if printing {
		status, decoded = decodeStatusInput(head, complete)
	}

	code := exitOK
	var writeErr error
	switch {
	case mode.Run != "":
		code = runStatusLine(ctx, mode.Run, input, out, mode.Stderr)
	case printing:
		_, writeErr = io.WriteString(out, summary(status))
	default:
		_, writeErr = io.Copy(out, input)
	}
	if file, ok := out.(*os.File); ok {
		_ = file.Close()
	}
	if (readErr != nil || writeErr != nil) && code == exitOK {
		code = exitFailure
	}

	if !printing {
		status, decoded = decodeStatusInput(head, complete)
	}
	if decoded {
		forwardStatus(ctx, env, paths, cfg, status)
	}
	return code
}

// decodeStatusInput decodes the status-line input, reporting false when it
// is not one or was too long to read whole.
func decodeStatusInput(input []byte, complete bool) (protocol.StatusInput, bool) {
	if !complete {
		return protocol.StatusInput{}, false
	}
	status, err := protocol.DecodeStatus(input)
	if err != nil {
		return protocol.StatusInput{}, false
	}
	return status, true
}

// summary is the line --print shows: the model, the context window's fill
// and the weekly limit's use, each left out when unknown. A context window
// at 0% is one Claude Code has not measured yet: it sends null until then.
func summary(s protocol.StatusInput) string {
	var parts []string
	if s.Model != nil {
		if name := cmp.Or(s.Model.DisplayName, s.Model.ID); name != "" {
			parts = append(parts, name)
		}
	}
	if s.ContextWindow != nil && s.ContextWindow.UsedPercentage > 0 {
		parts = append(parts, "ctx "+percent(s.ContextWindow.UsedPercentage))
	}
	if s.RateLimits != nil && s.RateLimits.SevenDay != nil && s.RateLimits.SevenDay.UsedPercentage != nil {
		parts = append(parts, "7d "+percent(*s.RateLimits.SevenDay.UsedPercentage))
	}
	return strings.Join(parts, " · ") + "\n"
}

func percent(v float64) string {
	return strconv.FormatFloat(math.Round(v), 'f', 0, 64) + "%"
}

// runStatusLine runs command with stdin as its input and its output on
// stdout and stderr, and returns its exit code.
func runStatusLine(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := statusLineCommand(ctx, command)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		return exitOK
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.ExitCode() > 0 {
		return exitErr.ExitCode()
	}
	if stderr != nil {
		_, _ = fmt.Fprintf(stderr, "loadline tap: run the status line: %v\n", err)
	}
	return exitFailure
}

// forwardStatus posts the allowlisted fields of a status-line input to the
// server, and starts the server when the post fails. An input the server
// could never take, such as one without a session, is dropped before it is
// sent.
func forwardStatus(ctx context.Context, env Env, paths config.Paths, cfg config.Config, status protocol.StatusInput) {
	if paths.TokenFile == "" {
		return
	}
	payload, err := json.Marshal(status)
	if err != nil {
		return
	}
	envelope, err := newEnvelope(env, protocol.KindStatus, payload)
	if err != nil {
		return
	}
	if _, err := protocol.StatusEvent(envelope, status); err != nil {
		return
	}
	token, err := config.EnsureToken(paths.TokenFile)
	if err != nil {
		return
	}

	client := forward.Client{BaseURL: forward.LoopbackURL(cfg.Port), Token: token}
	ctx, cancel := context.WithTimeout(ctx, tapSendTimeout)
	defer cancel()
	if client.Send(ctx, forward.StatusPath, envelope) != nil {
		ensureServer(env, paths)
	}
}

// ensureServer starts the server unless a tap started one, or tried to, in
// the last 30 s. The stamp's time says when; a stamp from the future, left
// by a clock that stepped back, is stale. If the stamp cannot be written,
// nothing is started: without it, every tap would start a server.
func ensureServer(env Env, paths config.Paths) {
	stamp := filepath.Join(paths.DataDir, ensureStampName)
	now := env.Now()
	if info, err := os.Stat(stamp); err == nil {
		if age := now.Sub(info.ModTime()); age >= 0 && age <= ensureEvery {
			return
		}
	}
	if err := touch(stamp, now); err != nil {
		return
	}
	_ = env.Spawn(env.Exe, "serve")
}

// touch creates the file at path if need be and sets its time to at.
func touch(path string, at time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		return fmt.Errorf("stamp %s: %w", path, err)
	}
	return nil
}
