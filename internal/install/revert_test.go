package install_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/dsj7419/loadline/internal/install"
)

func revert(t *testing.T, settings []byte, exe string) ([]byte, []install.Change) {
	t.Helper()

	out, changes, err := install.Revert(settings, exe)
	if err != nil {
		t.Fatalf("Revert: %v", err)
	}
	return out, changes
}

func TestRevert_RoundTrip(t *testing.T) {
	for _, input := range goldenInputs(t) {
		original := readFile(t, input)
		for _, shell := range shells {
			for _, exe := range []string{goldenExe, spacedExe, otherExe} {
				t.Run(filepath.Base(input)+" "+exe, func(t *testing.T) {
					o := options(shell)
					o.Exe = exe
					installed, _, _ := plan(t, original, o)

					for _, revertExe := range []string{exe, ""} {
						got, changes := revert(t, installed, revertExe)

						if !bytes.Equal(got, original) {
							t.Errorf("Revert(Plan(x), %q) != x:\n%s", revertExe, install.Diff("x", original, got))
						}
						if !install.HasEdits(changes) {
							t.Errorf("Revert(Plan(x), %q) reported no changes", revertExe)
						}
					}
				})
			}
		}
	}
}

func TestRevert_GoldenOutputs(t *testing.T) {
	for _, input := range goldenInputs(t) {
		t.Run(filepath.Base(input), func(t *testing.T) {
			want := readFile(t, input)

			got, changes := revert(t, readFile(t, goldenOutput(input)), goldenExe)

			if !bytes.Equal(got, want) {
				t.Errorf("Revert of %s differs from %s:\n%s", goldenOutput(input), input, install.Diff("want", want, got))
			}
			for _, c := range changes {
				if c.Action != install.ActionRemove && c.Action != install.ActionUpdate {
					t.Errorf("change %+v, want only removals and the restored status line", c)
				}
			}
		})
	}
}

func TestRevert_NothingToRemove(t *testing.T) {
	for _, input := range goldenInputs(t) {
		t.Run(filepath.Base(input), func(t *testing.T) {
			original := readFile(t, input)

			got, changes := revert(t, original, goldenExe)

			if len(changes) != 0 {
				t.Errorf("changes = %+v, want none", changes)
			}
			if !bytes.Equal(got, original) {
				t.Errorf("Revert changed a file with nothing of Loadline's in it:\n%s", install.Diff("x", original, got))
			}
		})
	}
}

func TestRevert_LeavesForeignHooks(t *testing.T) {
	settings := []byte(`{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {"type": "command", "command": "node notify.js"},
          {"type": "command", "command": "C:/x/loadline.exe", "args": ["hook"], "async": true}
        ]
      },
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "C:/tools/loadline-helper.exe", "args": ["hook"]},
          {"type": "command", "command": "C:/x/loadline.exe", "args": ["tap"]},
          {"type": "command", "command": "\"C:/x/loadline.exe\" hook"},
          {"type": "command", "command": "loadline hook"}
        ]
      }
    ],
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "/opt/loadline/bin/loadline", "args": ["hook"]}]}
    ]
  }
}`)
	want := []byte(`{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {"type": "command", "command": "node notify.js"}
        ]
      },
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "C:/tools/loadline-helper.exe", "args": ["hook"]},
          {"type": "command", "command": "C:/x/loadline.exe", "args": ["tap"]},
          {"type": "command", "command": "\"C:/x/loadline.exe\" hook"},
          {"type": "command", "command": "loadline hook"}
        ]
      }
    ]
  }
}`)

	got, changes := revert(t, settings, goldenExe)

	if !bytes.Equal(got, want) {
		t.Errorf("Revert result differs:\n%s", install.Diff("want", want, got))
	}
	if len(changes) != 2 || !hasChange(changes, "hooks.Stop", install.ActionRemove) ||
		!hasChange(changes, "hooks.SessionStart", install.ActionRemove) {
		t.Errorf("changes = %+v, want removals from hooks.Stop and hooks.SessionStart", changes)
	}
}

func TestRevert_RecognizesTheExePathItWasGiven(t *testing.T) {
	const custom = "D:/tools/ll.exe"
	settings := []byte(`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "` + custom + `", "args": ["hook"]}]}]},` +
		` "statusLine": {"type": "command", "command": "\"` + custom + `\" tap | status.sh"}}`)

	got, _ := revert(t, settings, custom)

	if want := `{"statusLine": {"type": "command", "command": "status.sh"}}`; string(got) != want {
		t.Errorf("Revert = %s, want %s", got, want)
	}
	if untouched, _ := revert(t, settings, goldenExe); !bytes.Equal(untouched, settings) {
		t.Errorf("Revert removed entries of an exe named neither loadline nor %s: %s", goldenExe, untouched)
	}
}

