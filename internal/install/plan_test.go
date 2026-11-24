package install_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/dsj7419/loadline/internal/install"
)

const (
	goldenExe   = "C:/Users/example/AppData/Local/loadline/bin/loadline.exe"
	goldenToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	goldenPort  = 8289

	otherExe   = "/home/example/.local/state/loadline/bin/loadline"
	spacedExe  = "C:/Users/John Smith/AppData/Local/loadline/bin/loadline.exe"
	otherToken = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

	utf8BOM = "\uFEFF"
)

var shells = []install.Shell{install.ShellBash, install.ShellPowerShell}

func options(shell install.Shell) install.Options {
	return install.Options{Exe: goldenExe, Port: goldenPort, Token: goldenToken, Shell: shell}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// goldenInputs returns the input fixtures, each of which has a matching
// .out.json holding the expected result of planning it with the bash options.
func goldenInputs(t *testing.T) []string {
	t.Helper()

	inputs, err := filepath.Glob(filepath.Join("testdata", "*.in.json"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(inputs) < 4 {
		t.Fatalf("found %d input fixtures, want at least 4", len(inputs))
	}
	return inputs
}

func goldenOutput(input string) string {
	return strings.TrimSuffix(input, ".in.json") + ".out.json"
}

func plan(t *testing.T, settings []byte, o install.Options) ([]byte, []install.Change, []string) {
	t.Helper()

	out, changes, warnings, err := install.Plan(settings, o)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return out, changes, warnings
}

// statusCommand returns the decoded status-line command of a settings file.
func statusCommand(t *testing.T, settings []byte) string {
	t.Helper()

	command := gjson.GetBytes(bytes.TrimPrefix(settings, []byte(utf8BOM)), "statusLine.command")
	if command.Type != gjson.String {
		t.Fatalf("statusLine.command is %s, want a string", command.Type)
	}
	return command.Str
}

func editing(changes []install.Change) []install.Change {
	var edits []install.Change
	for _, c := range changes {
		if c.Action != install.ActionSkip {
			edits = append(edits, c)
		}
	}
	return edits
}

func TestPlan_Golden(t *testing.T) {
	for _, input := range goldenInputs(t) {
		t.Run(filepath.Base(input), func(t *testing.T) {
			want := readFile(t, goldenOutput(input))

			got, _, _ := plan(t, readFile(t, input), options(install.ShellBash))

			if !bytes.Equal(got, want) {
				t.Errorf("Plan output differs from %s:\n%s", goldenOutput(input), install.Diff("want", want, got))
			}
		})
	}
}

func TestPlan_ReportsEachChange(t *testing.T) {
	_, changes, warnings := plan(t, readFile(t, "testdata/settings_owner.in.json"), options(install.ShellBash))

	want := []install.Change{
		{Path: "hooks.SessionStart", Action: install.ActionAdd},
		{Path: "hooks.SessionEnd", Action: install.ActionAdd},
		{Path: "hooks.UserPromptSubmit", Action: install.ActionAdd},
		{Path: "hooks.Stop", Action: install.ActionAdd},
		{Path: "hooks.StopFailure", Action: install.ActionAdd},
		{Path: "hooks.PermissionRequest", Action: install.ActionAdd},
		{Path: "hooks.PostToolUse", Action: install.ActionAdd},
		{Path: "hooks.SubagentStart", Action: install.ActionAdd},
		{Path: "hooks.SubagentStop", Action: install.ActionAdd},
		{Path: "hooks.PreCompact", Action: install.ActionAdd},
		{Path: "hooks.PostCompact", Action: install.ActionAdd},
		{Path: "hooks.Notification", Action: install.ActionAdd},
		{Path: "hooks.PreToolUse", Action: install.ActionAdd},
		{Path: "statusLine.command", Action: install.ActionUpdate},
		{Path: "env.CLAUDE_CODE_ENABLE_TELEMETRY", Action: install.ActionAdd},
		{Path: "env.OTEL_LOGS_EXPORTER", Action: install.ActionAdd},
		{Path: "env.OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", Action: install.ActionAdd},
		{Path: "env.OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", Action: install.ActionAdd},
		{Path: "env.OTEL_EXPORTER_OTLP_LOGS_HEADERS", Action: install.ActionAdd},
		{Path: "env.OTEL_LOGS_EXPORT_INTERVAL", Action: install.ActionAdd},
		{Path: "env.OTEL_METRICS_EXPORTER", Action: install.ActionAdd},
	}
	if len(changes) != len(want) {
		t.Fatalf("got %d changes, want %d: %+v", len(changes), len(want), changes)
	}
	for i, c := range changes {
		if c.Path != want[i].Path || c.Action != want[i].Action {
			t.Errorf("change %d = %s %s, want %s %s", i, c.Action, c.Path, want[i].Action, want[i].Path)
		}
		if c.Detail == "" {
			t.Errorf("change %d (%s) has no detail", i, c.Path)
		}
		if strings.Contains(c.Detail, goldenToken) {
			t.Errorf("change %d detail %q reveals the token", i, c.Detail)
		}
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none", warnings)
	}
}

// A hook's change says what it adds and, for an event a matcher narrows,
// which matcher.
func TestPlan_DescribesEachHookWithItsMatcher(t *testing.T) {
	_, changes, _ := plan(t, []byte(`{}`), options(install.ShellBash))

	want := map[string]string{
		"hooks.Stop":         "async loadline hook",
		"hooks.Notification": "async loadline hook for permission_prompt|elicitation_dialog|agent_needs_input|idle_prompt",
		"hooks.PreToolUse":   "async loadline hook for AskUserQuestion",
	}
	for _, c := range changes {
		if detail, ok := want[c.Path]; ok {
			if c.Detail != detail {
				t.Errorf("%s detail = %q, want %q", c.Path, c.Detail, detail)
			}
			delete(want, c.Path)
		}
	}
	if len(want) != 0 {
		t.Errorf("no change for %q", slices.Sorted(maps.Keys(want)))
	}
}

func TestPlan_Idempotent(t *testing.T) {
	for _, input := range goldenInputs(t) {
		for _, shell := range shells {
			t.Run(filepath.Base(input), func(t *testing.T) {
				once, _, _ := plan(t, readFile(t, input), options(shell))

				twice, changes, _ := plan(t, once, options(shell))

				if !bytes.Equal(twice, once) {
					t.Errorf("re-planning changed the output:\n%s", install.Diff("once", once, twice))
				}
				if edits := editing(changes); len(edits) != 0 {
					t.Errorf("re-planning gave edits %+v, want none", edits)
				}
				if install.HasEdits(changes) {
					t.Errorf("HasEdits(%+v) = true, want false", changes)
				}
			})
		}
	}
}

func TestPlan_DifferentExeUpdatesInPlace(t *testing.T) {
	original := readFile(t, "testdata/settings_owner.in.json")
	installed, _, _ := plan(t, original, options(install.ShellBash))
	moved := options(install.ShellBash)
	moved.Exe = otherExe

	out, changes, _ := plan(t, installed, moved)

	if len(changes) != 14 {
		t.Fatalf("got %d changes, want 13 hooks and the status line: %+v", len(changes), changes)
	}
	for _, c := range changes {
		if c.Action != install.ActionUpdate {
			t.Errorf("change %s is %s, want update", c.Path, c.Action)
		}
	}
	if bytes.Contains(out, []byte(goldenExe)) {
		t.Errorf("the old exe path is still in the output:\n%s", out)
	}
	if got, want := statusCommand(t, out), `"`+otherExe+`" tap | bash ~/.claude/statusline.sh`; got != want {
		t.Errorf("status line = %q, want %q (one prefix, not two)", got, want)
	}
	reverted, _ := revert(t, out, otherExe)
	if !bytes.Equal(reverted, original) {
		t.Errorf("reverting after the move did not restore the original:\n%s", install.Diff("original", original, reverted))
	}
}

func TestHookExes(t *testing.T) {
	owner := readFile(t, "testdata/settings_owner.in.json")
	installed, _, _ := plan(t, owner, options(install.ShellBash))
	// One hook run by another binary, as a half-finished move would leave it.
	mixed := []byte(`{"hooks": {
		"SessionStart": [{"hooks": [{"type": "command", "command": "` + goldenExe + `", "args": ["hook"], "async": true}]}],
		"Stop": [{"hooks": [
			{"type": "command", "command": "node format.js"},
			{"type": "command", "command": "` + otherExe + `", "args": ["hook"], "async": true}
		]}],
		"SessionEnd": [{"hooks": [{"type": "command", "command": "` + goldenExe + `", "args": ["hook"], "async": true}]}]
	}}`)

	tests := []struct {
		name     string
		settings []byte
		want     []string
	}{
		{name: "installed", settings: installed, want: []string{goldenExe}},
		{name: "two binaries, in the order install writes the events", settings: mixed, want: []string{goldenExe, otherExe}},
		{name: "the user's own hooks only", settings: owner},
		{name: "loadline run with something other than hook", settings: []byte(
			`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "` + goldenExe + `", "args": ["status"]}]}]}}`)},
		{name: "no hooks", settings: []byte(`{}`)},
		{name: "invalid JSON", settings: []byte(`{"hooks": `)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := install.HookExes(tt.settings); !slices.Equal(got, tt.want) {
				t.Errorf("HookExes = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPlan_NewPortAndTokenUpdateOnlyTheirKeys(t *testing.T) {
	installed, _, _ := plan(t, readFile(t, "testdata/settings_owner.in.json"), options(install.ShellBash))
	changed := options(install.ShellBash)
	changed.Port = 9123
	changed.Token = otherToken

	out, changes, _ := plan(t, installed, changed)

	if len(changes) != 2 {
		t.Fatalf("got changes %+v, want updates of the endpoint and headers", changes)
	}
	for i, path := range []string{"env.OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "env.OTEL_EXPORTER_OTLP_LOGS_HEADERS"} {
		if changes[i].Path != path || changes[i].Action != install.ActionUpdate {
			t.Errorf("change %d = %s %s, want update %s", i, changes[i].Action, changes[i].Path, path)
		}
	}
	if strings.Contains(changes[1].Detail, otherToken) || !strings.Contains(changes[1].Detail, "x-loadline-token=****") {
		t.Errorf("headers detail = %q, want the token masked", changes[1].Detail)
	}
	if got := gjson.GetBytes(out, "env.OTEL_EXPORTER_OTLP_LOGS_ENDPOINT").Str; got != "http://127.0.0.1:9123/v1/logs" {
		t.Errorf("endpoint = %q", got)
	}
	if got := gjson.GetBytes(out, "env.OTEL_EXPORTER_OTLP_LOGS_HEADERS").Str; got != "x-loadline-token="+otherToken {
		t.Errorf("headers = %q", got)
	}
}

func TestPlan_ExePathWithSpaces(t *testing.T) {
	original := readFile(t, "testdata/settings_owner.in.json")
	tests := []struct {
		shell install.Shell
		want  string
	}{
		{install.ShellBash, `"C:/Users/John Smith/AppData/Local/loadline/bin/loadline.exe" tap | bash ~/.claude/statusline.sh`},
		{install.ShellPowerShell, `& "C:/Users/John Smith/AppData/Local/loadline/bin/loadline.exe" tap --run-b64 '` + b64("bash ~/.claude/statusline.sh") + `'`},
	}
	for _, tt := range tests {
		o := options(tt.shell)
		o.Exe = spacedExe

		out, _, _ := plan(t, original, o)

		if got := statusCommand(t, out); got != tt.want {
			t.Errorf("shell %d: status line = %q, want %q", tt.shell, got, tt.want)
		}
		handlers := 0
		gjson.GetBytes(out, "hooks").ForEach(func(event, groups gjson.Result) bool {
			for _, group := range groups.Array() {
				for _, h := range group.Get("hooks").Array() {
					if h.Get("args.0").Str != "hook" {
						continue
					}
					handlers++
					if got := h.Get("command").Str; got != spacedExe {
						t.Errorf("hooks.%s command = %q, want the raw path %q", event.Str, got, spacedExe)
					}
				}
			}
			return true
		})
		if handlers != 13 {
			t.Errorf("found %d loadline handlers, want 13", handlers)
		}
	}
}

func TestPlan_StatusLineForms(t *testing.T) {
	tests := []struct {
		name     string
		settings string
		shell    install.Shell
		want     string
	}{
		{
			name:     "no status line, bash",
			settings: `{}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap --print`,
		},
		{
			name:     "no status line, PowerShell",
			settings: `{}`,
			shell:    install.ShellPowerShell,
			want:     `& "` + goldenExe + `" tap --print`,
		},
		{
			name:     "PowerShell gets the text of the file's escapes, a pair among them, wherever they are",
			settings: "{\"statusLine\": {\"type\": \"command\", \"command\": \"echo \\ud83d\\ude00 caf\\u00e9 \\ud83d\\ude00\"}}",
			shell:    install.ShellPowerShell,
			want:     `& "` + goldenExe + `" tap --run-b64 '` + b64("echo 😀 café 😀") + `'`,
		},
		{
			name:     "PowerShell gets the text of an escape that ends the command",
			settings: "{\"statusLine\": {\"type\": \"command\", \"command\": \"echo caf\\u00e9\"}}",
			shell:    install.ShellPowerShell,
			want:     `& "` + goldenExe + `" tap --run-b64 '` + b64("echo café") + `'`,
		},
		{
			name:     "PowerShell gets the original out of band, whatever its quotes",
			settings: `{"statusLine": {"type": "command", "command": "echo 'it''s' \"q\" ‘a’ $x"}}`,
			shell:    install.ShellPowerShell,
			want:     `& "` + goldenExe + `" tap --run-b64 '` + b64(`echo 'it''s' "q" ‘a’ $x`) + `'`,
		},
		{
			name:     "another program in typographic quotes is the user's own",
			settings: `{"statusLine": {"type": "command", "command": "& ‘C:/x/other.exe’ --print"}}`,
			shell:    install.ShellPowerShell,
			want:     `& "` + goldenExe + `" tap --run-b64 '` + b64(`& ‘C:/x/other.exe’ --print`) + `'`,
		},
		{
			name:     "so is one in typographic quotes the file writes as escapes",
			settings: `{"statusLine": {"type": "command", "command": "& \u2018C:/x/other.exe\u2019 --print"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | ( ( & ‘C:/x/other.exe’ --print` + "\n) )",
		},
		{
			name:     "another program in single quotes is the user's own",
			settings: `{"statusLine": {"type": "command", "command": "'C:/x/other.exe' --flag"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | 'C:/x/other.exe' --flag`,
		},
		{
			name:     "a plain command follows the pipe as it is",
			settings: `{"statusLine": {"type": "command", "command": "FOO=bar npx -y ccstatusline@latest --theme \"dark mode\" ~/x,y:1+2%"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | FOO=bar npx -y ccstatusline@latest --theme "dark mode" ~/x,y:1+2%`,
		},
		{
			name:     "a command that starts with a blank is the user's own, whatever it ends in",
			settings: `{"statusLine": {"type": "command", "command": " cat ~/notes/loadline"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap |  cat ~/notes/loadline`,
		},
		{
			name:     "a list is wrapped so the pipe feeds all of it",
			settings: `{"statusLine": {"type": "command", "command": "jq -r '.model' 2>/dev/null && echo \"ok\" | tr a-z A-Z"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | ( ( jq -r '.model' 2>/dev/null && echo "ok" | tr a-z A-Z` + "\n) )",
		},
		{
			name:     "a reserved first word is wrapped",
			settings: `{"statusLine": {"type": "command", "command": "time status.sh"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | ( ( time status.sh` + "\n) )",
		},
		{
			name:     "a script of several lines is wrapped",
			settings: `{"statusLine": {"type": "command", "command": "read line\necho \"$line\""}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | ( ( read line` + "\n" + `echo "$line"` + "\n) )",
		},
		{
			name:     "an old plain prefix is replaced, not doubled",
			settings: `{"statusLine": {"type": "command", "command": "\"D:/old/loadline.exe\" tap | my-status"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | my-status`,
		},
		{
			name:     "an old wrapped prefix is replaced, not doubled",
			settings: `{"statusLine": {"type": "command", "command": "\"D:/old/loadline.exe\" tap | ( ( a && b\n) )"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | ( ( a && b` + "\n) )",
		},
		{
			name:     "an earlier build's group becomes the wrap",
			settings: `{"statusLine": {"type": "command", "command": "\"D:/old/loadline.exe\" tap | { a && b\n}"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | ( ( a && b` + "\n) )",
		},
		{
			name:     "a list behind an old plain prefix gets wrapped",
			settings: `{"statusLine": {"type": "command", "command": "\"D:/old/loadline.exe\" tap | a && b"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | ( ( a && b` + "\n) )",
		},
		{
			name:     "a bash prefix becomes the PowerShell form",
			settings: `{"statusLine": {"type": "command", "command": "\"D:/old/loadline.exe\" tap | { echo it's && x\n}"}}`,
			shell:    install.ShellPowerShell,
			want:     `& "` + goldenExe + `" tap --run-b64 '` + b64("echo it's && x") + `'`,
		},
		{
			name:     "the PowerShell form becomes the bash prefix",
			settings: `{"statusLine": {"type": "command", "command": "& \"D:/old/loadline.exe\" tap --run-b64 '` + b64("echo it's; x") + `'"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | ( ( echo it's; x` + "\n) )",
		},
		{
			name:     "an old print command keeps printing",
			settings: `{"statusLine": {"type": "command", "command": "\"D:/old/loadline\" tap --print"}}`,
			shell:    install.ShellPowerShell,
			want:     `& "` + goldenExe + `" tap --print`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, _ := plan(t, []byte(tt.settings), options(tt.shell))

			if got := statusCommand(t, out); got != tt.want {
				t.Errorf("status line = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPlan_ReportsTheStatusLineItAdds(t *testing.T) {
	_, changes, _ := plan(t, []byte(`{}`), options(install.ShellBash))

	want := install.Change{Path: "statusLine", Action: install.ActionAdd, Detail: `"` + goldenExe + `" tap --print`}
	if !slices.Contains(changes, want) {
		t.Errorf("changes %+v lack %+v", changes, want)
	}
}

func TestPlan_NewStatusLineRefreshesEveryTenSeconds(t *testing.T) {
	out, _, _ := plan(t, []byte(`{}`), options(install.ShellBash))

	if got := gjson.GetBytes(out, "statusLine.refreshInterval").Raw; got != "10" {
		t.Errorf("refreshInterval = %s, want 10", got)
	}
	if got := gjson.GetBytes(out, "statusLine.type").Str; got != "command" {
		t.Errorf("type = %q, want command", got)
	}
}

func TestPlan_KeepsStatusLineSettings(t *testing.T) {
	settings := []byte(`{"statusLine": {"type": "command", "command": "status.sh", "padding": 2, "refreshInterval": 3}}`)

	out, _, _ := plan(t, settings, options(install.ShellBash))

	if got := gjson.GetBytes(out, "statusLine.padding").Raw; got != "2" {
		t.Errorf("padding = %s, want 2", got)
	}
	if got := gjson.GetBytes(out, "statusLine.refreshInterval").Raw; got != "3" {
		t.Errorf("refreshInterval = %s, want 3", got)
	}
}

func TestPlan_LeavesStatusLineWithoutCommand(t *testing.T) {
	for _, settings := range []string{
		`{"statusLine": {"type": "command", "padding": 2}}`,
		`{"statusLine": {"type": "command", "command": ""}}`,
		`{"statusLine": {"type": "command", "command": " \t "}}`,
		`{"statusLine": {"type": "command", "command": "# only a comment"}}`,
		`{"statusLine": {"type": "command", "command": "\n  # one\r\n\n\t# two\n"}}`,
		`{"statusLine": {"type": "command", "command": 7}}`,
		`{"statusLine": "status.sh"}`,
	} {
		t.Run(settings, func(t *testing.T) {
			for _, shell := range shells {
				out, changes, warnings := plan(t, []byte(settings), options(shell))

				before := gjson.Get(settings, "statusLine").Raw
				if got := gjson.GetBytes(out, "statusLine").Raw; got != before {
					t.Errorf("shell %d: statusLine = %s, want it left as %s", shell, got, before)
				}
				if !hasChange(changes, "statusLine", install.ActionSkip) {
					t.Errorf("shell %d: changes %+v lack a skip for statusLine", shell, changes)
				}
				if len(warnings) != 1 || !strings.Contains(warnings[0], "statusLine") {
					t.Errorf("shell %d: warnings = %q, want one about the status line", shell, warnings)
				}
			}
		})
	}
}

func TestPlan_SkipsAHereDocumentAGroupWouldBreak(t *testing.T) {
	for _, original := range []string{
		"cat <<EOF\nan unterminated here-document",
		"cat <<'EOF'\nterminated\nEOF",
		"cat <<-EOF\n\tindented\n\tEOF",
		"cat<<X\nx\nX",
		// The operator may start the command, ahead of its name.
		"<<EOF cat\nan unterminated here-document",
	} {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, original) + `}}`)

		out, changes, warnings := plan(t, settings, options(install.ShellBash))

		if got := statusCommand(t, out); got != original {
			t.Errorf("status line = %q, want %q left alone", got, original)
		}
		if !hasChange(changes, "statusLine.command", install.ActionSkip) ||
			len(warnings) != 1 || !strings.Contains(warnings[0], "here-document") {
			t.Errorf("%q: changes %+v and warnings %q, want a skip with a warning about the here-document", original, changes, warnings)
		}
	}

	// A here-string is a single word, and PowerShell has no here-documents.
	for original, shell := range map[string]install.Shell{
		`cat <<< "a here-string"`: install.ShellBash,
		"cat <<EOF\nx\nEOF":       install.ShellPowerShell,
	} {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, original) + `}}`)
		var out []byte
		var warnings []string
		var err error

		// Plan takes microseconds here. A search that stopped moving past
		// each here-string it skips would never return.
		within(10*time.Second, "Plan", func() { out, _, warnings, err = install.Plan(settings, options(shell)) })

		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if statusCommand(t, out) == original || len(warnings) != 0 {
			t.Errorf("shell %d: %q left alone with warnings %q, want it tapped", shell, original, warnings)
		}
	}
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestPlan_WrapsWithAReadableNewline(t *testing.T) {
	settings := []byte(`{"statusLine": {"type": "command", "command": "a && b"}}`)

	out, _, _ := plan(t, settings, options(install.ShellBash))

	if raw := gjson.GetBytes(out, "statusLine.command").Raw; !strings.HasSuffix(raw, `( ( a && b\n) )"`) {
		t.Errorf("statusLine.command = %s, want the wrap closed after an escaped \\n", raw)
	}
}

// Earlier builds wrapped originals in a group, which bash 3.2 runs without
// the EXIT trap the original sets. Installing again moves the original into
// the wrap install writes now, keeping its bytes for uninstall.
func TestPlan_UpgradesAnEarlierBuildsGroup(t *testing.T) {
	// As the file encodes it, with an escape few encoders write.
	const original = `trap 'echo bye' EXIT; printf '%s\u0021' \"$(cat)\"`
	settings := []byte(`{"statusLine": {"type": "command", "command": "\"` + goldenExe + `\" tap | { ` + original + `\n}"}}`)

	out, changes, _ := plan(t, settings, options(install.ShellBash))

	want := `"` + goldenExe + `" tap | ( ( trap 'echo bye' EXIT; printf '%s!' "$(cat)"` + "\n) )"
	if got := statusCommand(t, out); got != want {
		t.Errorf("status line = %q, want %q", got, want)
	}
	if !hasChange(changes, "statusLine.command", install.ActionUpdate) {
		t.Errorf("changes = %+v, want statusLine.command updated", changes)
	}
	restored, _ := revert(t, out, goldenExe)
	if got := gjson.GetBytes(restored, "statusLine.command").Raw; got != `"`+original+`"` {
		t.Errorf("uninstall restored %s, want the original's own bytes", got)
	}
}

func TestPlan_SkipsAnEditedLoadlineStatusLine(t *testing.T) {
	for _, command := range []string{
		`"C:/x/loadline.exe" tap|foo`,
		`"C:/x/loadline.exe" tap --print --wide`,
		`"C:/x/loadline.exe" tap | { foo`,
		`"C:/x/loadline.exe" tap | ( ( foo`,
		`"C:/x/loadline.exe" tap | ( ( foo ) )`,
		`& "C:/x/loadline.exe" tap | foo`,
		`& "C:/x/loadline.exe" tap --run 'foo'`,
		`& "C:/x/loadline.exe" tap --run-b64 'not*base64'`,
		`& "C:/x/loadline.exe" tap --run-b64 '` + b64("foo") + `' extra`,
		`loadline tap | foo`,
		`/opt/loadline/bin/loadline status`,
		// Install writes nothing ahead of the program, so blanks there are
		// a hand edit too, however much of the form they leave intact.
		`  "D:/old/loadline.exe" tap | my-status`,
		"\n\t\"C:/x/loadline.exe\" tap | ( ( a && b\n) )",
		` & "C:/x/loadline.exe" tap --run-b64 '` + b64("foo") + `'`,
		`&  "C:/x/loadline.exe" tap --print`,
		`&"C:/x/loadline.exe" tap --print`,
		`  loadline tap | foo`,
		// Install quotes the program only in ASCII double quotes. Any other
		// quotes come from a hand edit or a word processor.
		`&'C:/x/loadline.exe' tap --print`,
		`& 'C:/x/loadline.exe' tap --run-b64 '` + b64("foo") + `'`,
		`'C:/x/loadline.exe' tap | cat`,
		`& ‘C:/x/loadline.exe’ tap --print`,
		`& “C:/x/loadline.exe” tap --print`,
		`& 'C:/Users/O''Brien/loadline.exe' tap --print`,
		// A quote left open, closed elsewhere or around part of the path
		// still leaves a command that bash or PowerShell can read as
		// starting with Loadline.
		`& ‘C:/x/loadline.exe tap --print`,
		`'C:/x/loadline.exe tap | foo`,
		`& 'C:/x/loadline.exe tap --run-b64 '` + b64("foo") + `'`,
		`'C:/x'/loadline.exe tap | foo`,
		`"C:/x"/loadline.exe tap | foo`,
		`& "C:/x/loadline.exe” tap --print`,
		`"C:/x/loadline.exe” tap | echo "hi"`,
		`'C:/Users/Dan’s/loadline.exe' tap | foo`,
	} {
		t.Run(command, func(t *testing.T) {
			settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, command) + `}}`)

			for _, shell := range shells {
				out, changes, warnings := plan(t, settings, options(shell))

				if got := statusCommand(t, out); got != command {
					t.Errorf("shell %d: status line = %q, want it left as %q", shell, got, command)
				}
				if install.UsesPowerShellWrapper(out, goldenExe) {
					t.Errorf("shell %d: UsesPowerShellWrapper = true, want false for a status line install did not write", shell)
				}
				if !hasChange(changes, "statusLine.command", install.ActionSkip) {
					t.Errorf("shell %d: changes %+v lack a skip for statusLine.command", shell, changes)
				}
				if len(warnings) != 1 || !strings.Contains(warnings[0], "loadline") {
					t.Errorf("shell %d: warnings = %q, want one about the edited loadline command", shell, warnings)
				}
			}
		})
	}
}

func TestPlan_SkipsAnOriginalEndingInABackslash(t *testing.T) {
	// Escaped or not: on Windows a doubled backslash can reach Git Bash as a
	// single one and join the group's closing line to the command.
	for _, original := range []string{`status.sh --wide \`, `echo a\\`, `echo a\\\`} {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, original) + `}}`)

		out, changes, warnings := plan(t, settings, options(install.ShellBash))

		if got := statusCommand(t, out); got != original {
			t.Errorf("status line = %q, want %q left alone", got, original)
		}
		if !hasChange(changes, "statusLine.command", install.ActionSkip) ||
			len(warnings) != 1 || !strings.Contains(warnings[0], "backslash") {
			t.Errorf("%q: changes %+v and warnings %q, want a skip with a warning about the backslash", original, changes, warnings)
		}
	}
}

func TestPlan_AnEmptyCommandBehindTheTapPrefixPrints(t *testing.T) {
	for _, command := range []string{
		`"D:/old/loadline.exe" tap | `,
		`"D:/old/loadline.exe" tap |    `,
		"\"D:/old/loadline.exe\" tap | { \t \n}",
		"\"D:/old/loadline.exe\" tap | ( ( \t \n) )",
		`& "D:/old/loadline.exe" tap --run-b64 ''`,
		`& "D:/old/loadline.exe" tap --run-b64 '` + b64("  \n ") + `'`,
	} {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, command) + `}}`)
		for _, shell := range shells {
			out, changes, _ := plan(t, settings, options(shell))

			want := `"` + goldenExe + `" tap --print`
			if shell == install.ShellPowerShell {
				want = "& " + want
			}
			if got := statusCommand(t, out); got != want {
				t.Errorf("shell %d: %q became %q, want the print form %q", shell, command, got, want)
			}
			if !hasChange(changes, "statusLine.command", install.ActionUpdate) {
				t.Errorf("shell %d: changes %+v, want statusLine.command updated", shell, changes)
			}
		}
	}
}

// An earlier build wrapped originals that held only comments. They are the
// user's own lines, so a new plan leaves the wrap for uninstall to undo
// rather than turning it into the print form, which would lose them.
func TestPlan_LeavesACommentOnlyWrapForUninstall(t *testing.T) {
	for _, command := range []string{
		"\"D:/old/loadline.exe\" tap | { # my status\n}",
		`& "D:/old/loadline.exe" tap --run-b64 '` + b64("# my status") + `'`,
	} {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, command) + `}}`)
		for _, shell := range shells {
			out, changes, warnings := plan(t, settings, options(shell))

			if got := statusCommand(t, out); got != command {
				t.Errorf("shell %d: %q became %q, want it left for uninstall", shell, command, got)
			}
			if !hasChange(changes, "statusLine.command", install.ActionSkip) ||
				len(warnings) != 1 || !strings.Contains(warnings[0], "comment") {
				t.Errorf("shell %d: changes %+v and warnings %q, want a skip saying it holds only comments", shell, changes, warnings)
			}
		}
	}
}

func jsonQuote(t *testing.T, s string) string {
	t.Helper()

	quoted, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(quoted)
}

func hasChange(changes []install.Change, path, action string) bool {
	for _, c := range changes {
		if c.Path == path && c.Action == action {
			return true
		}
	}
	return false
}

func TestPlan_SkipsUserOTel(t *testing.T) {
	tests := []struct {
		name     string
		settings string
		keys     []string
	}{
		{
			name:     "an OpenTelemetry collector of the user's own",
			settings: string(readFile(t, "testdata/settings_user_otel.in.json")),
			keys:     []string{"CLAUDE_CODE_ENABLE_TELEMETRY", "OTEL_METRICS_EXPORTER", "OTEL_EXPORTER_OTLP_ENDPOINT"},
		},
		{
			name:     "telemetry switched on alone",
			settings: `{"env": {"CLAUDE_CODE_ENABLE_TELEMETRY": "1"}}`,
			keys:     []string{"CLAUDE_CODE_ENABLE_TELEMETRY"},
		},
		{
			name:     "a lower-case key, which Windows treats as the same variable",
			settings: `{"env": {"otel_logs_exporter": "console"}}`,
			keys:     []string{"otel_logs_exporter"},
		},
		{
			name:     "headers that belong to someone else",
			settings: `{"env": {"OTEL_EXPORTER_OTLP_LOGS_HEADERS": "authorization=Bearer abc"}}`,
			keys:     []string{"OTEL_EXPORTER_OTLP_LOGS_HEADERS"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, changes, warnings := plan(t, []byte(tt.settings), options(install.ShellBash))

			before := gjson.Get(tt.settings, "env").Raw
			if got := gjson.GetBytes(out, "env").Raw; got != before {
				t.Errorf("env = %s, want it left as %s", got, before)
			}
			if !hasChange(changes, "env", install.ActionSkip) {
				t.Errorf("changes %+v lack a skip for env", changes)
			}
			if len(warnings) != 1 {
				t.Fatalf("warnings = %q, want one", warnings)
			}
			for _, key := range tt.keys {
				if !strings.Contains(warnings[0], key) {
					t.Errorf("warning %q does not name %s", warnings[0], key)
				}
			}
			if !strings.Contains(warnings[0], "per-model") {
				t.Errorf("warning %q does not say the per-model view is unavailable", warnings[0])
			}
			if !gjson.GetBytes(out, "hooks.Stop").Exists() {
				t.Error("hooks were not installed alongside the skipped env")
			}
		})
	}
}

func TestPlan_ManagesOwnOTelNextToOtherKeys(t *testing.T) {
	settings := []byte(`{"env": {"OTEL_EXPORTER_OTLP_LOGS_HEADERS": "x-loadline-token=stale", "OTEL_RESOURCE_ATTRIBUTES": "team=a"}}`)

	out, changes, warnings := plan(t, settings, options(install.ShellBash))

	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none", warnings)
	}
	if hasChange(changes, "env", install.ActionSkip) {
		t.Errorf("changes %+v skip env, which is Loadline's", changes)
	}
	if !hasChange(changes, "env.OTEL_EXPORTER_OTLP_LOGS_HEADERS", install.ActionUpdate) {
		t.Errorf("changes %+v do not update the stale headers", changes)
	}
	if got := gjson.GetBytes(out, "env.OTEL_RESOURCE_ATTRIBUTES").Str; got != "team=a" {
		t.Errorf("OTEL_RESOURCE_ATTRIBUTES = %q, want it kept", got)
	}
}

func TestPlan_NeverEnablesContentLogging(t *testing.T) {
	for _, input := range goldenInputs(t) {
		out, _, _ := plan(t, readFile(t, input), options(install.ShellBash))

		if bytes.Contains(out, []byte("OTEL_LOG_")) {
			t.Errorf("%s: output sets an OTEL_LOG_* key", input)
		}
	}
}

func TestPlan_PreservesCRLFAndBOM(t *testing.T) {
	lf := readFile(t, "testdata/settings_owner.in.json")
	crlf := bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n"))
	tests := map[string][]byte{
		"fixture with CRLF, a BOM and tabs": readFile(t, "testdata/settings_crlf_bom.in.json"),
		"CRLF without a BOM":                crlf,
		"BOM with LF":                       append([]byte(utf8BOM), lf...),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			hasBOM := bytes.HasPrefix(input, []byte(utf8BOM))
			usesCRLF := bytes.Contains(input, []byte("\r\n"))

			out, _, _ := plan(t, input, options(install.ShellBash))

			if got := bytes.HasPrefix(out, []byte(utf8BOM)); got != hasBOM {
				t.Errorf("output starts with a BOM: %v, want %v", got, hasBOM)
			}
			body := bytes.TrimPrefix(out, []byte(utf8BOM))
			if bytes.Contains(body, []byte(utf8BOM)) {
				t.Error("output holds a second BOM")
			}
			if !json.Valid(body) {
				t.Fatalf("output is not valid JSON:\n%s", out)
			}
			lineFeeds := bytes.Count(body, []byte("\n"))
			crlfs := bytes.Count(body, []byte("\r\n"))
			if usesCRLF && crlfs != lineFeeds {
				t.Errorf("%d of %d line endings are CRLF, want all", crlfs, lineFeeds)
			}
			if !usesCRLF && crlfs != 0 {
				t.Errorf("output has %d CRLF line endings, want none", crlfs)
			}
			if lineFeeds <= bytes.Count(input, []byte("\n")) {
				t.Errorf("output has %d lines, want more than the input's", lineFeeds)
			}
			reverted, _ := revert(t, out, goldenExe)
			if !bytes.Equal(reverted, input) {
				t.Errorf("Revert did not restore the input byte for byte:\n%q\nwant\n%q", reverted, input)
			}
		})
	}
}

func TestPlan_FollowsCompactAndInlineLayouts(t *testing.T) {
	t.Run("a file on one line stays on one line", func(t *testing.T) {
		input := []byte(`{"model":"opus","env":{"EDITOR":"vim"}}`)

		out, _, _ := plan(t, input, options(install.ShellBash))

		if bytes.ContainsAny(out, "\r\n") {
			t.Errorf("output spans several lines:\n%s", out)
		}
		if !json.Valid(out) {
			t.Fatalf("output is not valid JSON: %s", out)
		}
		if !bytes.HasPrefix(out, []byte(`{"model":"opus","env":{"EDITOR":"vim","CLAUDE_CODE_ENABLE_TELEMETRY":"1",`)) {
			t.Errorf("output does not extend the objects in place: %s", out)
		}
	})
	t.Run("an object on one line in a formatted file stays on its line", func(t *testing.T) {
		input := []byte("{\n  \"env\": {\"EDITOR\": \"vim\"}\n}\n")

		out, _, _ := plan(t, input, options(install.ShellBash))

		want := "\n  \"env\": {\"EDITOR\": \"vim\", \"CLAUDE_CODE_ENABLE_TELEMETRY\": \"1\", \"OTEL_LOGS_EXPORTER\": \"otlp\","
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	})
}

func TestPlan_RepairsOwnHandlerInPlace(t *testing.T) {
	settings := []byte(`{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {"type": "command", "command": "D:/old/loadline.exe", "args": ["hook"], "timeout": 5}
        ]
      }
    ]
  }
}`)

	out, changes, _ := plan(t, settings, options(install.ShellBash))

	if !hasChange(changes, "hooks.Stop", install.ActionUpdate) {
		t.Errorf("changes %+v do not update hooks.Stop", changes)
	}
	handler := gjson.GetBytes(out, "hooks.Stop.0.hooks.0")
	if got := handler.Get("command").Str; got != goldenExe {
		t.Errorf("command = %q, want %q", got, goldenExe)
	}
	if !handler.Get("async").Bool() {
		t.Error("async was not switched on")
	}
	if got := handler.Get("timeout").Raw; got != "5" {
		t.Errorf("timeout = %q, want the user's 5 kept", got)
	}
	if got := len(gjson.GetBytes(out, "hooks.Stop").Array()); got != 1 {
		t.Errorf("hooks.Stop has %d groups, want the one repaired group", got)
	}
}

func TestPlan_FindsOwnHandlerInsideAUserGroup(t *testing.T) {
	settings := []byte(`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "notify.sh"}, ` +
		`{"type": "command", "command": "` + goldenExe + `", "args": ["hook"], "async": true}]}]}}`)

	out, changes, _ := plan(t, settings, options(install.ShellBash))

	if hasChange(changes, "hooks.Stop", install.ActionAdd) || hasChange(changes, "hooks.Stop", install.ActionUpdate) {
		t.Errorf("changes %+v touch hooks.Stop, which is already wired", changes)
	}
	if got := gjson.GetBytes(out, "hooks.Stop").Raw; got != gjson.GetBytes(settings, "hooks.Stop").Raw {
		t.Errorf("hooks.Stop = %s, want it unchanged", got)
	}
}

func TestPlan_RejectsInvalidJSON(t *testing.T) {
	for _, settings := range []string{
		``,
		"  \n",
		`{`,
		`{"a":}`,
		`{"a": 1,}`,
		`{} {}`,
		"// comment\n{}",
		utf8BOM + `{`,
		`[]`,
		`"settings"`,
		`null`,
		`{"hooks": []}`,
		`{"hooks": {"Stop": {}}}`,
		`{"hooks": {"Stop": "loadline"}}`,
		`{"env": "OTEL"}`,
		duplicatedKeys,
		`{"model": "opus", "model": "sonnet"}`,
		`{"a": 1, "a": 2}`,
		`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "a", "command": "b"}]}]}}`,
		`{"permissions": {"allow": [], "deny": [], "allow": ["Bash"]}}`,
		`{"hooks": {"Stop": [{"hooks": []}, {"matcher": "a", "hooks": [], "matcher": "b"}]}}`,
	} {
		t.Run(settings, func(t *testing.T) {
			out, changes, _, err := install.Plan([]byte(settings), options(install.ShellBash))

			if !errors.Is(err, install.ErrInvalidSettings) {
				t.Errorf("err = %v, want ErrInvalidSettings", err)
			}
			if out != nil || changes != nil {
				t.Errorf("got output %q and changes %+v alongside the error", out, changes)
			}
		})
	}
}

// duplicatedKeys repeats hooks and env. Claude Code reads the last of each,
// while a JSON path finds the first, so an edit would land on the copy
// Claude Code ignores and an uninstall could delete the hooks it runs.
const duplicatedKeys = `{"hooks":{"PostToolUse":[]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"notify.sh"}]}]},"env":{},"env":{"EDITOR":"vim"}}`

func TestPlan_DuplicateKeyErrorNamesTheKeyAndItsPath(t *testing.T) {
	tests := map[string]struct{ key, where string }{
		`{"model": "opus", "model": "sonnet"}`:                                               {`"model"`, "top level"},
		`{"hooks": {"Stop": [{"hooks": [{"type": "command", "type": "command"}]}]}}`:         {`"type"`, "hooks.Stop[0].hooks[0]"},
		`{"env": {"EDITOR": "vim", "OTEL_LOGS_EXPORTER": "otlp", "EDITOR": "code"}, "x": 1}`: {`"EDITOR"`, "env"},
	}
	for settings, want := range tests {
		_, _, _, err := install.Plan([]byte(settings), options(install.ShellBash))

		if err == nil || !strings.Contains(err.Error(), want.key) || !strings.Contains(err.Error(), want.where) {
			t.Errorf("Plan(%s) err = %v, want it to name %s in %s", settings, err, want.key, want.where)
		}
	}
}

func TestPlan_RejectsUnsafeOptions(t *testing.T) {
	valid := options(install.ShellBash)
	tests := map[string]func(*install.Options){
		"relative exe":          func(o *install.Options) { o.Exe = "bin/loadline.exe" },
		"backslashes":           func(o *install.Options) { o.Exe = `C:\loadline\loadline.exe` },
		"double quote":          func(o *install.Options) { o.Exe = `C:/a"b/loadline.exe` },
		"dollar sign":           func(o *install.Options) { o.Exe = "C:/$HOME/loadline.exe" },
		"backtick":              func(o *install.Options) { o.Exe = "C:/a`b/loadline.exe" },
		"left double quote":     func(o *install.Options) { o.Exe = "C:/a\u201cb/loadline.exe" },
		"right double quote":    func(o *install.Options) { o.Exe = "C:/a\u201db/loadline.exe" },
		"low double quote":      func(o *install.Options) { o.Exe = "C:/a\u201eb/loadline.exe" },
		"newline":               func(o *install.Options) { o.Exe = "C:/a\nb/loadline.exe" },
		"port zero":             func(o *install.Options) { o.Port = 0 },
		"port too high":         func(o *install.Options) { o.Port = 65536 },
		"empty token":           func(o *install.Options) { o.Token = "" },
		"token with a comma":    func(o *install.Options) { o.Token = "abc,def" },
		"token with a space":    func(o *install.Options) { o.Token = "abc def" },
		"no shell":              func(o *install.Options) { o.Shell = 0 },
		"unknown shell":         func(o *install.Options) { o.Shell = 9 },
		"drive without a slash": func(o *install.Options) { o.Exe = "C:loadline.exe" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			o := valid
			mutate(&o)

			_, _, _, err := install.Plan([]byte(`{}`), o)

			if err == nil {
				t.Errorf("Plan accepted %+v", o)
			}
		})
	}
}

