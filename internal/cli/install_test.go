package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/dsj7419/loadline/internal/cli"
)

const (
	restartNotice = "Claude Code sessions that are already running will show up fully after you restart them."

	ownerSettings = `{
  "model": "opus",
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Write|Edit",
        "hooks": [
          {
            "type": "command",
            "command": "node C:/Users/example/.claude/hooks/format.js"
          }
        ]
      }
    ]
  },
  "statusLine": {
    "type": "command",
    "command": "bash ~/.claude/statusline.sh",
    "refreshInterval": 5
  },
  "env": {
    "CLAUDE_CODE_USE_POWERSHELL_TOOL": "1"
  }
}
`
	knownToken = "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdef0123"
)

func runWithInput(t *testing.T, stdin string, args ...string) result {
	t.Helper()

	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), args, cli.IO{
		In:  strings.NewReader(stdin),
		Out: &stdout,
		Err: &stderr,
	})
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// sandbox points Loadline's own files at a temporary directory and returns
// it, with a Claude Code config dir holding settings ("" for none).
func sandbox(t *testing.T, settings string) (home, configDir string) {
	t.Helper()

	home = t.TempDir()
	t.Setenv("LOADLINE_HOME", home)
	configDir = t.TempDir()
	if settings != "" {
		writeSettings(t, configDir, settings)
	}
	return home, configDir
}

func writeSettings(t *testing.T, dir, settings string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readSettings(t *testing.T, dir string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func entries(t *testing.T, dir string) []string {
	t.Helper()

	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list))
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

func installedExe(home string) string {
	name := "loadline"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.ToSlash(filepath.Join(home, "bin", name))
}

func TestInstall_DryRunWritesNothing(t *testing.T) {
	home, dir := sandbox(t, ownerSettings)
	path := filepath.Join(dir, "settings.json")

	got := run(t, "install", "--config-dir", dir, "--dry-run")

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", got.code, got.stderr)
	}
	for _, want := range []string{
		"--- " + path + "\n",
		"+++ " + path + " (loadline)\n",
		"hooks.SessionStart",
		"statusLine.command",
		`x-loadline-token=****`,
		installedExe(home),
		"add  bin/" + path2base(installedExe(home)),
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, got.stdout)
		}
	}
	if strings.Contains(got.stdout, "<created-on-install>") {
		t.Errorf("stdout shows the token placeholder unmasked:\n%s", got.stdout)
	}
	if strings.Contains(got.stdout, "Apply these changes?") {
		t.Errorf("a dry run asked to apply:\n%s", got.stdout)
	}
	if readSettings(t, dir) != ownerSettings {
		t.Error("a dry run changed settings.json")
	}
	if names := entries(t, dir); !slices.Equal(names, []string{"settings.json"}) {
		t.Errorf("config dir holds %q after a dry run, want only settings.json", names)
	}
	if names := entries(t, home); len(names) != 0 {
		t.Errorf("a dry run wrote %q to the Loadline home, want nothing (no binary, no token)", names)
	}
}

func path2base(slashPath string) string { return filepath.Base(filepath.FromSlash(slashPath)) }

func testBinary(t *testing.T) []byte {
	t.Helper()

	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestInstall_RestoresTheInstalledBinary(t *testing.T) {
	home, dir := sandbox(t, ownerSettings)
	if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
		t.Fatalf("install: %s", got.stderr)
	}
	exe := filepath.FromSlash(installedExe(home))
	settings := readSettings(t, dir)
	name := "bin/" + filepath.Base(exe)

	for _, damage := range []struct {
		name   string
		action string
		apply  func() error
	}{
		{"altered", "update", func() error { return os.WriteFile(exe, []byte("an older loadline"), 0o600) }},
		{"removed", "add", func() error { return os.Remove(exe) }},
	} {
		t.Run(damage.name, func(t *testing.T) {
			if err := damage.apply(); err != nil {
				t.Fatal(err)
			}

			dry := run(t, "install", "--config-dir", dir, "--dry-run")
			if dry.code != 0 || !strings.Contains(dry.stdout, damage.action+"  "+name) || strings.Contains(dry.stdout, "Already installed.") {
				t.Errorf("dry run: code %d, stdout:\n%s\nwant %s %s listed", dry.code, dry.stdout, damage.action, name)
			}

			fixed := run(t, "install", "--config-dir", dir, "--yes")

			if fixed.code != 0 {
				t.Fatalf("install: code %d, stderr %s", fixed.code, fixed.stderr)
			}
			if got, err := os.ReadFile(exe); err != nil || !bytes.Equal(got, testBinary(t)) {
				t.Errorf("the installed binary was not restored (%v)", err)
			}
			if got := readSettings(t, dir); got != settings {
				t.Error("restoring the binary changed settings.json, which was already wired")
			}
			if names := entries(t, dir); len(names) != 2 {
				t.Errorf("config dir holds %q, want no backup for an unchanged settings file", names)
			}
		})
	}
}

