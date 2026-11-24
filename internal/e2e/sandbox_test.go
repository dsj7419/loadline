package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// defaultPort is the port the owner's own Loadline serves on, which no test
// may ever reach.
const defaultPort = 8289

// sandbox is a tree that holds everything a real Loadline process run by
// these tests reads or writes: its home folders, its data directory and
// config, Claude Code's config dir, a stand-in claude and the binaries
// under test. The owner's own setup must stay out of their reach, so each
// child runs in the tree with an environment built from an allow-list, and
// check fails a step that would let anything point outside the tree.
type sandbox struct {
	tree string
	// configDir is Claude Code's config dir, which every install names.
	configDir string
	// loadlineHome is LOADLINE_HOME, which holds the data directory and the
	// config.
	loadlineHome string
	// claude is the stand-in claude the config names.
	claude string
	// port is the free port the config names.
	port int
	// env is every child's whole environment.
	env []string
}

// sessionPrefixes start the names of the variables no child may get, as
// they would tie it to the session that runs the test, or to an account.
var sessionPrefixes = []string{"ANTHROPIC_", "CLAUDE_", "CLAUDECODE", "OTEL_"}

// sandboxPathVars name the variables that hold folders, each in the tree.
var sandboxPathVars = []string{
	"HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "XDG_STATE_HOME", "XDG_CONFIG_HOME",
	"LOADLINE_HOME", "TEMP", "TMP", "TMPDIR", "CLAUDE_CONFIG_DIR",
}

// systemVars name the variables a child takes from the test's own
// environment, which only say where the system is.
var systemVars = []string{"PATHEXT", "SystemRoot", "windir", "ComSpec"}

// newSandbox makes the tree and the environment of its children.
func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	tree := t.TempDir()
	s := &sandbox{
		tree:         tree,
		configDir:    filepath.Join(tree, ".claude"),
		loadlineHome: filepath.Join(tree, "loadline"),
		claude:       filepath.Join(tree, "claude", "claude"+exeSuffix()),
		port:         freePort(t),
	}
	folders := map[string]string{
		"HOME":              tree,
		"USERPROFILE":       tree,
		"LOCALAPPDATA":      filepath.Join(tree, "AppData", "Local"),
		"APPDATA":           filepath.Join(tree, "AppData", "Roaming"),
		"XDG_STATE_HOME":    filepath.Join(tree, ".local", "state"),
		"XDG_CONFIG_HOME":   filepath.Join(tree, ".config"),
		"LOADLINE_HOME":     s.loadlineHome,
		"TEMP":              filepath.Join(tree, "tmp"),
		"TMP":               filepath.Join(tree, "tmp"),
		"TMPDIR":            filepath.Join(tree, "tmp"),
		"CLAUDE_CONFIG_DIR": s.configDir,
	}
	for _, name := range slices.Sorted(maps.Keys(folders)) {
		if err := os.MkdirAll(folders[name], 0o700); err != nil {
			t.Fatal(err)
		}
		s.env = append(s.env, name+"="+folders[name])
	}
	s.env = append(s.env, "PATH="+systemPath())
	for _, name := range systemVars {
		if value, ok := os.LookupEnv(name); ok {
			s.env = append(s.env, name+"="+value)
		}
	}
	return s
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// systemPath is a PATH of the system's own folders alone, which hold no
// claude.
func systemPath() string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		return filepath.Join(root, "System32") + string(os.PathListSeparator) + root
	}
	return "/usr/bin:/bin"
}