func TestRevert_StatusLineForms(t *testing.T) {
	const (
		restored = iota
		removed
		skipped // starts with loadline but is not a form install writes
		untouched
	)
	tests := []struct {
		name    string
		command string
		outcome int
		want    string // the restored command
	}{
		{"plain bash prefix", `"/opt/loadline/bin/loadline" tap | my-status`, restored, "my-status"},
		{"plain bash prefix of an empty command", `"/opt/loadline/bin/loadline" tap | `, removed, ""},
		{"wrapped blank command", "\"/opt/loadline/bin/loadline\" tap | ( (  \n) )", removed, ""},
		{"PowerShell form of a blank command", `& "C:/x/loadline.exe" tap --run-b64 '` + b64(" ") + `'`, removed, ""},
		{"wrapped bash prefix", "\"/opt/loadline/bin/loadline\" tap | ( ( a && b\n) )", restored, "a && b"},
		{"wrapped bash prefix of a subshell", "\"/opt/loadline/bin/loadline\" tap | ( ( ( a\n)\n) )", restored, "( a\n)"},
		{"wrapped bash prefix of an earlier build's group", "\"/opt/loadline/bin/loadline\" tap | ( ( { a\n}\n) )", restored, "{ a\n}"},
		// Earlier builds wrapped the same originals in a group.
		{"earlier build's group", "\"/opt/loadline/bin/loadline\" tap | { a && b\n}", restored, "a && b"},
		{"earlier build's group of a group", "\"/opt/loadline/bin/loadline\" tap | { { a\n}\n}", restored, "{ a\n}"},
		{"earlier build's group of the wrap", "\"/opt/loadline/bin/loadline\" tap | { ( ( a\n) )\n}", restored, "( ( a\n) )"},
		{"earlier build's group of a blank command", "\"/opt/loadline/bin/loadline\" tap | {  \n}", removed, ""},
		// An earlier build wrapped originals that held only comments; they
		// are the user's own lines and come back.
		{"earlier build's group of a comment-only command", "\"/opt/loadline/bin/loadline\" tap | { # my status\n}", restored, "# my status"},
		{"PowerShell form of a comment-only command", `& "C:/x/loadline.exe" tap --run-b64 '` + b64("# my status") + `'`, restored, "# my status"},
		{"PowerShell form", `& "C:/x/loadline.exe" tap --run-b64 '` + b64("echo 'a' \"b\" ‘c’") + `'`, restored, "echo 'a' \"b\" ‘c’"},
		{"print, bash", `"C:/Program Files/loadline/loadline.exe" tap --print`, removed, ""},
		{"print, PowerShell, any case", `& "C:/x/Loadline.EXE" tap --print`, removed, ""},
		{"no spaces around the pipe", `"C:/x/loadline.exe" tap|foo`, skipped, ""},
		{"unclosed group", `"C:/x/loadline.exe" tap | { foo`, skipped, ""},
		{"unclosed subshells", `"C:/x/loadline.exe" tap | ( ( foo`, skipped, ""},
		{"subshells closed on the same line", `"C:/x/loadline.exe" tap | ( ( foo ) )`, skipped, ""},
		{"pipe in PowerShell", `& "C:/x/loadline.exe" tap | foo`, skipped, ""},
		{"PowerShell quoting of an older form", `& "C:/x/loadline.exe" tap --run 'echo'`, skipped, ""},
		{"padded base64", `& "C:/x/loadline.exe" tap --run-b64 '` + base64.URLEncoding.EncodeToString([]byte("ab")) + `'`, skipped, ""},
		{"base64 of invalid UTF-8", `& "C:/x/loadline.exe" tap --run-b64 '` + base64.RawURLEncoding.EncodeToString([]byte{0xff}) + `'`, skipped, ""},
		// Both decode, but neither is the encoding install writes.
		{"base64 broken across lines", `& "C:/x/loadline.exe" tap --run-b64 'ZXhp` + "\n" + `dCAz'`, skipped, ""},
		{"base64 with stray trailing bits", `& "C:/x/loadline.exe" tap --run-b64 'YR'`, skipped, ""},
		{"print with more after it", `"C:/x/loadline.exe" tap --print --wide`, skipped, ""},
		{"unquoted loadline", `loadline tap | foo`, skipped, ""},
		{"blanks ahead of the prefix", `  "C:/x/loadline.exe" tap | foo`, skipped, ""},
		{"blanks ahead of the PowerShell form", ` & "C:/x/loadline.exe" tap --run-b64 '` + b64("foo") + `'`, skipped, ""},
		{"blanks after the PowerShell call", `&  "C:/x/loadline.exe" tap --print`, skipped, ""},
		{"no blank after the PowerShell call", `&"C:/x/loadline.exe" tap --print`, skipped, ""},
		// Install quotes the program only in ASCII double quotes.
		{"single quotes, no blank after the PowerShell call", `&'C:/x/loadline.exe' tap --print`, skipped, ""},
		{"single quotes in the PowerShell form", `& 'C:/x/loadline.exe' tap --run-b64 '` + b64("foo") + `'`, skipped, ""},
		{"single quotes in the bash form", `'C:/x/loadline.exe' tap | cat`, skipped, ""},
		{"typographic single quotes", `& ‘C:/x/loadline.exe’ tap --print`, skipped, ""},
		{"typographic double quotes", `& “C:/x/loadline.exe” tap --print`, skipped, ""},
		{"a doubled single quote in the path", `& 'C:/Users/O''Brien/loadline.exe' tap --print`, skipped, ""},
		{"a PowerShell call of the user's own", `&"C:/x/other.exe" --print`, untouched, ""},
		{"a command of the user's own that starts with a blank", ` cat ~/notes/loadline`, untouched, ""},
		{"another program", `"C:/x/other.exe" tap | foo`, untouched, ""},
		{"loadline later in the command", `status.sh | "C:/x/loadline.exe" tap`, untouched, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := []byte(`{"model": "opus", "statusLine": {"type": "command", "command": ` + jsonQuote(t, tt.command) + `, "padding": 1}}`)

			got, changes := revert(t, settings, "")

			switch tt.outcome {
			case untouched:
				if !bytes.Equal(got, settings) || len(changes) != 0 {
					t.Errorf("Revert changed a status line that is not Loadline's: %s, %+v", got, changes)
				}
			case skipped:
				if !bytes.Equal(got, settings) || install.HasEdits(changes) {
					t.Errorf("Revert edited a status line it does not recognize: %s, %+v", got, changes)
				}
				if !hasChange(changes, "statusLine.command", install.ActionSkip) {
					t.Errorf("changes = %+v, want a skip saying the command was edited", changes)
				}
			case removed:
				if want := `{"model": "opus"}`; string(got) != want {
					t.Errorf("Revert = %s, want the status line removed: %s", got, want)
				}
				if !hasChange(changes, "statusLine", install.ActionRemove) {
					t.Errorf("changes = %+v, want statusLine removed", changes)
				}
			default:
				if restored := statusCommand(t, got); restored != tt.want {
					t.Errorf("restored command = %q, want %q", restored, tt.want)
				}
				if got := gjson.GetBytes(got, "statusLine.padding").Raw; got != "1" {
					t.Errorf("padding = %s, want it kept", got)
				}
				if !hasChange(changes, "statusLine.command", install.ActionUpdate) {
					t.Errorf("changes = %+v, want statusLine.command restored", changes)
				}
			}
		})
	}
}

