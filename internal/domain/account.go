package domain

import (
	"fmt"
	"time"
)

// AuthKind is how an account signs in to Claude Code.
type AuthKind uint8

const (
	// AuthUnknown is an account whose sign-in has not been determined.
	AuthUnknown AuthKind = iota
	// AuthSubscription is a Claude subscription (Pro, Max, Team or Enterprise).
	AuthSubscription
	// AuthAPIKey is an API key, billed per token.
	AuthAPIKey
	// AuthThirdParty is a cloud provider such as Bedrock or Vertex.
	AuthThirdParty
)

var authKindNames = [...]string{
	AuthUnknown:      "unknown",
	AuthSubscription: "subscription",
	AuthAPIKey:       "api_key",
	AuthThirdParty:   "third_party",
}

// String returns the kind's text form, or AuthKind(n) for a value outside the
// defined kinds.
func (k AuthKind) String() string {
	if int(k) < len(authKindNames) {
		return authKindNames[k]
	}
	return fmt.Sprintf("AuthKind(%d)", k)
}

// MarshalText implements encoding.TextMarshaler. It refuses a value outside
// the defined kinds, which could not be read back.
func (k AuthKind) MarshalText() ([]byte, error) {
	if int(k) >= len(authKindNames) {
		return nil, fmt.Errorf("unknown auth kind %d", k)
	}
	return []byte(authKindNames[k]), nil
}

// UnmarshalText implements encoding.TextUnmarshaler. Unknown text is an
// error and leaves k unchanged.
func (k *AuthKind) UnmarshalText(text []byte) error {
	for kind := AuthKind(0); int(kind) < len(authKindNames); kind++ {
		if authKindNames[kind] == string(text) {
			*k = kind
			return nil
		}
	}
	return fmt.Errorf("unknown auth kind %q", text)
}

// Account is one Claude Code configuration directory and who it belongs to.
type Account struct {
	Dir                     ConfigDir
	Label, Email, Plan, Org string
	Auth                    AuthKind
	Limits                  Limits
	ProbedAt                time.Time
}

// APIRequest is one model call, as an OpenTelemetry api_request log event
// reports it. It carries token counts, cost and duration, never content.
type APIRequest struct {
	// At is when the call ended, which is when Claude Code logs it.
	At      time.Time
	Session SessionID
	Email   string
	Model   string

	Input, Output, CacheRead, CacheWrite int64
	CostUSD                              float64
	// DurationMS is how long the call took, zero when unknown, so the call
	// began DurationMS before At.
	DurationMS int64
}