func TestPlan_AcceptsAbsoluteSlashPaths(t *testing.T) {
	for _, exe := range []string{goldenExe, otherExe, "//server/share/loadline.exe", "c:/loadline.exe", "A:/loadline.exe", "z:/loadline.exe"} {
		o := options(install.ShellBash)
		o.Exe = exe

		if _, _, _, err := install.Plan([]byte(`{}`), o); err != nil {
			t.Errorf("Plan rejected exe %q: %v", exe, err)
		}
	}
}

func nested(depth int) string {
	// The top-level object is the first level.
	return `{"x": ` + strings.Repeat("[", depth-1) + strings.Repeat("]", depth-1) + `}`
}

func TestPlan_RefusesSettingsNestedTooDeep(t *testing.T) {
	if _, _, _, err := install.Plan([]byte(nested(512)), options(install.ShellBash)); err != nil {
		t.Errorf("Plan refused 512 levels: %v", err)
	}
	// The bound itself, with the smallest input over it. Without the bound a
	// far deeper input would only exhaust memory, so the test stops here
	// before trying one.
	_, _, _, err := install.Plan([]byte(nested(513)), options(install.ShellBash))
	if !errors.Is(err, install.ErrInvalidSettings) || !strings.Contains(err.Error(), "512") {
		t.Fatalf("depth 513: err = %v, want ErrInvalidSettings naming the 512 limit", err)
	}
	if _, _, err := install.Revert([]byte(nested(513)), goldenExe); !errors.Is(err, install.ErrInvalidSettings) {
		t.Fatalf("depth 513: Revert err = %v, want ErrInvalidSettings", err)
	}

	// With the bound in place, far deeper input is refused as quickly.
	start := time.Now()
	if _, _, _, err := install.Plan([]byte(nested(50000)), options(install.ShellBash)); !errors.Is(err, install.ErrInvalidSettings) {
		t.Errorf("depth 50000: err = %v, want ErrInvalidSettings", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("depth 50000 took %s to refuse", elapsed)
	}
	// Brackets inside strings are not nesting.
	deepInText := `{"x": "` + strings.Repeat("[{", 1000) + `"}`
	if _, _, _, err := install.Plan([]byte(deepInText), options(install.ShellBash)); err != nil {
		t.Errorf("Plan refused brackets inside a string: %v", err)
	}
	// Nor are brackets that have closed: arrays side by side, however many,
	// nest no deeper than one of them.
	wide := `{"x": [` + strings.Repeat("[], ", 600) + `[]]}`
	if _, _, _, err := install.Plan([]byte(wide), options(install.ShellBash)); err != nil {
		t.Errorf("Plan refused 601 arrays side by side: %v", err)
	}
}

func TestPlan_AcceptsEveryPortFromOneTo65535(t *testing.T) {
	for _, port := range []int{1, 65535} {
		o := options(install.ShellBash)
		o.Port = port

		out, _, _ := plan(t, []byte(`{}`), o)

		want := "http://127.0.0.1:" + strconv.Itoa(port) + "/v1/logs"
		if got := gjson.GetBytes(out, "env.OTEL_EXPORTER_OTLP_LOGS_ENDPOINT").Str; got != want {
			t.Errorf("port %d: endpoint = %q, want %q", port, got, want)
		}
	}
}

// The bound is on the whole command PowerShell receives: one of exactly 8000
// characters is written, and one a character longer is not.
func TestPlan_WritesAPowerShellFormOfExactlyTheLimit(t *testing.T) {
	// 5942 bytes encode to 7923 base64 characters, which the call around them
	// brings to 8000.
	longest := strings.Repeat("x", 5942)
	if form := `& "` + goldenExe + `" tap --run-b64 '` + b64(longest) + `'`; len(form) != 8000 {
		t.Fatalf("the form of the longest original has %d characters; the test needs one of exactly 8000", len(form))
	}
	for original, fits := range map[string]bool{longest: true, longest + "x": false} {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, original) + `}}`)

		out, _, warnings := plan(t, settings, options(install.ShellPowerShell))

		if tapped := statusCommand(t, out) != original; tapped != fits {
			t.Errorf("%d bytes: tapped = %v, want %v", len(original), tapped, fits)
		}
		if fits && len(warnings) != 0 || !fits && (len(warnings) != 1 || !strings.Contains(warnings[0], "8000")) {
			t.Errorf("%d bytes: warnings = %q, want one naming the limit only when it does not fit", len(original), warnings)
		}
	}
}