func TestRevert_KeepsTheOriginalEncodingInTheBashForms(t *testing.T) {
	// Encoders differ in what they escape. The bash forms carry the user's
	// command as the file encoded it, so a round trip restores those bytes.
	for _, command := range []string{
		`"bash ~/s.sh 2\u003e/dev/null \u0026\u0026 echo \/ok"`,
		`"echo \u0027hi\u0027 \u2019there\u2019 caf\u00e9 \ud83d\ude00 \"q\" \\ \t"`,
		`"echo 'hi' ’there’ café 😀"`,
		`"status.sh --plain"`,
	} {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + command + `}}`)
		installed, _, _ := plan(t, settings, options(install.ShellBash))

		got, _ := revert(t, installed, goldenExe)

		if !bytes.Equal(got, settings) {
			t.Errorf("Revert(Plan(x)) = %s, want %s", got, settings)
		}
	}
}

func TestRevert_RestoresThePowerShellOriginal(t *testing.T) {
	// The PowerShell form carries the command's text, not its encoding: a
	// file encoded as Claude Code writes it comes back byte for byte, and
	// any other encoding comes back as the same text.
	tests := map[string]string{
		`"echo 'hi' \"q\" ’there’ café 😀 \\ \t\n"`: `"echo 'hi' \"q\" ’there’ café 😀 \\ \t\n"`,
		`"a \u003e b \/ c"`: `"a > b / c"`,
	}
	for command, want := range tests {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + command + `}}`)
		installed, _, _ := plan(t, settings, options(install.ShellPowerShell))

		got, _ := revert(t, installed, goldenExe)

		if wantSettings := `{"statusLine": {"type": "command", "command": ` + want + `}}`; string(got) != wantSettings {
			t.Errorf("Revert(Plan(%s)) = %s, want %s", command, got, wantSettings)
		}
	}
}

