package install_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
			name:     "PowerShell gets the original out of band, whatever its quotes",
			settings: `{"statusLine": {"type": "command", "command": "echo 'it''s' \"q\" ‘a’ $x"}}`,
			shell:    install.ShellPowerShell,
			want:     `& "` + goldenExe + `" tap --run-b64 '` + b64(`echo 'it''s' "q" ‘a’ $x`) + `'`,
		},
		{
			name:     "a plain command follows the pipe as it is",
			settings: `{"statusLine": {"type": "command", "command": "FOO=bar npx -y ccstatusline@latest --theme \"dark mode\" ~/x,y:1+2%"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | FOO=bar npx -y ccstatusline@latest --theme "dark mode" ~/x,y:1+2%`,
		},
		{
			name:     "a list is grouped so the pipe feeds all of it",
			settings: `{"statusLine": {"type": "command", "command": "jq -r '.model' 2>/dev/null && echo \"ok\" | tr a-z A-Z"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | { jq -r '.model' 2>/dev/null && echo "ok" | tr a-z A-Z` + "\n}",
		},
		{
			name:     "a reserved first word is grouped",
			settings: `{"statusLine": {"type": "command", "command": "time status.sh"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | { time status.sh` + "\n}",
		},
		{
			name:     "a script of several lines is grouped",
			settings: `{"statusLine": {"type": "command", "command": "read line\necho \"$line\""}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | { read line` + "\n" + `echo "$line"` + "\n}",
		},
		{
			name:     "an old plain prefix is replaced, not doubled",
			settings: `{"statusLine": {"type": "command", "command": "\"D:/old/loadline.exe\" tap | my-status"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | my-status`,
		},
		{
			name:     "an old grouped prefix is replaced, not doubled",
			settings: `{"statusLine": {"type": "command", "command": "\"D:/old/loadline.exe\" tap | { a && b\n}"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | { a && b` + "\n}",
		},
		{
			name:     "a list behind an old plain prefix gets grouped",
			settings: `{"statusLine": {"type": "command", "command": "\"D:/old/loadline.exe\" tap | a && b"}}`,
			shell:    install.ShellBash,
			want:     `"` + goldenExe + `" tap | { a && b` + "\n}",
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
			want:     `"` + goldenExe + `" tap | { echo it's; x` + "\n}",
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
		`{"statusLine": {"type": "command", "command": 7}}`,
		`{"statusLine": "status.sh"}`,
	} {
		t.Run(settings, func(t *testing.T) {
			out, changes, warnings := plan(t, []byte(settings), options(install.ShellBash))

			before := gjson.Get(settings, "statusLine").Raw
			if got := gjson.GetBytes(out, "statusLine").Raw; got != before {
				t.Errorf("statusLine = %s, want it left as %s", got, before)
			}
			if !hasChange(changes, "statusLine", install.ActionSkip) {
				t.Errorf("changes %+v lack a skip for statusLine", changes)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], "statusLine") {
				t.Errorf("warnings = %q, want one about the status line", warnings)
			}
		})
	}
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestPlan_GroupsWithAReadableNewline(t *testing.T) {
	settings := []byte(`{"statusLine": {"type": "command", "command": "a && b"}}`)

	out, _, _ := plan(t, settings, options(install.ShellBash))

	if raw := gjson.GetBytes(out, "statusLine.command").Raw; !strings.HasSuffix(raw, `{ a && b\n}"`) {
		t.Errorf("statusLine.command = %s, want the group closed by an escaped \\n", raw)
	}
}

func TestPlan_SkipsAnEditedLoadlineStatusLine(t *testing.T) {
	for _, command := range []string{
		`"C:/x/loadline.exe" tap|foo`,
		`"C:/x/loadline.exe" tap --print --wide`,
		`"C:/x/loadline.exe" tap | { foo`,
		`& "C:/x/loadline.exe" tap | foo`,
		`& "C:/x/loadline.exe" tap --run 'foo'`,
		`& "C:/x/loadline.exe" tap --run-b64 'not*base64'`,
		`& "C:/x/loadline.exe" tap --run-b64 '` + b64("foo") + `' extra`,
		`loadline tap | foo`,
		`/opt/loadline/bin/loadline status`,
	} {
		t.Run(command, func(t *testing.T) {
			settings := []byte(`{"statusLine": {"type": "command", "command": ` + jsonQuote(t, command) + `}}`)

			for _, shell := range shells {
				out, changes, warnings := plan(t, settings, options(shell))

				if got := statusCommand(t, out); got != command {
					t.Errorf("shell %d: status line = %q, want it left as %q", shell, got, command)
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
	settings := []byte(`{"statusLine": {"type": "command", "command": "status.sh --wide \\"}}`)

	out, changes, warnings := plan(t, settings, options(install.ShellBash))

	if got := statusCommand(t, out); got != `status.sh --wide \` {
		t.Errorf("status line = %q, want it left alone", got)
	}
	if !hasChange(changes, "statusLine.command", install.ActionSkip) || len(warnings) != 1 {
		t.Errorf("changes %+v and warnings %q, want a skip with a warning", changes, warnings)
	}

	escaped := []byte(`{"statusLine": {"type": "command", "command": "echo a\\\\"}}`)
	out, _, _ = plan(t, escaped, options(install.ShellBash))
	if got, want := statusCommand(t, out), `"`+goldenExe+`" tap | { echo a\\`+"\n}"; got != want {
		t.Errorf("status line = %q, want an escaped backslash grouped: %q", got, want)
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
		"typographic quote":     func(o *install.Options) { o.Exe = "C:/a\u201cb/loadline.exe" },
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
	for _, exe := range []string{goldenExe, otherExe, "//server/share/loadline.exe", "c:/loadline.exe"} {
		o := options(install.ShellBash)
		o.Exe = exe

		if _, _, _, err := install.Plan([]byte(`{}`), o); err != nil {
			t.Errorf("Plan rejected exe %q: %v", exe, err)
		}
	}
}