// A file may start with a line break. Its line ending is still the one the
// file uses, and reverting gives back its bytes.
func TestPlan_FollowsAFileThatStartsWithALineBreak(t *testing.T) {
	for name, newline := range map[string]string{"LF": "\n", "CRLF": "\r\n"} {
		t.Run(name, func(t *testing.T) {
			input := []byte(newline + "{" + newline + `  "model": "opus"` + newline + "}" + newline)

			out, _, _ := plan(t, input, options(install.ShellBash))

			crlfs, lineFeeds := bytes.Count(out, []byte("\r\n")), bytes.Count(out, []byte("\n"))
			if newline == "\r\n" && crlfs != lineFeeds || newline == "\n" && crlfs != 0 {
				t.Errorf("%d of %d line endings are CRLF, want the file's %q throughout", crlfs, lineFeeds, newline)
			}
			if reverted, _ := revert(t, out, goldenExe); !bytes.Equal(reverted, input) {
				t.Errorf("Revert = %q, want %q", reverted, input)
			}
		})
	}
}

// A line of nothing but blanks, which editors can leave behind, shows no
// indentation; the first line with content sets it.
func TestPlan_CopiesTheIndentOfTheFirstLineWithContent(t *testing.T) {
	input := []byte("{\n    \n  \"model\": \"opus\"\n}\n")

	out, _, _ := plan(t, input, options(install.ShellBash))

	if want := "\n  \"hooks\": {\n    \"SessionStart\": [\n      {\n"; !bytes.Contains(out, []byte(want)) {
		t.Errorf("output lacks %q, the file's two-space indent:\n%s", want, out)
	}
}

// Windows limits a command line to about 8000 characters in places along the
// way, so a PowerShell form longer than that is not written.
func TestPlan_SkipsAPowerShellOriginalTooLongToPass(t *testing.T) {
	for length, fits := range map[int]bool{5000: true, 6000: false} {
		original := "Write-Output '" + strings.Repeat("x", length) + "'"
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, original) + `}}`)

		out, changes, warnings := plan(t, settings, options(install.ShellPowerShell))

		if tapped := statusCommand(t, out) != original; tapped != fits {
			t.Errorf("%d characters: tapped = %v, want %v", length, tapped, fits)
		}
		if !fits && (!hasChange(changes, "statusLine.command", install.ActionSkip) ||
			len(warnings) != 1 || !strings.Contains(warnings[0], "8000")) {
			t.Errorf("%d characters: changes %+v and warnings %q, want a skip naming the limit", length, changes, warnings)
		}
		bashOut, _, _ := plan(t, settings, options(install.ShellBash))
		if statusCommand(t, bashOut) == original {
			t.Errorf("%d characters: the bash form was skipped too", length)
		}
	}
}
