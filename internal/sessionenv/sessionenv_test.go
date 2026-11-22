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