func TestInstall_RejectsAMalformedToken(t *testing.T) {
	home, dir := sandbox(t, ownerSettings)
	if err := os.WriteFile(filepath.Join(home, "token"), []byte("not-a-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 1 || !strings.Contains(got.stderr, "token") {
		t.Errorf("code %d, stderr %q, want a failure about the token", got.code, got.stderr)
	}
	if strings.Contains(got.stdout, "not-a-token") {
		t.Errorf("stdout reveals the token file's contents:\n%s", got.stdout)
	}
	if readSettings(t, dir) != ownerSettings {
		t.Error("settings changed although the token is unusable")
	}
	if names := entries(t, home); !slices.Equal(names, []string{"token"}) {
		t.Errorf("Loadline home holds %q, want only the untouched token file", names)
	}
}

func TestInstall_EditsARepeatedConfigDirOnce(t *testing.T) {
	_, dir := sandbox(t, ownerSettings)
	path := filepath.Join(dir, "settings.json")
	same := []string{dir, dir + string(filepath.Separator), filepath.Join(dir, ".")}
	if runtime.GOOS == "windows" {
		same = append(same, strings.ToUpper(dir))
	}
	args := []string{"install", "--dry-run"}
	for _, d := range same {
		args = append(args, "--config-dir", d)
	}

	got := run(t, args...)

	if got.code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", got.code, got.stderr)
	}
	if n := strings.Count(strings.ToLower(got.stdout), strings.ToLower("--- "+path+"\n")); n != 1 {
		t.Errorf("the settings file is diffed %d times, want once:\n%s", n, got.stdout)
	}
}

func TestInstall_DryRunMasksTheExistingToken(t *testing.T) {
	home, dir := sandbox(t, ownerSettings)
	if err := os.WriteFile(filepath.Join(home, "token"), []byte(knownToken), 0o600); err != nil {
		t.Fatal(err)
	}

	got := run(t, "install", "--config-dir", dir, "--dry-run")

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", got.code, got.stderr)
	}
	if strings.Contains(got.stdout+got.stderr, knownToken) {
		t.Errorf("the output reveals the token:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "x-loadline-token=****") {
		t.Errorf("stdout lacks the masked token:\n%s", got.stdout)
	}
}

func TestInstall_YesInstallsAndUninstallRestores(t *testing.T) {
	home, dir := sandbox(t, ownerSettings)

	installed := run(t, "install", "--config-dir", dir, "--yes")

	if installed.code != 0 {
		t.Fatalf("install exit code = %d, want 0; stderr: %s", installed.code, installed.stderr)
	}
	if !strings.Contains(installed.stdout, restartNotice) {
		t.Errorf("stdout lacks the restart notice:\n%s", installed.stdout)
	}
	settings := []byte(readSettings(t, dir))
	exe := installedExe(home)
	if got := gjson.GetBytes(settings, "hooks.Stop.0.hooks.0.command").Str; got != exe {
		t.Errorf("Stop hook runs %q, want the installed binary %q", got, exe)
	}
	if _, err := os.Stat(filepath.FromSlash(exe)); err != nil {
		t.Errorf("the binary was not installed: %v", err)
	}
	token, err := os.ReadFile(filepath.Join(home, "token"))
	if err != nil {
		t.Fatalf("the token was not created: %v", err)
	}
	if got := gjson.GetBytes(settings, "env.OTEL_EXPORTER_OTLP_LOGS_HEADERS").Str; got != "x-loadline-token="+string(token) {
		t.Errorf("headers = %q, want the created token", got)
	}
	if strings.Contains(installed.stdout+installed.stderr, string(token)) {
		t.Errorf("the output reveals the token:\n%s", installed.stdout)
	}
	if names := entries(t, dir); len(names) != 2 {
		t.Errorf("config dir holds %q, want settings.json and one backup", names)
	}

	again := run(t, "install", "--config-dir", dir, "--yes")

	if again.code != 0 || !strings.Contains(again.stdout, "Already installed.") {
		t.Errorf("second install: code %d, stdout:\n%s", again.code, again.stdout)
	}
	if names := entries(t, dir); len(names) != 2 {
		t.Errorf("config dir holds %q after a no-op install, want no new backup", names)
	}

	removed := run(t, "uninstall", "--config-dir", dir, "--yes")

	if removed.code != 0 {
		t.Fatalf("uninstall exit code = %d, want 0; stderr: %s", removed.code, removed.stderr)
	}
	if got := readSettings(t, dir); got != ownerSettings {
		t.Errorf("uninstall did not restore the original settings:\n%s", got)
	}

	nothing := run(t, "uninstall", "--config-dir", dir, "--yes")

	if nothing.code != 0 || !strings.Contains(nothing.stdout, "Nothing to remove.") {
		t.Errorf("second uninstall: code %d, stdout:\n%s", nothing.code, nothing.stdout)
	}
}