// inside reports whether path is the tree or in it.
func (s *sandbox) inside(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(s.tree, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// installed is the binary install puts in the data directory.
func (s *sandbox) installed() string {
	return filepath.Join(s.loadlineHome, "bin", "loadline"+exeSuffix())
}

// writeConfig writes Loadline's config: the free port, the stand-in claude,
// no dashboard opened as a server starts, which a server a hook or a status
// line starts would otherwise do, and an idle exit of a minute, so that a
// server left behind by a failed run leaves once its session is gone. Every
// check refuses a step until it is written.
func (s *sandbox) writeConfig(t *testing.T) {
	t.Helper()
	config, err := json.Marshal(map[string]any{
		"port": s.port, "claudePath": s.claude, "openOnStart": "never", "idleExitMinutes": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.loadlineHome, "config.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
}

// check fails the test unless the next step stays in the sandbox: every
// variable of the children's environment is one the allow-list names, each
// folder among them is set and in the tree, none ties a child to a session
// or an account, no folder on PATH holds a claude, every path among args,
// alone or as a flag's value, is in the tree, and the config is there, and
// names a free port other than the default, a claude in the tree, and no
// browser opened as a server starts.
func (s *sandbox) check(t *testing.T, args ...string) {
	t.Helper()
	if err := s.verify(args...); err != nil {
		t.Fatalf("the step would leave the sandbox: %v", err)
	}
}

func (s *sandbox) verify(args ...string) error {
	if err := s.verifyEnv(); err != nil {
		return err
	}
	for _, arg := range args {
		path := arg
		if flag, value, ok := strings.Cut(arg, "="); ok && strings.HasPrefix(flag, "-") {
			path = value
		}
		if filepath.IsAbs(path) && !s.inside(path) {
			return fmt.Errorf("argument %s is outside the tree %s", arg, s.tree)
		}
	}
	return s.verifyConfig()
}

func (s *sandbox) verifyEnv() error {
	// A folder variable left out would let a child fall back on the
	// machine's own folder.
	for _, name := range sandboxPathVars {
		if !slices.ContainsFunc(s.env, func(entry string) bool {
			n, _, _ := strings.Cut(entry, "=")
			return strings.EqualFold(n, name)
		}) {
			return fmt.Errorf("variable %s is not set", name)
		}
	}
	allowed := slices.Concat(sandboxPathVars, systemVars, []string{"PATH"})
	for _, entry := range s.env {
		name, value, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		switch {
		case !slices.ContainsFunc(allowed, func(a string) bool { return strings.EqualFold(a, name) }):
			return fmt.Errorf("variable %s is not on the allow-list", name)
		case upper != "CLAUDE_CONFIG_DIR" && slices.ContainsFunc(sessionPrefixes, func(p string) bool { return strings.HasPrefix(upper, p) }):
			return fmt.Errorf("variable %s would tie a child to a session or an account", name)
		case slices.ContainsFunc(sandboxPathVars, func(v string) bool { return strings.EqualFold(v, name) }) && !s.inside(value):
			return fmt.Errorf("variable %s=%s is outside the tree %s", name, value, s.tree)
		case upper == "CLAUDE_CONFIG_DIR" && value != s.configDir:
			return fmt.Errorf("CLAUDE_CONFIG_DIR=%s, want %s", value, s.configDir)
		case upper == "PATH":
			if dir, ok := holdsClaude(value); ok {
				return fmt.Errorf("PATH holds a claude in %s", dir)
			}
		}
	}
	return nil
}

// holdsClaude returns the first folder on path that holds a claude.
func holdsClaude(path string) (string, bool) {
	for _, dir := range filepath.SplitList(path) {
		for _, name := range []string{"claude", "claude.exe", "claude.cmd", "claude.ps1"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return dir, true
			}
		}
	}
	return "", false
}

// verifyConfig refuses a sandbox without its config, in which a Loadline
// command would take the default port and the claude on PATH, and a config
// that lets a server a hook starts open the browser.
func (s *sandbox) verifyConfig() error {
	data, err := os.ReadFile(filepath.Join(s.loadlineHome, "config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("the sandbox has no config yet: a Loadline command would take the default port and the claude on PATH")
	}
	if err != nil {
		return err
	}
	var config struct {
		Port        int    `json:"port"`
		ClaudePath  string `json:"claudePath"`
		OpenOnStart string `json:"openOnStart"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("read the config: %w", err)
	}
	if config.Port == 0 || config.Port == defaultPort || config.Port != s.port {
		return fmt.Errorf("the config names port %d, want the free port %d", config.Port, s.port)
	}
	if !s.inside(config.ClaudePath) {
		return fmt.Errorf("the config's claudePath %s is outside the tree", config.ClaudePath)
	}
	if config.OpenOnStart != "never" {
		return fmt.Errorf("the config's openOnStart is %q, want never: a server a hook starts would open the browser", config.OpenOnStart)
	}
	return nil
}

// command returns the command that runs exe with args in the sandbox, or
// the reason it would leave it. ctx bounds it.
func (s *sandbox) command(ctx context.Context, exe string, args ...string) (*exec.Cmd, error) {
	if err := s.verify(append([]string{exe}, args...)...); err != nil {
		return nil, fmt.Errorf("the step would leave the sandbox: %w", err)
	}
	cmd := exec.CommandContext(ctx, exe, args...) //nolint:gosec // G204: binaries the test built, in its own tree
	cmd.Env = slices.Clone(s.env)
	cmd.Dir = s.tree
	return cmd, nil
}

// The check is what keeps the real processes from the machine's own setup,
// so it refuses each way out of the sandbox, in whatever form it comes.
func TestSandbox_RefusesEveryWayOutOfTheTree(t *testing.T) {
	outside := filepath.Join(filepath.Dir(t.TempDir()), "elsewhere")
	rows := map[string]struct {
		edit func(s *sandbox)
		// config, when set, leaves the config as the row needs it, in place
		// of the one writeConfig writes.
		config func(t *testing.T, s *sandbox)
		args   []string
	}{
		"an argument outside":            {args: []string{outside}},
		"a flag's value outside":         {args: []string{"--config-dir=" + outside}},
		"a folder variable outside":      {edit: func(s *sandbox) { s.setEnv("LOADLINE_HOME", outside) }},
		"a folder variable left out":     {edit: func(s *sandbox) { s.unsetEnv("LOADLINE_HOME") }},
		"the claude config dir left out": {edit: func(s *sandbox) { s.unsetEnv("CLAUDE_CONFIG_DIR") }},
		"a session's variable":           {edit: func(s *sandbox) { s.env = append(s.env, "CLAUDE_PID=4242") }},
		"a variable off the list":        {edit: func(s *sandbox) { s.env = append(s.env, "GOFLAGS=-v") }},
		"the default port":               {edit: func(s *sandbox) { s.port = defaultPort }},
		"a claude outside":               {edit: func(s *sandbox) { s.claude = filepath.Join(outside, "claude"+exeSuffix()) }},
		// Without a config a Loadline command takes the defaults: the
		// default port, and the claude on the machine's PATH.
		"no config yet": {config: func(t *testing.T, s *sandbox) {
			t.Helper()
			if err := os.Remove(filepath.Join(s.loadlineHome, "config.json")); err != nil {
				t.Fatal(err)
			}
		}},
		// A server a hook or a status line starts would open the browser.
		"a config that lets a server open the dashboard": {config: func(t *testing.T, s *sandbox) {
			t.Helper()
			config := fmt.Sprintf(`{"port":%d,"claudePath":%q,"idleExitMinutes":1}`, s.port, s.claude)
			if err := os.WriteFile(filepath.Join(s.loadlineHome, "config.json"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for name, row := range rows {
		t.Run(name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeConfig(t)
			if err := s.verify(); err != nil {
				t.Fatalf("a fresh sandbox fails its check: %v", err)
			}
			if row.edit != nil {
				row.edit(s)
				s.writeConfig(t)
			}
			if row.config != nil {
				row.config(t, s)
			}

			if err := s.verify(row.args...); err == nil {
				t.Error("the check passed, want it to refuse")
			}
		})
	}
}

// Before the config is written, a Loadline command would take the defaults:
// the default port, and the claude on the machine's PATH. No step that runs
// one is built then.
func TestSandbox_BuildsNoCommandBeforeTheConfigExists(t *testing.T) {
	s := newSandbox(t)
	exe := filepath.Join(s.tree, "build", "loadline"+exeSuffix())

	for _, args := range [][]string{
		{"serve", "--no-open"},
		{"hook"},
		{"stop"},
		{"install", "--yes", "--config-dir", s.configDir},
	} {
		if cmd, err := s.command(t.Context(), exe, args...); err == nil {
			t.Errorf("command(%q) = %v with no config in the sandbox, want a refusal", args, cmd)
		}
	}
}

// setEnv sets the children's variable name to value.
func (s *sandbox) setEnv(name, value string) {
	s.unsetEnv(name)
	s.env = append(s.env, name+"="+value)
}

// unsetEnv leaves the children's variable name out.
func (s *sandbox) unsetEnv(name string) {
	s.env = slices.DeleteFunc(s.env, func(entry string) bool {
		n, _, _ := strings.Cut(entry, "=")
		return strings.EqualFold(n, name)
	})
}
