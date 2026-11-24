// Package hermetictest keeps a test binary, and every process it starts,
// away from the machine's own Claude Code and Loadline. Tests of the server
// and the commands give them a fake claude, a home directory and a port of
// their own; but a mutant of the code under test can lose any of those, and
// the code then falls back on the claude on PATH, the user's home and the
// default port, which on a developer's machine are the user's real account
// and running server.
//
// A package whose tests can start claude, a server or a browser calls
// StandIn first thing in its TestMain, then Seal, and passes the tests' exit
// code through Finish:
//
//	func TestMain(m *testing.M) {
//		hermetictest.StandIn()
//		sealed, err := hermetictest.Seal()
//		if err != nil {
//			fmt.Fprintln(os.Stderr, err)
//			os.Exit(1)
//		}
//		os.Exit(sealed.Finish(m.Run()))
//	}
package hermetictest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
)

const (
	// standInName is the stand-in's name, which is what tells a copy of the
	// test binary standing in for claude from the test binary itself.
	standInName = "claude"
	// recordName is the file beside the stand-in where it records its runs.
	recordName = "calls.jsonl"
	// standInExit is the stand-in's exit code: a failure, as claude's would
	// be if it could not run.
	standInExit = 1
)

// claudePath is the stand-in Seal made, for tests to name in a config.
var claudePath string

// StandIn returns at once, unless this process is the stand-in for claude
// that Seal puts first on PATH, a copy of the test binary under claude's
// name. The stand-in records how it was called beside itself, says on
// stderr what it is, and exits with a failure, running no test.
func StandIn() {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	if !isStandIn(exe) {
		return
	}
	// A relative name says nothing of where the record is.
	if filepath.IsAbs(exe) {
		record(filepath.Join(filepath.Dir(exe), recordName))
	}
	fmt.Fprintln(os.Stderr, "loadline tests: this is a stand-in for claude, which no test may run; "+
		"the call is recorded and fails the test run")
	os.Exit(standInExit)
}

func isStandIn(path string) bool {
	base := filepath.Base(path)
	if runtime.GOOS == "windows" {
		base = strings.TrimSuffix(strings.ToLower(base), ".exe")
	}
	return base == standInName
}

// call is one run of the stand-in.
type call struct {
	Args         []string `json:"args"`
	ConfigDir    string   `json:"configDir,omitempty"`
	ConfigDirSet bool     `json:"configDirSet"`
}

func (c call) String() string {
	account := "with no CLAUDE_CONFIG_DIR"
	if c.ConfigDirSet {
		account = "with CLAUDE_CONFIG_DIR=" + c.ConfigDir
	}
	return strings.Join(append([]string{standInName}, c.Args...), " ") + " (" + account + ")"
}

// record appends this run to the record at path as one line, written at
// once, so that runs at the same time do not mix. A run it cannot record
// still fails, and so does the test that started it.
func record(path string) {
	configDir, set := os.LookupEnv("CLAUDE_CONFIG_DIR")
	line, err := json.Marshal(call{Args: os.Args[1:], ConfigDir: configDir, ConfigDirSet: set})
	if err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: the record beside the stand-in, in Seal's folder
	if err != nil {
		fmt.Fprintf(os.Stderr, "loadline tests: record a run of the stand-in for claude: %v\n", err)
		return
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(line, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "loadline tests: record a run of the stand-in for claude: %v\n", err)
	}
}

// Sealed is a test binary that Seal has sealed.
type Sealed struct {
	// dir holds the stand-in and its record, the home directory and
	// Loadline's home.
	dir string
	// port is the port the sealed config names, which nothing listened on
	// as Seal chose it, and claude the stand-in.
	port   int
	claude string
}