func TestInstall_AsksBeforeWriting(t *testing.T) {
	for _, answer := range []string{"n\n", "no\n", "\n", "", "maybe\n"} {
		t.Run(answer, func(t *testing.T) {
			home, dir := sandbox(t, ownerSettings)

			got := runWithInput(t, answer, "install", "--config-dir", dir)

			if got.code != 1 {
				t.Errorf("exit code = %d, want 1 when the answer is not yes", got.code)
			}
			if !strings.Contains(got.stdout, "Apply these changes? [y/N] ") {
				t.Errorf("stdout lacks the prompt:\n%s", got.stdout)
			}
			if readSettings(t, dir) != ownerSettings {
				t.Error("settings changed without a yes")
			}
			if names := entries(t, home); len(names) != 0 {
				t.Errorf("Loadline home holds %q without a yes, want nothing", names)
			}
		})
	}
}

func TestInstall_AppliesOnYes(t *testing.T) {
	for _, answer := range []string{"y\n", "Y\n", "yes\n", "YES", " yes \r\n"} {
		t.Run(answer, func(t *testing.T) {
			_, dir := sandbox(t, ownerSettings)

			got := runWithInput(t, answer, "install", "--config-dir", dir)

			if got.code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr: %s", got.code, got.stderr)
			}
			if !gjson.Get(readSettings(t, dir), "hooks.Stop").Exists() {
				t.Error("the answer did not apply the changes")
			}
		})
	}
}

// editingAnswer answers yes to the prompt, but only after someone else has
// rewritten the settings file, as an editor might while the prompt waits.
type editingAnswer struct {
	path, settings string
	answered       bool
}

func (a *editingAnswer) Read(p []byte) (int, error) {
	if a.answered {
		return 0, io.EOF
	}
	a.answered = true
	if err := os.WriteFile(a.path, []byte(a.settings), 0o600); err != nil {
		return 0, err
	}
	return copy(p, "y\n"), nil
}

func TestInstall_RefusesWhenSettingsChangeDuringThePrompt(t *testing.T) {
	_, dir := sandbox(t, ownerSettings)
	const edited = `{"model": "sonnet"}`
	var stdout, stderr bytes.Buffer

	code := cli.Run(context.Background(), []string{"install", "--config-dir", dir}, cli.IO{
		In:  &editingAnswer{path: filepath.Join(dir, "settings.json"), settings: edited},
		Out: &stdout,
		Err: &stderr,
	})

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "changed after it was read") || !strings.Contains(stderr.String(), "run the command again") {
		t.Errorf("stderr = %q, want it to explain the refusal", stderr.String())
	}
	if got := readSettings(t, dir); got != edited {
		t.Errorf("settings = %s, want the other edit kept", got)
	}
	if names := entries(t, dir); !slices.Equal(names, []string{"settings.json"}) {
		t.Errorf("config dir holds %q, want no backup from a refused write", names)
	}
}

func TestInstall_RefusesInvalidJSON(t *testing.T) {
	home, dir := sandbox(t, `{"model": `)

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 1 {
		t.Errorf("exit code = %d, want 1", got.code)
	}
	if !strings.Contains(got.stderr, "not valid JSON") || !strings.Contains(got.stderr, filepath.Join(dir, "settings.json")) {
		t.Errorf("stderr = %q, want it to name the file and the problem", got.stderr)
	}
	if readSettings(t, dir) != `{"model": ` {
		t.Error("an invalid settings file was rewritten")
	}
	if names := entries(t, home); len(names) != 0 {
		t.Errorf("Loadline home holds %q after a refusal, want nothing", names)
	}
}

