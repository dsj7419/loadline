package hermetictest_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/hermetictest"
)

// The tests seal the test binary one at a time and run the stand-in on
// purpose, so the package's own run is not sealed.
func TestMain(m *testing.M) {
	hermetictest.StandIn()
	os.Exit(m.Run())
}

// sealedVars are the variables Seal sets on this system.
func sealedVars() []string {
	vars := []string{"PATH", "HOME", "USERPROFILE", "CLAUDE_CONFIG_DIR", "LOADLINE_HOME"}
	switch runtime.GOOS {
	case "windows":
		return append(vars, "LOCALAPPDATA", "APPDATA")
	case "darwin":
		return vars
	default:
		return append(vars, "XDG_CONFIG_HOME", "XDG_STATE_HOME")
	}
}

// seal seals the test binary for the rest of the test. The test's end puts
// back every variable Seal set and removes the sealed folder.
func seal(t *testing.T) *hermetictest.Sealed {
	t.Helper()
	for _, name := range sealedVars() {
		// t.Setenv puts back the value it found at the test's end, whatever
		// Seal sets meanwhile.
		t.Setenv(name, os.Getenv(name))
	}
	s, err := hermetictest.Seal()
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(hermetictest.Dir(s)) })
	return s
}

// runClaude runs claude, as PATH finds it, with args, in the test's
// environment with configDir as CLAUDE_CONFIG_DIR, or without the variable
// when configDir is empty. It returns claude's exit code and what it wrote
// to stderr. It runs only the stand-in in the sealed folder of s: when the
// seal failed to put it first on PATH, the next claude there may be the
// user's own.
func runClaude(t *testing.T, s *hermetictest.Sealed, configDir string, args ...string) (code int, stderr string) {
	t.Helper()
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("look up claude: %v", err)
	}
	if bin := filepath.Join(hermetictest.Dir(s), "bin"); !inside(path, bin) {
		t.Fatalf("claude on PATH is %s, outside the sealed %s; not running it", path, bin)
	}
	cmd := exec.CommandContext(t.Context(), path, args...) //nolint:gosec // G204: the stand-in for claude, with the test's own arguments
	cmd.Env = slices.DeleteFunc(os.Environ(), func(v string) bool {
		return strings.HasPrefix(strings.ToUpper(v), "CLAUDE_CONFIG_DIR=")
	})
	if configDir != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+configDir)
	}
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if _, exited := errors.AsType[*exec.ExitError](err); !exited {
			t.Fatalf("run claude: %v", err)
		}
	}
	return cmd.ProcessState.ExitCode(), errOut.String()
}

// inside reports whether path is dir or below it.
func inside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func TestSeal_PutsAStandInForClaudeFirstOnPATH(t *testing.T) {
	s := seal(t)

	found, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("look up claude: %v", err)
	}
	if found != hermetictest.Claude(t) {
		t.Errorf("claude on PATH is %s, want the stand-in %s", found, hermetictest.Claude(t))
	}
	if !inside(found, hermetictest.Dir(s)) {
		t.Errorf("the stand-in %s is outside the sealed folder %s", found, hermetictest.Dir(s))
	}
}

func TestSeal_TheStandInRecordsEachCallAndFails(t *testing.T) {
	s := seal(t)
	account := filepath.Join(t.TempDir(), ".claude-b")

	firstCode, firstErr := runClaude(t, s, account, "auth", "status", "--json")
	secondCode, _ := runClaude(t, s, "", "agents", "--json", "--all")

	if firstCode == 0 || secondCode == 0 {
		t.Errorf("the stand-in exited %d and %d, want a failure each time", firstCode, secondCode)
	}
	if !strings.Contains(firstErr, "stand-in for claude") {
		t.Errorf("the stand-in said %q on stderr, want it to say what it is", firstErr)
	}
	calls, err := hermetictest.Calls(s)
	if err != nil {
		t.Fatalf("Calls: %v", err)
	}
	want := []hermetictest.Call{
		{Args: []string{"auth", "status", "--json"}, ConfigDir: account, ConfigDirSet: true},
		{Args: []string{"agents", "--json", "--all"}},
	}
	if !slices.EqualFunc(calls, want, func(a, b hermetictest.Call) bool {
		return slices.Equal(a.Args, b.Args) && a.ConfigDir == b.ConfigDir && a.ConfigDirSet == b.ConfigDirSet
	}) {
		t.Errorf("Calls = %+v, want %+v", calls, want)
	}
}

