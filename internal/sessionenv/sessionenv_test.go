package sessionenv

import (
	"runtime"
	"slices"
	"testing"
)

func TestStrip_TakesOutTheSessionsClaudeVariables(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"ANTHROPIC_API_KEY=sk-ant-canary",
		"ANTHROPIC_BASE_URL=https://proxy.example",
		"CLAUDE_CONFIG_DIR=/home/x/.claude-b",
		"CLAUDE_CODE_OAUTH_TOKEN=oauth-canary",
		"CLAUDE_CODE_USE_VERTEX=1",
		"CLAUDE_PID=4242",
		"CLAUDECODE=1",
		"OTEL_EXPORTER_OTLP_LOGS_HEADERS=x-loadline-token=canary",
		"OTEL_LOGS_EXPORTER=otlp",
		"HOME=/home/x",
	}
	snapshot := slices.Clone(base)

	got := strip(base, false)

	if want := []string{"PATH=/usr/bin", "HOME=/home/x"}; !slices.Equal(got, want) {
		t.Errorf("Strip = %q, want %q", got, want)
	}
	if !slices.Equal(base, snapshot) {
		t.Errorf("Strip modified its input to %q", base)
	}
}

func TestStrip_KeepsTheRestOfTheUsersEnvironment(t *testing.T) {
	base := []string{
		"AWS_SECRET_ACCESS_KEY=aws-canary",
		"AWS_PROFILE=bedrock",
		"GOOGLE_APPLICATION_CREDENTIALS=/home/x/gcloud.json",
		"AZURE_CLIENT_SECRET=azure-canary",
		"LOADLINE_HOME=/srv/loadline",
		"MY_CLAUDE_NOTES=kept",
		"CLAUDE=kept",
		"CLAUDEX=kept",
		"ANTHROPIC=kept",
		"XOTEL_LEVEL=kept",
		"=C:=C:\\work",
		"NOVALUE",
	}

	got := strip(base, false)

	if !slices.Equal(got, base) {
		t.Errorf("Strip = %q, want everything kept: %q", got, base)
	}
}

// Claude Code on Windows finds a Git Bash installed somewhere unusual only
// through CLAUDE_CODE_GIT_BASH_PATH, and it holds no credential, so every
// claude Loadline runs keeps it.
func TestStrip_KeepsTheGitBashPath(t *testing.T) {
	tests := []struct {
		name     string
		base     []string
		foldCase bool
		want     []string
	}{
		{
			name:     "as Claude Code spells it",
			base:     []string{`CLAUDE_CODE_GIT_BASH_PATH=D:\tools\git\bin\bash.exe`, "CLAUDE_CODE_OAUTH_TOKEN=oauth-canary"},
			foldCase: false,
			want:     []string{`CLAUDE_CODE_GIT_BASH_PATH=D:\tools\git\bin\bash.exe`},
		},
		{
			name:     "in another case, where names fold",
			base:     []string{`Claude_Code_Git_Bash_Path=D:\tools\git\bin\bash.exe`, "claude_code_oauth_token=oauth-canary"},
			foldCase: true,
			want:     []string{`Claude_Code_Git_Bash_Path=D:\tools\git\bin\bash.exe`},
		},
		{
			name:     "a longer name that starts the same",
			base:     []string{"CLAUDE_CODE_GIT_BASH_PATH_OLD=x"},
			foldCase: false,
			want:     []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strip(tt.base, tt.foldCase); !slices.Equal(got, tt.want) {
				t.Errorf("Strip = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStrip_KeyCase(t *testing.T) {
	base := []string{"anthropic_api_key=a", "Claude_Config_Dir=b", "ClaudeCode=1", "otel_service_name=c", "PATH=/bin"}

	if got, want := strip(base, true), []string{"PATH=/bin"}; !slices.Equal(got, want) {
		t.Errorf("case-insensitive Strip = %q, want %q", got, want)
	}
	if got := strip(base, false); !slices.Equal(got, base) {
		t.Errorf("case-sensitive Strip = %q, want %q: a differently cased name is another variable", got, base)
	}
}

func TestStrip_FoldsCaseOnWindowsOnly(t *testing.T) {
	got := Strip([]string{"Anthropic_Api_Key=a"})

	if dropped, want := len(got) == 0, runtime.GOOS == "windows"; dropped != want {
		t.Errorf("Strip = %q on %s: dropped a differently cased name = %v, want %v", got, runtime.GOOS, dropped, want)
	}
}