func TestInstall_CreatesMissingSettings(t *testing.T) {
	_, dir := sandbox(t, "")

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", got.code, got.stderr)
	}
	settings := readSettings(t, dir)
	if !gjson.Valid(settings) || !gjson.Get(settings, "hooks.SessionStart").Exists() {
		t.Errorf("created settings lack the hooks:\n%s", settings)
	}
	if names := entries(t, dir); !slices.Equal(names, []string{"settings.json"}) {
		t.Errorf("config dir holds %q, want only the new settings.json", names)
	}
}

func TestInstall_RefusesMissingConfigDir(t *testing.T) {
	home, dir := sandbox(t, "")
	missing := filepath.Join(dir, "typo")

	got := run(t, "install", "--config-dir", missing, "--yes")

	if got.code != 1 || !strings.Contains(got.stderr, missing) {
		t.Errorf("code %d, stderr %q, want a failure naming %s", got.code, got.stderr, missing)
	}
	if names := entries(t, home); len(names) != 0 {
		t.Errorf("Loadline home holds %q after a refusal, want nothing", names)
	}
}

func TestInstall_WarnsAboutTheUsersOwnTelemetry(t *testing.T) {
	_, dir := sandbox(t, `{"env": {"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318"}}`)

	got := run(t, "install", "--config-dir", dir, "--dry-run")

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "OTEL_EXPORTER_OTLP_ENDPOINT") || !strings.Contains(got.stderr, "per-model view will be unavailable") {
		t.Errorf("stderr = %q, want a warning about the user's telemetry", got.stderr)
	}
}

func TestInstall_EditsEveryConfigDir(t *testing.T) {
	_, first := sandbox(t, ownerSettings)
	second := t.TempDir()
	writeSettings(t, second, `{}`)

	got := run(t, "install", "--config-dir", first, "--config-dir", second, "--yes")

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", got.code, got.stderr)
	}
	for _, dir := range []string{first, second} {
		if !gjson.Get(readSettings(t, dir), "hooks.Stop").Exists() {
			t.Errorf("%s was not installed", dir)
		}
	}
}

func TestInstall_DefaultsToTheClaudeDirInHome(t *testing.T) {
	sandbox(t, "")
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	claudeDir := filepath.Join(userHome, ".claude")
	if err := os.Mkdir(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSettings(t, claudeDir, `{}`)

	got := run(t, "install", "--dry-run")

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", got.code, got.stderr)
	}
	// Windows ignores case in paths, and the default dir is normalized to
	// lower case there.
	if want := "--- " + filepath.Join(claudeDir, "settings.json"); !strings.Contains(strings.ToLower(got.stdout), strings.ToLower(want)) {
		t.Errorf("stdout does not diff %s:\n%s", want, got.stdout)
	}
}

func TestInstall_PortFlag(t *testing.T) {
	_, dir := sandbox(t, `{}`)

	got := run(t, "install", "--config-dir", dir, "--dry-run", "--port", "9123")

	if got.code != 0 || !strings.Contains(got.stdout, "http://127.0.0.1:9123/v1/logs") {
		t.Errorf("code %d, stdout:\n%s\nwant the endpoint on port 9123", got.code, got.stdout)
	}

	for _, port := range []string{"80", "70000", "-1"} {
		if bad := run(t, "install", "--config-dir", dir, "--dry-run", "--port", port); bad.code != 2 {
			t.Errorf("--port %s: exit code = %d, want 2", port, bad.code)
		}
	}
}

func TestInstall_RejectsUnknownFlagsAndArguments(t *testing.T) {
	_, dir := sandbox(t, `{}`)

	for _, args := range [][]string{
		{"install", "--config-dir", dir, "--frobnicate"},
		{"install", "--config-dir", dir, "extra"},
		{"uninstall", "--config-dir", dir, "--port", "9123"},
	} {
		if got := run(t, args...); got.code != 2 {
			t.Errorf("%q: exit code = %d, want 2", args, got.code)
		}
	}
}

