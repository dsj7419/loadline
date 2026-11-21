// Package claudecli runs the claude command-line interface and maps what it
// prints to domain types. It uses only the two commands Claude Code documents
// for reading state from outside: `claude agents --json`, the source of truth
// for sessions, and `claude auth status`, which says who an account is.
package claudecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dsj7419/loadline/internal/domain"
)

// Runner runs claude for the account whose configuration directory is dir and
// returns its stdout. A Runner that fails because claude exited with a
// non-zero code returns the output alongside an error that carries the code;
// see exitCoder.
type Runner interface {
	Run(ctx context.Context, dir domain.ConfigDir, args ...string) ([]byte, error)
}

// exitCoder is what Client needs of the error a failed child process produces,
// such as *exec.ExitError. Asking for the method alone lets a fake Runner
// report an exit code without running a process.
type exitCoder interface{ ExitCode() int }

// loggedOutExitCode is the code `claude auth status` exits with when nobody is
// logged in. It still prints its report.
const loggedOutExitCode = 1

// Client reads session and account state from claude.
type Client struct{ R Runner }

// Agents lists the sessions of the account in dir, as `claude agents --json
// --all` reports them, with every poll stamped at. An entry without a
// sessionId has nothing to key on and is skipped.
//
// Claude Code's output evolves, so a field of an unexpected type reads as
// absent instead of failing the poll, and unknown fields and kinds are
// ignored. Output that is not a JSON array is an error.
func (c Client) Agents(ctx context.Context, dir domain.ConfigDir, at time.Time) ([]domain.Poll, error) {
	out, err := c.R.Run(ctx, dir, "agents", "--json", "--all")
	if err != nil {
		return nil, fmt.Errorf("list sessions of %s: %w", dir, err)
	}

	var entries []json.RawMessage
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("decode sessions of %s: %w", dir, err)
	}

	polls := make([]domain.Poll, 0, len(entries))
	for _, raw := range entries {
		var entry agentEntry
		if err := json.Unmarshal(raw, &entry); err != nil || entry.SessionID.V == "" {
			continue
		}
		polls = append(polls, entry.poll(dir, at))
	}
	return polls, nil
}

// lenient decodes a JSON value of type T, and reads a value of any other type
// as T's zero value.
type lenient[T any] struct{ V T }

func (l *lenient[T]) UnmarshalJSON(data []byte) error {
	var v T
	if err := json.Unmarshal(data, &v); err == nil {
		l.V = v
	}
	return nil
}

// agentEntry mirrors the fields of one `claude agents --json` entry that
// Loadline reads.
type agentEntry struct {
	SessionID  lenient[string] `json:"sessionId"`
	Kind       lenient[string] `json:"kind"`
	PID        lenient[int]    `json:"pid"`
	Status     lenient[string] `json:"status"`
	WaitingFor lenient[string] `json:"waitingFor"`
	State      lenient[string] `json:"state"`
	Name       lenient[string] `json:"name"`
	Cwd        lenient[string] `json:"cwd"`
	StartedAt  lenient[int64]  `json:"startedAt"`
}

func (e agentEntry) poll(dir domain.ConfigDir, at time.Time) domain.Poll {
	kind := domain.KindInteractive
	if e.Kind.V == domain.KindBackground.String() {
		kind = domain.KindBackground
	}
	return domain.Poll{
		At:         at,
		Dir:        dir,
		Session:    domain.SessionID(e.SessionID.V),
		Kind:       kind,
		PID:        e.PID.V,
		Status:     e.Status.V,
		WaitingFor: e.WaitingFor.V,
		BgState:    e.State.V,
		Name:       e.Name.V,
		Cwd:        e.Cwd.V,
		StartedAt:  startTime(e.StartedAt.V),
	}
}

// startTime converts Unix milliseconds to a UTC time. Zero, negative values and
// those past year 9999, which a time cannot marshal, read as absent.
func startTime(unixMilli int64) time.Time {
	if unixMilli <= 0 {
		return time.Time{}
	}
	t := time.UnixMilli(unixMilli).UTC()
	if t.Year() > 9999 {
		return time.Time{}
	}
	return t
}

