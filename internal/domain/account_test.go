package domain_test

import (
	"testing"

	"github.com/dsj7419/loadline/internal/domain"
)

func TestAuthKindText(t *testing.T) {
	for kind, want := range map[domain.AuthKind]string{
		domain.AuthUnknown:      "unknown",
		domain.AuthSubscription: "subscription",
		domain.AuthAPIKey:       "api_key",
		domain.AuthThirdParty:   "third_party",
	} {
		if got := kind.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
		text, err := kind.MarshalText()
		if err != nil || string(text) != want {
			t.Errorf("MarshalText() = %q, %v; want %q", text, err, want)
		}
		parsed := domain.AuthKind(255)
		if err := parsed.UnmarshalText([]byte(want)); err != nil || parsed != kind {
			t.Errorf("UnmarshalText(%q) = %v, %v; want %v", want, parsed, err, kind)
		}
	}
}

func TestAuthKind_ZeroValueIsUnknown(t *testing.T) {
	var kind domain.AuthKind

	if kind != domain.AuthUnknown {
		t.Errorf("zero AuthKind = %v, want %v", kind, domain.AuthUnknown)
	}
}

func TestAuthKindText_RejectsUnknown(t *testing.T) {
	for _, text := range []string{"", "oauth", "API_KEY", "api-key"} {
		kind := domain.AuthSubscription
		if err := kind.UnmarshalText([]byte(text)); err == nil {
			t.Errorf("UnmarshalText(%q) accepted it as %v", text, kind)
		}
		if kind != domain.AuthSubscription {
			t.Errorf("failed UnmarshalText(%q) changed the kind to %v", text, kind)
		}
	}

	unknown := domain.AuthKind(99)
	if text, err := unknown.MarshalText(); err == nil {
		t.Errorf("MarshalText of kind 99 = %q, want an error", text)
	}
	if got := unknown.String(); got != "AuthKind(99)" {
		t.Errorf("String() of kind 99 = %q, want AuthKind(99)", got)
	}
}