func TestUninstall_DryRunWritesNothing(t *testing.T) {
	home, dir := sandbox(t, ownerSettings)
	if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
		t.Fatalf("install: %s", got.stderr)
	}
	installed := readSettings(t, dir)
	token, err := os.ReadFile(filepath.Join(home, "token"))
	if err != nil {
		t.Fatal(err)
	}

	got := run(t, "uninstall", "--config-dir", dir, "--dry-run")

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", got.code, got.stderr)
	}
	for _, want := range []string{"remove", "hooks.Stop", `-    "OTEL_EXPORTER_OTLP_LOGS_HEADERS": "x-loadline-token=****"`} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, got.stdout)
		}
	}
	if strings.Contains(got.stdout, string(token)) {
		t.Errorf("the output reveals the token:\n%s", got.stdout)
	}
	if readSettings(t, dir) != installed {
		t.Error("a dry run changed settings.json")
	}
}

func TestUninstall_MissingSettingsHasNothingToRemove(t *testing.T) {
	_, dir := sandbox(t, "")

	got := run(t, "uninstall", "--config-dir", dir, "--yes")

	if got.code != 0 || !strings.Contains(got.stdout, "Nothing to remove.") {
		t.Errorf("code %d, stdout:\n%s", got.code, got.stdout)
	}
	if names := entries(t, dir); len(names) != 0 {
		t.Errorf("uninstall created %q", names)
	}
}

func TestRun_HelpListsInstallAndUninstall(t *testing.T) {
	got := run(t, "help")

	for _, command := range []string{"install", "uninstall"} {
		if !strings.Contains(got.stdout, command) {
			t.Errorf("usage lacks %s:\n%s", command, got.stdout)
		}
	}
}

// LOADLINE_HOME is the data directory itself, which install makes private
// with all it holds, so it must be a folder of Loadline's own.
func TestInstall_RefusesALoadlineHomeThatHoldsOtherThings(t *testing.T) {
	home, dir := sandbox(t, ownerSettings)
	notes := filepath.Join(home, "notes.txt")
	if err := os.WriteFile(notes, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 1 || !strings.Contains(got.stderr, "notes.txt, which is not Loadline's") ||
		!strings.Contains(got.stderr, "LOADLINE_HOME must be a folder of Loadline's own") {
		t.Errorf("install = %d, stderr %q; want 1 and the folder refused", got.code, got.stderr)
	}
	if settings := readSettings(t, dir); settings != ownerSettings {
		t.Errorf("install changed the settings although it refused:\n%s", settings)
	}
	if names := entries(t, home); !slices.Equal(names, []string{"notes.txt"}) {
		t.Errorf("LOADLINE_HOME holds %q after the refusal, want only notes.txt", names)
	}
}

// Install makes the data directory private as soon as it can, but makes a
// new one only once the user has agreed.
func TestInstall_MakesNoDataDirWithoutAYes(t *testing.T) {
	home := filepath.Join(t.TempDir(), "loadline")
	t.Setenv("LOADLINE_HOME", home)
	dir := t.TempDir()
	writeSettings(t, dir, ownerSettings)

	got := runWithInput(t, "n\n", "install", "--config-dir", dir)

	if got.code != 1 {
		t.Errorf("install = %d, want 1 when the answer is no", got.code)
	}
	if _, err := os.Stat(home); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("install made %s without a yes (%v)", home, err)
	}
}

// asideBinary puts, beside the binary install copies into home, one that an
// earlier upgrade moved aside, and returns its path.
func asideBinary(t *testing.T, home string) string {
	t.Helper()

	path := filepath.FromSlash(installedExe(home)) + ".old"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("a binary an upgrade replaced"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// removedLine is what install says once it has removed the binary at path,
// which an upgrade moved aside.
func removedLine(path string) string {
	return "Removed " + filepath.Join("bin", filepath.Base(path)) + ", which an earlier upgrade replaced.\n"
}

// noteLine is what a dry run of install says of the binary at path, which
// an upgrade moved aside.
func noteLine(path string) string {
	return "Note: " + filepath.Join("bin", filepath.Base(path)) + ", left by an earlier upgrade, goes once nothing runs it.\n"
}

// A binary an upgrade moved aside goes as housekeeping, never as a change.
// With nothing else to do, install has nothing to install or to ask about.
func TestInstall_DoesNotClaimToInstallWhenOnlyAsideBinariesGo(t *testing.T) {
	home, dir := sandbox(t, ownerSettings)
	if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
		t.Fatalf("install: %s", got.stderr)
	}
	aside := asideBinary(t, home)

	got := run(t, "install", "--config-dir", dir)

	want := "Already installed.\n" + removedLine(aside)
	if got.code != 0 || got.stdout != want || got.stderr != "" {
		t.Errorf("install = %d, stdout %q, stderr %q; want 0 and %q", got.code, got.stdout, got.stderr, want)
	}
	if _, err := os.Lstat(aside); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s is still there (%v)", aside, err)
	}
}

