package install

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	// headersKey carries the ingest token, and its value is how Loadline
	// tells its OpenTelemetry settings from anyone else's.
	headersKey   = "OTEL_EXPORTER_OTLP_LOGS_HEADERS"
	headerPrefix = "x-loadline-token="
	maskedToken  = "****"
)

// envVar is an environment variable install sets in the env object.
type envVar struct {
	key   string
	value string
}

// telemetryEnv is the OpenTelemetry setup that sends Claude Code's api_request
// log events to the ingest endpoint every 5 s. Metrics stay off, and so does
// every OTEL_LOG_* switch that would add prompts, tool details or other
// content to the events.
func telemetryEnv(port int, token string) []envVar {
	return []envVar{
		{key: "CLAUDE_CODE_ENABLE_TELEMETRY", value: "1"},
		{key: "OTEL_LOGS_EXPORTER", value: "otlp"},
		{key: "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", value: "http/json"},
		{key: "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", value: "http://127.0.0.1:" + strconv.Itoa(port) + "/v1/logs"},
		{key: headersKey, value: headerPrefix + token},
		{key: "OTEL_LOGS_EXPORT_INTERVAL", value: "5000"},
		{key: "OTEL_METRICS_EXPORTER", value: "none"},
	}
}

// ownsTelemetry reports whether the OpenTelemetry settings in env are
// Loadline's.
func ownsTelemetry(env gjson.Result) bool {
	headers := env.Get(headersKey)
	return headers.Type == gjson.String && strings.HasPrefix(headers.Str, headerPrefix)
}

// telemetryKeys lists the keys in env that configure Claude Code's telemetry.
// Windows treats environment variable names without regard to case, so
// neither does this.
func telemetryKeys(env gjson.Result) []string {
	var keys []string
	env.ForEach(func(key, _ gjson.Result) bool {
		name := strings.ToUpper(key.Str)
		if strings.HasPrefix(name, "OTEL_") || name == "CLAUDE_CODE_ENABLE_TELEMETRY" {
			keys = append(keys, key.Str)
		}
		return true
	})
	return keys
}

var tokenPattern = regexp.MustCompile(`x-loadline-token=[^",\s\\]*`)

// MaskToken replaces every ingest token in text with ****, so that settings
// and diffs can be shown without revealing it.
func MaskToken(text []byte) []byte {
	return tokenPattern.ReplaceAllLiteral(text, []byte(headerPrefix+maskedToken))
}