// Seal points the test binary, and every process it starts from then on, at
// a folder of its own. It puts a stand-in for claude first on PATH, makes the
// home directory, Claude Code's config dir and Loadline's files the folder's,
// and writes a Loadline config there that names a port nothing listens on
// and the stand-in. A test that wants Loadline's files or Claude Code's
// config dir elsewhere still sets its own.
//
// Seal changes the process's environment, so it belongs in TestMain, before
// any test runs. In the stand-in it does not return, as StandIn does not.
func Seal() (*Sealed, error) {
	StandIn()
	dir, err := os.MkdirTemp("", "loadline-sealed-")
	if err != nil {
		return nil, fmt.Errorf("make the sealed folder: %w", err)
	}
	s := &Sealed{dir: dir}
	if err := s.seal(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return s, nil
}

func (s *Sealed) seal() error {
	bin := filepath.Join(s.dir, "bin")
	home := filepath.Join(s.dir, "home")
	loadlineHome := filepath.Join(s.dir, "loadline")
	vars := map[string]string{
		"PATH":              bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME":              home,
		"USERPROFILE":       home,
		"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"),
		"LOADLINE_HOME":     loadlineHome,
	}
	folders := []string{bin, home, loadlineHome}
	// Where Loadline keeps its files when LOADLINE_HOME is unset, as some
	// tests unset it. On macOS that is in the home directory's Library.
	switch runtime.GOOS {
	case "windows":
		vars["LOCALAPPDATA"] = filepath.Join(home, "AppData", "Local")
		vars["APPDATA"] = filepath.Join(home, "AppData", "Roaming")
		folders = append(folders, vars["LOCALAPPDATA"], vars["APPDATA"])
	case "darwin":
	default:
		vars["XDG_CONFIG_HOME"] = filepath.Join(home, ".config")
		vars["XDG_STATE_HOME"] = filepath.Join(home, ".local", "state")
	}
	for _, folder := range folders {
		if err := os.MkdirAll(folder, 0o700); err != nil {
			return fmt.Errorf("make %s: %w", folder, err)
		}
	}

	standIn := filepath.Join(bin, standInName)
	if runtime.GOOS == "windows" {
		standIn += ".exe"
	}
	if err := copyTestBinary(standIn); err != nil {
		return err
	}
	port, err := closedPort()
	if err != nil {
		return err
	}
	for name, value := range vars {
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("set %s: %w", name, err)
		}
	}
	if err := writeConfig(filepath.Join(loadlineHome, "config.json"), "{}", port, standIn); err != nil {
		return err
	}
	// A stand-in that PATH does not find first guards nothing.
	found, err := exec.LookPath(standInName)
	if err != nil {
		return fmt.Errorf("look up claude on the sealed PATH: %w", err)
	}
	if !sameFile(found, standIn) {
		return fmt.Errorf("claude on the sealed PATH is %s, not the stand-in %s", found, standIn)
	}
	s.port, s.claude = port, standIn
	claudePath = standIn
	return nil
}

// sameFile reports whether paths a and b name one file.
func sameFile(a, b string) bool {
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

// Defaults returns Loadline's defaults with the sealed config's port and the
// stand-in in place of their own, for code that falls back on the defaults
// when it has no config it can use.
func (s *Sealed) Defaults() config.Config {
	defaults := config.Defaults()
	defaults.Port, defaults.ClaudePath = s.port, s.claude
	return defaults
}

// copyTestBinary copies the running test binary to path, executable.
func copyTestBinary(path string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find the test binary: %w", err)
	}
	src, err := os.Open(exe) //nolint:gosec // G304: the running test binary
	if err != nil {
		return fmt.Errorf("open the test binary: %w", err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700) //nolint:gosec // G302,G304: the stand-in must be executable, in Seal's folder
	if err != nil {
		return fmt.Errorf("create the stand-in for claude: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("write the stand-in for claude: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("write the stand-in for claude: %w", err)
	}
	return nil
}

// closedPort returns a loopback port that nothing listened on a moment ago.
func closedPort() (int, error) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find a free port: %w", err)
	}
	defer func() { _ = listener.Close() }()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return 0, fmt.Errorf("find a free port: %w", err)
	}
	return strconv.Atoi(port)
}

// Finish returns code, the tests' exit code, or a failure when the stand-in
// ran while they did, after it has said on stderr how it was called each
// time. It removes what Seal made.
func (s *Sealed) Finish(code int) int {
	return s.finish(code, os.Stderr)
}