// AuthStatus is the part of `claude auth status` that Loadline reads. Claude
// Code documents LoggedIn, AuthMethod and ConfigDirectory; the rest are
// optional and may be empty.
type AuthStatus struct {
	LoggedIn bool
	// AuthMethod is none, claude.ai, oauth_token, api_key, api_key_helper or
	// third_party.
	AuthMethod string
	// APIProvider is firstParty for Anthropic's own API, or a cloud provider
	// such as bedrock or vertex.
	APIProvider string

	Email, OrgName, SubscriptionType, PlanTier, ConfigDirectory string
}

// authStatusReport mirrors the JSON of `claude auth status`. The documented
// fields are strict, since reading one as absent could turn a signed-in
// account into a signed-out one. The others are lenient.
type authStatusReport struct {
	LoggedIn         bool            `json:"loggedIn"`
	AuthMethod       string          `json:"authMethod"`
	ConfigDirectory  string          `json:"configDirectory"`
	APIProvider      lenient[string] `json:"apiProvider"`
	Email            lenient[string] `json:"email"`
	OrgName          lenient[string] `json:"orgName"`
	SubscriptionType lenient[string] `json:"subscriptionType"`
	PlanTier         lenient[string] `json:"planTier"`
}

// AuthStatus asks claude who the account in dir is signed in as. Exit code 1
// with a readable report is how claude says nobody is logged in; it is a
// result, with LoggedIn false, and not an error. Any other failure, or output
// that is not a JSON object, is an error.
func (c Client) AuthStatus(ctx context.Context, dir domain.ConfigDir) (AuthStatus, error) {
	out, runErr := c.R.Run(ctx, dir, "auth", "status", "--json")
	if runErr != nil && !exitedLoggedOut(runErr) {
		return AuthStatus{}, fmt.Errorf("read auth status of %s: %w", dir, runErr)
	}

	var report authStatusReport
	if err := json.Unmarshal(out, &report); err != nil {
		if runErr != nil {
			// Exit code 1 without a report is a failure, not a logout.
			return AuthStatus{}, fmt.Errorf("read auth status of %s: %w", dir, runErr)
		}
		return AuthStatus{}, fmt.Errorf("decode auth status of %s: %w", dir, err)
	}
	return AuthStatus{
		LoggedIn:         report.LoggedIn,
		AuthMethod:       report.AuthMethod,
		APIProvider:      report.APIProvider.V,
		Email:            report.Email.V,
		OrgName:          report.OrgName.V,
		SubscriptionType: report.SubscriptionType.V,
		PlanTier:         report.PlanTier.V,
		ConfigDirectory:  report.ConfigDirectory,
	}, nil
}

func exitedLoggedOut(err error) bool {
	var exit exitCoder
	return errors.As(err, &exit) && exit.ExitCode() == loggedOutExitCode
}

// providerFirstParty is the apiProvider of Anthropic's own API.
const providerFirstParty = "firstParty"

// Account describes the account in dir as of at, under the display name
// label. Limits stay empty: status lines report them.
func (s AuthStatus) Account(dir domain.ConfigDir, label string, at time.Time) domain.Account {
	return domain.Account{
		Dir:      dir,
		Label:    label,
		Email:    s.Email,
		Plan:     s.plan(),
		Org:      s.OrgName,
		Auth:     s.authKind(),
		ProbedAt: at,
	}
}

// authKind maps how the account signs in. A provider other than Anthropic's
// own decides first, since a cloud provider can be reached with an API key
// too, but is billed by that provider.
func (s AuthStatus) authKind() domain.AuthKind {
	if s.APIProvider != "" && s.APIProvider != providerFirstParty {
		return domain.AuthThirdParty
	}
	switch s.AuthMethod {
	case "third_party":
		return domain.AuthThirdParty
	case "claude.ai", "oauth_token":
		return domain.AuthSubscription
	case "api_key", "api_key_helper":
		return domain.AuthAPIKey
	default:
		return domain.AuthUnknown
	}
}

// plan is the plan tier, or the subscription type when there is no tier, with
// underscores read as word breaks and each word capitalized: max_20x is Max
// 20x.
func (s AuthStatus) plan() string {
	raw := s.PlanTier
	if strings.TrimSpace(raw) == "" {
		raw = s.SubscriptionType
	}
	words := strings.FieldsFunc(raw, func(r rune) bool { return r == '_' || unicode.IsSpace(r) })
	for i, word := range words {
		first, size := utf8.DecodeRuneInString(word)
		words[i] = string(unicode.ToUpper(first)) + word[size:]
	}
	return strings.Join(words, " ")
}
