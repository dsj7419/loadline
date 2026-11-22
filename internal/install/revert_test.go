package install_test

import (
	"bytes"
	"encoding/json"
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
		untouched
	)
	tests := []struct {
		name    string
		command string
		outcome int
		want    string // the restored command
	}{
		{"bash prefix", `"/opt/loadline/bin/loadline" tap | my-status`, restored, "my-status"},
		{"bash prefix of an empty command", `"/opt/loadline/bin/loadline" tap | `, restored, ""},
		{"PowerShell wrapper", `& "C:/x/loadline.exe" tap --run 'echo ''a'' ‘‘b’’'`, restored, "echo 'a' ‘b’"},
		{"print, bash", `"C:/Program Files/loadline/loadline.exe" tap --print`, removed, ""},
		{"print, PowerShell, any case", `& "C:/x/Loadline.EXE" tap --print`, removed, ""},
		{"another program", `"C:/x/other.exe" tap | foo`, untouched, ""},
		{"unquoted path", `loadline tap | foo`, untouched, ""},
		{"no spaces around the pipe", `"C:/x/loadline.exe" tap|foo`, untouched, ""},
		{"PowerShell call without the wrapper", `& "C:/x/loadline.exe" tap | foo`, untouched, ""},
		{"unterminated PowerShell string", `& "C:/x/loadline.exe" tap --run 'echo`, untouched, ""},
		{"PowerShell string that ends early", `& "C:/x/loadline.exe" tap --run 'a'b'`, untouched, ""},
		{"print with more after it", `"C:/x/loadline.exe" tap --print --wide`, untouched, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command, err := json.Marshal(tt.command)
			if err != nil {
				t.Fatal(err)
			}
			settings := []byte(`{"model": "opus", "statusLine": {"type": "command", "command": ` + string(command) + `, "padding": 1}}`)

			got, changes := revert(t, settings, "")

			switch tt.outcome {
			case untouched:
				if !bytes.Equal(got, settings) || len(changes) != 0 {
					t.Errorf("Revert changed a status line that is not Loadline's: %s, %+v", got, changes)
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

func TestRevert_KeepsTheOriginalEncoding(t *testing.T) {
	// Encoders differ in what they escape. Whatever the file used must survive
	// a round trip untouched, including in the PowerShell form, which doubles
	// the quotes.
	for _, command := range []string{
		`"bash ~/s.sh 2\u003e/dev/null \u0026\u0026 echo \/ok"`,
		`"echo \u0027hi\u0027 \u2019there\u2019 caf\u00e9 \ud83d\ude00 \"q\" \\ \t"`,
		`"echo 'hi' ’there’ café 😀"`,
	} {
		settings := []byte(`{"statusLine": {"type": "command", "command": ` + command + `}}`)
		for _, shell := range shells {
			installed, _, _ := plan(t, settings, options(shell))

			got, _ := revert(t, installed, goldenExe)

			if !bytes.Equal(got, settings) {
				t.Errorf("shell %d: Revert(Plan(x)) = %s, want %s", shell, got, settings)
			}
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
	for _, settings := range []string{``, `{`, `[]`, `{"a": 1,}`} {
		out, changes, err := install.Revert([]byte(settings), goldenExe)

		if !errors.Is(err, install.ErrInvalidSettings) {
			t.Errorf("Revert(%q) err = %v, want ErrInvalidSettings", settings, err)
		}
		if out != nil || changes != nil {
			t.Errorf("Revert(%q) returned output %q and changes %+v alongside the error", settings, out, changes)
		}
	}
}