// Claude Code's home and Loadline's files lie in the sealed folder, and the
// config Loadline finds there names a port other than the default and the
// stand-in claude.
func TestSeal_PointsTheHomeAndLoadlineAtTheSealedFolder(t *testing.T) {
	s := seal(t)
	dir := hermetictest.Dir(s)

	home, err := os.UserHomeDir()
	if err != nil || !inside(home, dir) {
		t.Errorf("UserHomeDir = %q, %v; want a folder in %s", home, err, dir)
	}
	for _, name := range slices.DeleteFunc(sealedVars(), func(name string) bool { return name == "PATH" }) {
		if value := os.Getenv(name); !inside(value, dir) {
			t.Errorf("%s = %q, want a folder in %s", name, value, dir)
		}
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if !inside(paths.DataDir, dir) || !inside(paths.ConfigFile, dir) {
		// Nothing outside the sealed folder is read.
		t.Fatalf("DefaultPaths = %+v, want every path in %s", paths, dir)
	}
	cfg, err := config.Load(paths.ConfigFile, home)
	if err != nil {
		t.Fatalf("load the sealed config: %v", err)
	}
	if defaultPort := config.Defaults().Port; cfg.Port == defaultPort || cfg.ClaudePath != hermetictest.Claude(t) {
		t.Errorf("the sealed config names port %d and claude %q; want a port other than the default %d, and the stand-in %s",
			cfg.Port, cfg.ClaudePath, defaultPort, hermetictest.Claude(t))
	}
}

// Code that falls back on the defaults when it has no config it can use, as
// the doctor does, falls back in a sealed run on the sealed config's port
// and the stand-in instead of the defaults' own.
func TestSealed_DefaultsAreLoadlinesWithTheSealedPortAndClaude(t *testing.T) {
	s := seal(t)
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sealedConfig, err := config.Load(paths.ConfigFile, home)
	if err != nil {
		t.Fatalf("load the sealed config: %v", err)
	}
	want := config.Defaults()
	want.Port, want.ClaudePath = sealedConfig.Port, hermetictest.Claude(t)

	if got := s.Defaults(); !reflect.DeepEqual(got, want) {
		t.Errorf("Defaults = %+v, want %+v", got, want)
	}
}

func TestFinish_FailsARunWhoseTestsRanTheStandIn(t *testing.T) {
	tests := map[string]struct {
		calls, code, wantCode int
	}{
		"passed, no call":   {calls: 0, code: 0, wantCode: 0},
		"failed, no call":   {calls: 0, code: 3, wantCode: 3},
		"passed, a call":    {calls: 1, code: 0, wantCode: 1},
		"failed, two calls": {calls: 2, code: 3, wantCode: 3},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			s := seal(t)
			for range tt.calls {
				runClaude(t, s, "", "auth", "status", "--json")
			}
			var stderr bytes.Buffer

			code := hermetictest.FinishTo(s, tt.code, &stderr)

			if code != tt.wantCode {
				t.Errorf("Finish(%d) = %d, want %d", tt.code, code, tt.wantCode)
			}
			if got := strings.Count(stderr.String(), "claude auth status --json"); got != tt.calls {
				t.Errorf("Finish named %d calls, want %d:\n%s", got, tt.calls, stderr.String())
			}
			if _, err := os.Stat(hermetictest.Dir(s)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the sealed folder is still there after Finish: %v", err)
			}
		})
	}
}

func TestWriteConfig_AddsThePortAndTheStandInWhereTheConfigNamesNeither(t *testing.T) {
	seal(t)
	const port, ownPort = 41001, 41002
	ownClaude := filepath.Join(t.TempDir(), "no-claude")
	ownClaudeJSON := strings.ReplaceAll(filepath.ToSlash(ownClaude), `"`, `\"`)
	tests := map[string]struct {
		contents   string
		wantPort   int
		wantClaude string
		wantIdle   int
	}{
		"nothing":        {contents: `{}`, wantPort: port, wantClaude: hermetictest.Claude(t), wantIdle: config.Defaults().IdleExitMinutes},
		"its own port":   {contents: `{"port":41002}`, wantPort: ownPort, wantClaude: hermetictest.Claude(t), wantIdle: config.Defaults().IdleExitMinutes},
		"its own claude": {contents: `{"claudePath":"` + ownClaudeJSON + `"}`, wantPort: port, wantClaude: filepath.ToSlash(ownClaude), wantIdle: config.Defaults().IdleExitMinutes},
		"other settings": {contents: `{"idleExitMinutes":7}`, wantPort: port, wantClaude: hermetictest.Claude(t), wantIdle: 7},
		// Loadline reads null as no settings, and keys in any case.
		"null":         {contents: `null`, wantPort: port, wantClaude: hermetictest.Claude(t), wantIdle: config.Defaults().IdleExitMinutes},
		"its own Port": {contents: `{"Port":41002}`, wantPort: ownPort, wantClaude: hermetictest.Claude(t), wantIdle: config.Defaults().IdleExitMinutes},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")

			hermetictest.WriteConfig(t, path, tt.contents, port)

			cfg, err := config.Load(path, t.TempDir())
			if err != nil {
				t.Fatalf("load the config written: %v", err)
			}
			if cfg.Port != tt.wantPort || cfg.ClaudePath != tt.wantClaude || cfg.IdleExitMinutes != tt.wantIdle {
				t.Errorf("the config names port %d, claude %q and idleExitMinutes %d; want %d, %q and %d",
					cfg.Port, cfg.ClaudePath, cfg.IdleExitMinutes, tt.wantPort, tt.wantClaude, tt.wantIdle)
			}
		})
	}
}

// The hook, the tap and install read a config they cannot load as the
// defaults, default port and all, and every command reads a config whose
// port is null so. WriteConfig leaves no such file for a test.
func TestWriteConfig_RefusesAConfigThatReadsAsTheDefaultPort(t *testing.T) {
	for name, contents := range map[string]string{
		"truncated":        `{"port":`,
		"not an object":    `[]`,
		"not JSON":         `port = 8289`,
		"empty":            ``,
		"an unknown key":   `{"theme":"dark"}`,
		"a port too low":   `{"port":80}`,
		"a null port":      `{"port":null}`,
		"the default port": `{"port":8289}`,
		"the default Port": `{"Port":8289}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")

			if err := hermetictest.TryWriteConfig(path, contents, 41001, "claude"); err == nil {
				written, _ := os.ReadFile(path)
				t.Errorf("WriteConfig(%q) wrote %s, want it refused", contents, written)
			}
		})
	}
}