func TestRevert_RemovesOnlyOwnEnv(t *testing.T) {
	settings := []byte(`{"env": {"EDITOR": "vim", "OTEL_RESOURCE_ATTRIBUTES": "team=a"}}`)
	installed, _, _ := plan(t, []byte(`{"env": {"EDITOR": "vim", "OTEL_EXPORTER_OTLP_LOGS_HEADERS": "x-loadline-token=old", "OTEL_RESOURCE_ATTRIBUTES": "team=a"}}`), options(install.ShellBash))

	got, _ := revert(t, installed, goldenExe)

	if env := gjson.GetBytes(got, "env").Raw; env != gjson.GetBytes(settings, "env").Raw {
		t.Errorf("env = %s, want %s", env, gjson.GetBytes(settings, "env").Raw)
	}
}

func TestRevert_LeavesEnvThatIsNotLoadlines(t *testing.T) {
	settings := []byte(`{"env": {"CLAUDE_CODE_ENABLE_TELEMETRY": "1", "OTEL_LOGS_EXPORTER": "otlp", "OTEL_EXPORTER_OTLP_LOGS_HEADERS": "authorization=x"}}`)

	got, changes := revert(t, settings, goldenExe)

	if !bytes.Equal(got, settings) || len(changes) != 0 {
		t.Errorf("Revert touched the user's OpenTelemetry settings: %s, %+v", got, changes)
	}
}

func TestRevert_RejectsInvalidJSON(t *testing.T) {
	for _, settings := range []string{``, `{`, `[]`, `{"a": 1,}`, duplicatedKeys, `{"env": {"A": "1", "A": "2"}}`} {
		out, changes, err := install.Revert([]byte(settings), goldenExe)

		if !errors.Is(err, install.ErrInvalidSettings) {
			t.Errorf("Revert(%q) err = %v, want ErrInvalidSettings", settings, err)
		}
		if out != nil || changes != nil {
			t.Errorf("Revert(%q) returned output %q and changes %+v alongside the error", settings, out, changes)
		}
	}
}

// Revert removes the containers it empties, not the ones the user left
// empty.
func TestRevert_LeavesTheUsersOwnEmptyHooks(t *testing.T) {
	own := `{"type": "command", "command": "/opt/loadline/bin/loadline", "args": ["hook"]}`
	tests := map[string]struct{ settings, want string }{
		"an empty hooks object, with a status line to revert": {
			settings: `{"hooks": {}, "statusLine": {"type": "command", "command": "\"/opt/loadline/bin/loadline\" tap | status.sh"}}`,
			want:     `{"hooks": {}, "statusLine": {"type": "command", "command": "status.sh"}}`,
		},
		"an empty event beside one Loadline empties": {
			settings: `{"hooks": {"Stop": [], "SessionStart": [{"hooks": [` + own + `]}]}}`,
			want:     `{"hooks": {"Stop": []}}`,
		},
		"an empty event and nothing of Loadline's": {
			settings: `{"hooks": {"Stop": []}}`,
			want:     `{"hooks": {"Stop": []}}`,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, _ := revert(t, []byte(tt.settings), "")

			if string(got) != tt.want {
				t.Errorf("Revert = %s, want %s", got, tt.want)
			}
		})
	}
}

// Only headers that start with Loadline's own make the telemetry settings
// Loadline's, however else they mention it.
func TestPlanAndRevert_LeaveHeadersThatOnlyMentionLoadline(t *testing.T) {
	for _, headers := range []string{
		"authorization=Bearer abc,x-loadline-token=abc",
		"x-api-key=loadline-dashboard",
		"x-loadline-token",
	} {
		t.Run(headers, func(t *testing.T) {
			settings := []byte(`{"env": {"OTEL_EXPORTER_OTLP_LOGS_HEADERS": ` + jsonQuote(t, headers) + `, "OTEL_LOGS_EXPORTER": "otlp"}}`)

			installed, changes, warnings := plan(t, settings, options(install.ShellBash))

			if got, want := gjson.GetBytes(installed, "env").Raw, gjson.GetBytes(settings, "env").Raw; got != want {
				t.Errorf("Plan changed env to %s, want the user's %s", got, want)
			}
			if !hasChange(changes, "env", install.ActionSkip) || len(warnings) != 1 {
				t.Errorf("Plan changes %+v and warnings %q, want env skipped with a warning", changes, warnings)
			}
			reverted, removed := revert(t, settings, goldenExe)
			if !bytes.Equal(reverted, settings) || len(removed) != 0 {
				t.Errorf("Revert = %s with %+v, want the user's telemetry untouched", reverted, removed)
			}
		})
	}
}