func (s *Sealed) finish(code int, w io.Writer) int {
	defer func() { _ = os.RemoveAll(s.dir) }()
	calls, err := s.calls()
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(w, "loadline tests: read the record of the stand-in for claude: %v\n", err)
	case len(calls) == 0:
		return code
	default:
		_, _ = fmt.Fprintf(w, "loadline tests: tests ran claude, which no test may run; the stand-in answered %d times:\n", len(calls))
		for _, c := range calls {
			_, _ = fmt.Fprintf(w, "\t%s\n", c)
		}
	}
	if code == 0 {
		return 1
	}
	return code
}

// calls returns the runs of the stand-in so far, in order.
func (s *Sealed) calls() ([]call, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "bin", recordName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var calls []call
	lines := bufio.NewScanner(bytes.NewReader(data))
	for lines.Scan() {
		var c call
		if err := json.Unmarshal(lines.Bytes(), &c); err != nil {
			// A run is still a run, whatever its line says.
			c = call{Args: []string{"(unreadable record: " + lines.Text() + ")"}}
		}
		calls = append(calls, c)
	}
	return calls, lines.Err()
}

// Claude returns the path of the stand-in for claude, for a test's config to
// name as claudePath. It fails tb when the test binary is not sealed.
func Claude(tb testing.TB) string {
	tb.Helper()
	if claudePath == "" {
		tb.Fatal("the test binary is not sealed: its TestMain must call hermetictest.Seal")
	}
	return claudePath
}

// ClosedPort returns a loopback port that nothing listened on a moment ago,
// for a config that must name a port no server answers on.
func ClosedPort(tb testing.TB) int {
	tb.Helper()
	port, err := closedPort()
	if err != nil {
		tb.Fatal(err)
	}
	return port
}

// WriteConfig writes contents, a JSON object, to path as Loadline's config,
// adding port, and the stand-in for claude as claudePath, wherever contents
// names neither. Whatever else a test configures, a server or command that
// reads the file then takes a port of the test's own and runs no real
// claude. It fails tb when the file would not load, or would name the
// default port: the hook, the tap and install read a config they cannot load
// as the defaults, default port and all. A test that means to write a broken
// config writes it itself.
func WriteConfig(tb testing.TB, path, contents string, port int) {
	tb.Helper()
	if err := writeConfig(path, contents, port, Claude(tb)); err != nil {
		tb.Fatal(err)
	}
}

// writeConfig is WriteConfig with claude as the stand-in. It returns why it
// wrote no config, or why the one it wrote will not do.
func writeConfig(path, contents string, port int, claude string) error {
	sealed, err := sealedConfig([]byte(contents), port, claude)
	if err != nil {
		return fmt.Errorf("the config %q is no JSON object: %w", contents, err)
	}
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		return fmt.Errorf("write the config %s: %w", path, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find the home directory to load the config %s: %w", path, err)
	}
	cfg, err := config.Load(path, home)
	if err != nil {
		return fmt.Errorf("the config %q does not load: %w", contents, err)
	}
	if defaultPort := config.Defaults().Port; cfg.Port == defaultPort {
		return fmt.Errorf("the config %q names the default port %d, where the user's own server may be listening", contents, defaultPort)
	}
	return nil
}

// sealedConfig returns the config object contents with port and claude
// added where it names neither, or an error for contents that are no JSON
// object. Like Loadline's own reading of the file, it takes null for an
// object with nothing in it, and a key in any case for the setting.
func sealedConfig(contents []byte, port int, claude string) ([]byte, error) {
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(contents, &settings); err != nil {
		return nil, err
	}
	if settings == nil {
		settings = map[string]json.RawMessage{}
	}
	if !names(settings, "port") {
		settings["port"] = json.RawMessage(strconv.Itoa(port))
	}
	if !names(settings, "claudePath") {
		encoded, err := json.Marshal(claude)
		if err != nil {
			return nil, err
		}
		settings["claudePath"] = encoded
	}
	return json.Marshal(settings)
}

// names reports whether settings sets key, in any case.
func names(settings map[string]json.RawMessage, key string) bool {
	for name := range settings {
		if strings.EqualFold(name, key) {
			return true
		}
	}
	return false
}