// Once it has installed, install removes what earlier upgrades moved aside,
// apart from the changes it showed, and says so before the restart notice.
func TestInstall_RemovesAsideBinariesOnceItHasInstalled(t *testing.T) {
	home, dir := sandbox(t, ownerSettings)
	aside := asideBinary(t, home)

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 0 || got.stderr != "" {
		t.Fatalf("install = %d, stderr %q; want 0 and nothing", got.code, got.stderr)
	}
	if want := removedLine(aside) + restartNotice + "\n"; !strings.HasSuffix(got.stdout, want) {
		t.Errorf("stdout does not end with %q:\n%s", want, got.stdout)
	}
	if n := strings.Count(got.stdout, filepath.Base(aside)); n != 1 {
		t.Errorf("stdout names %s %d times, want once, apart from the changes:\n%s", filepath.Base(aside), n, got.stdout)
	}
	if _, err := os.Lstat(aside); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s is still there (%v)", aside, err)
	}
}

// A dry run removes nothing. It notes what earlier upgrades moved aside
// after the changes, as none of them, whether or not anything else is to
// change.
func TestInstall_DryRunNotesAsideBinariesOutsideTheChanges(t *testing.T) {
	for name, installed := range map[string]bool{"everything else up to date": true, "with changes": false} {
		t.Run(name, func(t *testing.T) {
			home, dir := sandbox(t, ownerSettings)
			if installed {
				if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
					t.Fatalf("install: %s", got.stderr)
				}
			}
			aside := asideBinary(t, home)

			got := run(t, "install", "--config-dir", dir, "--dry-run")

			if got.code != 0 || got.stderr != "" {
				t.Errorf("dry run = %d, stderr %q; want 0 and nothing", got.code, got.stderr)
			}
			if installed {
				if want := "Already installed.\n" + noteLine(aside); got.stdout != want {
					t.Errorf("stdout = %q, want %q", got.stdout, want)
				}
			} else {
				if want := "Dry run: nothing was written.\n" + noteLine(aside); !strings.HasSuffix(got.stdout, want) {
					t.Errorf("stdout does not end with %q:\n%s", want, got.stdout)
				}
				if n := strings.Count(got.stdout, filepath.Base(aside)); n != 1 {
					t.Errorf("stdout names %s %d times, want once, apart from the changes:\n%s", filepath.Base(aside), n, got.stdout)
				}
			}
			if _, err := os.Lstat(aside); err != nil {
				t.Errorf("a dry run removed %s: %v", aside, err)
			}
		})
	}
}

// Housekeeping never fails install. What it cannot do is a warning, and
// install says and does all it would have otherwise.
func TestInstall_WarnsOfAsideBinariesItCannotHandleAndSucceeds(t *testing.T) {
	failure := errors.New("the disk is failing")
	for _, tc := range []struct {
		name      string
		installed bool
		args      []string
		says      string
		// tells is what install says of the binary an upgrade moved aside.
		tells func(path string) string
	}{
		{"installing", false, []string{"--yes"}, restartNotice, removedLine},
		{"already installed", true, nil, "Already installed.", removedLine},
		{"dry run", false, []string{"--dry-run"}, "Dry run: nothing was written.", noteLine},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, dir := sandbox(t, ownerSettings)
			if tc.installed {
				if got := run(t, "install", "--config-dir", dir, "--yes"); got.code != 0 {
					t.Fatalf("install: %s", got.stderr)
				}
			}
			aside := asideBinary(t, home)
			cli.FailAsideHousekeeping(t, failure)

			got := run(t, append([]string{"install", "--config-dir", dir}, tc.args...)...)

			if want := "loadline: warning: " + failure.Error() + "\n"; got.code != 0 || got.stderr != want {
				t.Errorf("install = %d, stderr %q; want 0 and %q", got.code, got.stderr, want)
			}
			for _, want := range []string{tc.says, tc.tells(aside)} {
				if !strings.Contains(got.stdout, want) {
					t.Errorf("stdout lacks %q:\n%s", want, got.stdout)
				}
			}
		})
	}
}
