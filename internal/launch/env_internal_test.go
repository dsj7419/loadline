package launch

import (
	"os/exec"
	"strings"
	"testing"
)

// A hook runs with its Claude Code session's environment, which carries the
// settings file's env: the ingest token in OTEL_EXPORTER_OTLP_LOGS_HEADERS,
// and any API key or token the user keeps there. The server a hook starts
// runs claude for every account, and opens the browser.

// sessionVars are what a session hands its hooks that belong to that session
// or carry its Claude credentials.
var sessionVars = map[string]string{ //nolint:gosec // G101: canaries, not credentials
	"ANTHROPIC_API_KEY":               "sk-ant-canary",
	"ANTHROPIC_AUTH_TOKEN":            "auth-canary",
	"CLAUDE_CODE_OAUTH_TOKEN":         "oauth-canary",
	"CLAUDE_CONFIG_DIR":               `C:\Users\x\.claude-b`,
	"CLAUDE_CODE_USE_BEDROCK":         "1",
	"CLAUDECODE":                      "1",
	"OTEL_EXPORTER_OTLP_LOGS_HEADERS": "x-loadline-token=canary",
}

// userVars are the rest of the user's environment, which a program the
// server starts may need: a Bedrock or Vertex account's own probe reads the
// cloud credentials, and they do nothing without CLAUDE_CODE_USE_*.
var userVars = map[string]string{ //nolint:gosec // G101: canaries, not credentials
	"AWS_SECRET_ACCESS_KEY":          "aws-canary",
	"GOOGLE_APPLICATION_CREDENTIALS": `C:\Users\x\gcloud.json`,
	"AZURE_CLIENT_SECRET":            "azure-canary",
	"LOADLINE_HOME":                  `D:\loadline`,
	"MY_CLAUDE_NOTES":                "kept",
}

func assertSessionStripped(t *testing.T, what string, cmd *exec.Cmd) {
	t.Helper()
	seen := map[string]string{}
	for _, kv := range cmd.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		seen[strings.ToUpper(name)] = value
	}
	for name := range sessionVars {
		if _, ok := seen[name]; ok {
			t.Errorf("%s inherits %s from the session that started it", what, name)
		}
	}
	for name, value := range userVars {
		if seen[name] != value {
			t.Errorf("%s has %s=%q, want the user's %q", what, name, seen[name], value)
		}
	}
	if seen["PATH"] == "" {
		t.Errorf("%s has no PATH", what)
	}
}

func TestCommands_KeepTheSessionsClaudeVariablesOut(t *testing.T) {
	for name, value := range sessionVars {
		t.Setenv(name, value)
	}
	for name, value := range userVars {
		t.Setenv(name, value)
	}

	server, err := serverCommand("loadline", "serve")
	if err != nil {
		t.Fatal(err)
	}
	assertSessionStripped(t, "the server a hook starts", server)
	browser, err := browserCommand("browser", "http://127.0.0.1:8289/login?code=0")
	if err != nil {
		t.Fatal(err)
	}
	assertSessionStripped(t, "the browser the server opens", browser)
}
