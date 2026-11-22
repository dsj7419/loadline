package claudecli_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/claudecli"
	"github.com/dsj7419/loadline/internal/domain"
)

const testDir domain.ConfigDir = `C:\Users\example\.claude-work`

var pollTime = time.Date(2026, time.November, 21, 12, 0, 0, 0, time.UTC)

// fakeRunner is a claudecli.Runner that returns canned output and records the
// call it received.
type fakeRunner struct {
	out []byte
	err error

	gotDir  domain.ConfigDir
	gotArgs []string
}

func (f *fakeRunner) Run(_ context.Context, dir domain.ConfigDir, args ...string) ([]byte, error) {
	f.gotDir = dir
	f.gotArgs = args
	return f.out, f.err
}

// fakeExitError is an error that carries a process exit code, as *exec.ExitError
// does.
type fakeExitError struct{ code int }

func (e fakeExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e fakeExitError) ExitCode() int { return e.code }

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func agents(t *testing.T, out string) []domain.Poll {
	t.Helper()

	polls, err := claudecli.Client{R: &fakeRunner{out: []byte(out)}}.Agents(context.Background(), testDir, pollTime)
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	return polls
}

func TestAgents_MapsFixture(t *testing.T) {
	runner := &fakeRunner{out: fixture(t, "agents.json")}

	polls, err := claudecli.Client{R: runner}.Agents(context.Background(), testDir, pollTime)
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}

	if runner.gotDir != testDir {
		t.Errorf("ran in dir %q, want %q", runner.gotDir, testDir)
	}
	if want := []string{"agents", "--json", "--all"}; !slices.Equal(runner.gotArgs, want) {
		t.Errorf("args = %q, want %q", runner.gotArgs, want)
	}

	demo := `C:\Users\example\projects\demo`
	want := []domain.Poll{
		{
			At: pollTime, Dir: testDir, Session: "aaaaaaaa-0000-4000-8000-000000000001",
			Kind: domain.KindInteractive, PID: 41001, Status: "busy",
			Name: "api-refactor", Cwd: demo, StartedAt: time.UnixMilli(1795293100000),
		},
		{
			At: pollTime, Dir: testDir, Session: "aaaaaaaa-0000-4000-8000-000000000002",
			Kind: domain.KindInteractive, PID: 41002, Status: "waiting", WaitingFor: "permission prompt",
			Name: "release-notes", Cwd: demo, StartedAt: time.UnixMilli(1795293150000),
		},
		{
			At: pollTime, Dir: testDir, Session: "aaaaaaaa-0000-4000-8000-000000000003",
			Kind: domain.KindInteractive, PID: 41003, Status: "idle",
			Cwd: `C:\Users\example\projects\widgets`, StartedAt: time.UnixMilli(1795293200000),
		},
		{
			At: pollTime, Dir: testDir, Session: "bbbbbbbb-0000-4000-8000-000000000001",
			Kind: domain.KindBackground, BgState: "stopped",
			Name: "nightly-build", Cwd: demo, StartedAt: time.UnixMilli(1795293250123),
		},
		{
			At: pollTime, Dir: testDir, Session: "bbbbbbbb-0000-4000-8000-000000000002",
			Kind: domain.KindBackground, PID: 41004, Status: "busy", BgState: "working",
			Name: "docs-scan", Cwd: demo, StartedAt: time.UnixMilli(1795293900456),
		},
	}
	if len(polls) != len(want) {
		t.Fatalf("got %d polls, want %d: %+v", len(polls), len(want), polls)
	}
	for i := range want {
		if !samePoll(polls[i], want[i]) {
			t.Errorf("poll %d = %+v, want %+v", i, polls[i], want[i])
		}
	}
}

// samePoll compares polls with Equal for their times, since time.Time may
// differ in representation while naming the same instant.
func samePoll(a, b domain.Poll) bool {
	if !a.At.Equal(b.At) || !a.StartedAt.Equal(b.StartedAt) {
		return false
	}
	a.At, a.StartedAt, b.At, b.StartedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

func TestAgents_EmptyArray(t *testing.T) {
	for _, out := range []string{"[]", "[]\n", " [ ] ", "null"} {
		if polls := agents(t, out); len(polls) != 0 {
			t.Errorf("Agents(%q) = %+v, want no polls", out, polls)
		}
	}
}

func TestAgents_SkipsEntriesWithoutSessionID(t *testing.T) {
	polls := agents(t, `[
		{"pid":1,"kind":"interactive","status":"busy"},
		{"pid":2,"kind":"interactive","sessionId":"","status":"busy"},
		{"pid":3,"kind":"interactive","sessionId":"keep","status":"idle"}
	]`)

	if len(polls) != 1 || polls[0].Session != "keep" || polls[0].PID != 3 {
		t.Errorf("polls = %+v, want only the entry with session %q", polls, "keep")
	}
}

func TestAgents_SkipsElementsThatAreNotObjects(t *testing.T) {
	polls := agents(t, `["oops", 7, null, [], {"sessionId":"keep"}]`)

	if len(polls) != 1 || polls[0].Session != "keep" {
		t.Errorf("polls = %+v, want only the object entry", polls)
	}
}

func TestAgents_UnknownKindIsInteractive(t *testing.T) {
	polls := agents(t, `[
		{"sessionId":"a","kind":"remote"},
		{"sessionId":"b"},
		{"sessionId":"c","kind":"background"}
	]`)

	kinds := make([]domain.Kind, len(polls))
	for i, p := range polls {
		kinds[i] = p.Kind
	}
	want := []domain.Kind{domain.KindInteractive, domain.KindInteractive, domain.KindBackground}
	if !slices.Equal(kinds, want) {
		t.Errorf("kinds = %v, want %v", kinds, want)
	}
}

func TestAgents_IgnoresUnknownFields(t *testing.T) {
	polls := agents(t, `[{"sessionId":"a","status":"busy","tokens":{"in":1},"future":[1,2],"pid":9}]`)

	if len(polls) != 1 || polls[0].Status != "busy" || polls[0].PID != 9 {
		t.Errorf("polls = %+v, want the known fields decoded", polls)
	}
}

func TestAgents_MistypedFieldKeepsTheEntry(t *testing.T) {
	polls := agents(t, `[
		{"sessionId":"a","pid":"41001","startedAt":"soon","status":"busy","name":"kept"},
		{"sessionId":"b","status":["busy"],"kind":3,"cwd":{}}
	]`)

	if len(polls) != 2 {
		t.Fatalf("got %d polls, want 2: %+v", len(polls), polls)
	}
	if a := polls[0]; a.PID != 0 || !a.StartedAt.IsZero() || a.Status != "busy" || a.Name != "kept" {
		t.Errorf("poll a = %+v, want mistyped pid and startedAt dropped and the rest kept", a)
	}
	if b := polls[1]; b.Status != "" || b.Kind != domain.KindInteractive || b.Cwd != "" {
		t.Errorf("poll b = %+v, want mistyped fields left empty", b)
	}
}

func TestAgents_StartedAtOutsideRangeReadsAsAbsent(t *testing.T) {
	// 253402300800000 ms is year 10000, which a time cannot marshal.
	polls := agents(t, `[
		{"sessionId":"zero","startedAt":0},
		{"sessionId":"negative","startedAt":-5},
		{"sessionId":"huge","startedAt":9223372036854775807},
		{"sessionId":"year10000","startedAt":253402300800000},
		{"sessionId":"lastMillisecond","startedAt":253402300799999}
	]`)

	if len(polls) != 5 {
		t.Fatalf("got %d polls, want 5", len(polls))
	}
	for _, p := range polls[:4] {
		if !p.StartedAt.IsZero() {
			t.Errorf("%s: StartedAt = %v, want zero", p.Session, p.StartedAt)
		}
	}
	if got, want := polls[4].StartedAt, time.UnixMilli(253402300799999); !got.Equal(want) {
		t.Errorf("lastMillisecond: StartedAt = %v, want %v", got, want)
	}
}

func TestAgents_Errors(t *testing.T) {
	boom := errors.New("claude: not found")
	tests := []struct {
		name    string
		runner  *fakeRunner
		wantErr error
	}{
		{"runner fails", &fakeRunner{err: boom}, boom},
		{"runner fails with output", &fakeRunner{out: []byte("[]"), err: fakeExitError{code: 1}}, nil},
		{"empty output", &fakeRunner{}, nil},
		{"truncated JSON", &fakeRunner{out: []byte(`[{"sessionId":"a"`)}, nil},
		{"not JSON", &fakeRunner{out: []byte("update available")}, nil},
		{"object instead of array", &fakeRunner{out: []byte(`{"sessionId":"a"}`)}, nil},
		{"scalar instead of array", &fakeRunner{out: []byte(`7`)}, nil},
		{"data after the array", &fakeRunner{out: []byte(`[{"sessionId":"a"}] {"sessionId":"b"}`)}, nil},
		{"unterminated array", &fakeRunner{out: []byte(`[{"sessionId":"a"},`)}, nil},
		{"missing closing bracket", &fakeRunner{out: []byte(`[{"sessionId":"a"}`)}, nil},
		{"opening bracket only", &fakeRunner{out: []byte(`[`)}, nil},
		{"syntax error inside an entry", &fakeRunner{out: []byte(`[{"sessionId":}]`)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			polls, err := claudecli.Client{R: tt.runner}.Agents(context.Background(), testDir, pollTime)

			if err == nil {
				t.Fatalf("Agents = %+v, want an error", polls)
			}
			if polls != nil {
				t.Errorf("polls = %+v, want nil alongside an error", polls)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want it to wrap %v", err, tt.wantErr)
			}
		})
	}
}

// maxAgentEntries is the most entries Agents accepts: far more than any
// machine runs, so that the output cap of four MiB cannot multiply into
// memory.
const maxAgentEntries = 5000

func TestAgents_EntryCap(t *testing.T) {
	list := func(n int) string {
		entries := make([]string, n)
		for i := range entries {
			entries[i] = fmt.Sprintf(`{"sessionId":"s%d"}`, i)
		}
		return "[" + strings.Join(entries, ",") + "]"
	}

	if polls := agents(t, list(maxAgentEntries)); len(polls) != maxAgentEntries {
		t.Errorf("got %d polls for %d entries, want all of them", len(polls), maxAgentEntries)
	}

	polls, err := claudecli.Client{R: &fakeRunner{out: []byte(list(maxAgentEntries + 1))}}.Agents(context.Background(), testDir, pollTime)
	if err == nil || polls != nil {
		t.Errorf("Agents = %d polls, %v, want an error for %d entries", len(polls), err, maxAgentEntries+1)
	}
}

func TestAgents_CapCountsEntriesItSkips(t *testing.T) {
	out := "[" + strings.Repeat("0,", maxAgentEntries) + "0]"

	if polls, err := (claudecli.Client{R: &fakeRunner{out: []byte(out)}}).Agents(context.Background(), testDir, pollTime); err == nil {
		t.Errorf("Agents = %+v, want an error for %d elements that are not sessions", polls, maxAgentEntries+1)
	}
}

func TestAgents_OutputAtTheCapDoesNotMultiplyIntoMemory(t *testing.T) {
	const outputCap = 4 << 20
	const memoryCeiling = 64 << 20

	for name, element := range map[string]string{"numbers": "0", "empty objects": "{}", "tiny sessions": `{"sessionId":"s"}`} {
		t.Run(name, func(t *testing.T) {
			out := []byte("[" + strings.Repeat(element+",", outputCap/(len(element)+1)) + element + "]")
			runner := &fakeRunner{out: out}

			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, err := claudecli.Client{R: runner}.Agents(context.Background(), testDir, pollTime)
			runtime.ReadMemStats(&after)

			if err == nil {
				t.Error("err = nil, want the entry cap to refuse the output")
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > memoryCeiling {
				t.Errorf("Agents allocated %d MiB for %d bytes of output, want under %d MiB", allocated>>20, len(out), memoryCeiling>>20)
			}
		})
	}
}

func authStatus(t *testing.T, out string, runErr error) (claudecli.AuthStatus, error) {
	t.Helper()

	runner := &fakeRunner{out: []byte(out), err: runErr}
	status, err := claudecli.Client{R: runner}.AuthStatus(context.Background(), testDir)

	if runner.gotDir != testDir {
		t.Errorf("ran in dir %q, want %q", runner.gotDir, testDir)
	}
	if want := []string{"auth", "status", "--json"}; !slices.Equal(runner.gotArgs, want) {
		t.Errorf("args = %q, want %q", runner.gotArgs, want)
	}
	return status, err
}

func TestAuthStatus_MapsFixture(t *testing.T) {
	status, err := authStatus(t, string(fixture(t, "auth_status.json")), nil)
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}

	want := claudecli.AuthStatus{
		LoggedIn:         true,
		AuthMethod:       "claude.ai",
		APIProvider:      "firstParty",
		Email:            "dev@example.com",
		OrgName:          "Example Org",
		SubscriptionType: "max",
		PlanTier:         "max_20x",
	}
	if status != want {
		t.Errorf("status = %+v, want %+v", status, want)
	}
}

func TestAuthStatus_LoggedOutExitOne(t *testing.T) {
	status, err := authStatus(t, `{"loggedIn":false,"authMethod":"none"}`, fmt.Errorf("run claude: %w", fakeExitError{code: 1}))
	if err != nil {
		t.Fatalf("AuthStatus: %v, want logged out to be a result and not an error", err)
	}

	if status.LoggedIn || status.AuthMethod != "none" {
		t.Errorf("status = %+v, want logged out with authMethod none", status)
	}
}

func TestAuthStatus_ExitOneWithoutJSONIsAnError(t *testing.T) {
	_, err := authStatus(t, "", fmt.Errorf("run claude: %w", fakeExitError{code: 1}))

	var exit fakeExitError
	if !errors.As(err, &exit) || exit.code != 1 {
		t.Errorf("err = %v, want the exit error", err)
	}
}

func TestAuthStatus_OtherExitCodesAreErrors(t *testing.T) {
	for _, code := range []int{2, 127, -1} {
		_, err := authStatus(t, `{"loggedIn":false}`, fakeExitError{code: code})
		if err == nil {
			t.Errorf("exit code %d with JSON: err = nil, want an error", code)
		}
	}
}

func TestAuthStatus_RunFailureIsAnError(t *testing.T) {
	_, err := authStatus(t, "", context.DeadlineExceeded)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wrap the run error", err)
	}
}

func TestAuthStatus_InvalidJSONIsAnError(t *testing.T) {
	for _, out := range []string{"", "Not logged in", `{"loggedIn":`, `[]`, `"loggedIn"`} {
		if status, err := authStatus(t, out, nil); err == nil {
			t.Errorf("output %q: status = %+v, want an error", out, status)
		}
	}
}

func TestAuthStatus_MistypedOptionalFieldsReadAsAbsent(t *testing.T) {
	status, err := authStatus(t, `{"loggedIn":true,"authMethod":"claude.ai","email":["a"],"orgName":7,"planTier":{},"subscriptionType":"pro","configDirectory":["x"],"apiProvider":false}`, nil)
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}

	want := claudecli.AuthStatus{LoggedIn: true, AuthMethod: "claude.ai", SubscriptionType: "pro"}
	if status != want {
		t.Errorf("status = %+v, want %+v", status, want)
	}
}

func TestAuthStatus_ReportWithoutLoggedInIsAnError(t *testing.T) {
	// An error object, or a report from a future version that renames the
	// field, must not read as "logged out": that would hide a signed-in
	// account.
	for _, out := range []string{`{}`, `{"error":"unauthorized"}`, `{"loggedIn":null}`, `{"authMethod":"claude.ai","email":"dev@example.com"}`} {
		if status, err := authStatus(t, out, nil); err == nil {
			t.Errorf("exit 0, output %s: status = %+v, want an error", out, status)
		}

		_, err := authStatus(t, out, fakeExitError{code: 1})
		var exit fakeExitError
		if !errors.As(err, &exit) {
			t.Errorf("exit 1, output %s: err = %v, want the exit error", out, err)
		}
	}
}

func TestAuthStatus_MistypedDocumentedFieldsAreErrors(t *testing.T) {
	// Reading these as absent would turn a signed-in account into a signed-out
	// one, or lose its sign-in method.
	for _, out := range []string{
		`{"loggedIn":"yes"}`,
		`{"loggedIn":true,"authMethod":3}`,
	} {
		if status, err := authStatus(t, out, nil); err == nil {
			t.Errorf("output %s: status = %+v, want an error", out, status)
		}
	}
}

func TestAuthStatus_Account(t *testing.T) {
	tests := []struct {
		name     string
		status   claudecli.AuthStatus
		wantAuth domain.AuthKind
		wantPlan string
	}{
		{"claude.ai max", claudecli.AuthStatus{AuthMethod: "claude.ai", APIProvider: "firstParty", SubscriptionType: "max", PlanTier: "max_20x"}, domain.AuthSubscription, "Max 20x"},
		{"claude.ai pro", claudecli.AuthStatus{AuthMethod: "claude.ai", PlanTier: "pro"}, domain.AuthSubscription, "Pro"},
		{"oauth token", claudecli.AuthStatus{AuthMethod: "oauth_token", APIProvider: "firstParty"}, domain.AuthSubscription, ""},
		{"api key", claudecli.AuthStatus{AuthMethod: "api_key", APIProvider: "firstParty"}, domain.AuthAPIKey, ""},
		{"api key helper", claudecli.AuthStatus{AuthMethod: "api_key_helper"}, domain.AuthAPIKey, ""},
		{"third party method", claudecli.AuthStatus{AuthMethod: "third_party"}, domain.AuthThirdParty, ""},
		{"bedrock provider", claudecli.AuthStatus{AuthMethod: "third_party", APIProvider: "bedrock"}, domain.AuthThirdParty, ""},
		{"vertex provider wins over method", claudecli.AuthStatus{AuthMethod: "api_key", APIProvider: "vertex"}, domain.AuthThirdParty, ""},
		{"foundry provider wins over method", claudecli.AuthStatus{AuthMethod: "claude.ai", APIProvider: "foundry"}, domain.AuthThirdParty, ""},
		{"none", claudecli.AuthStatus{AuthMethod: "none"}, domain.AuthUnknown, ""},
		{"unknown method", claudecli.AuthStatus{AuthMethod: "passkey"}, domain.AuthUnknown, ""},
		{"nothing reported", claudecli.AuthStatus{}, domain.AuthUnknown, ""},
		{"plan falls back to subscription type", claudecli.AuthStatus{AuthMethod: "claude.ai", SubscriptionType: "team"}, domain.AuthSubscription, "Team"},
		{"plan tier wins over subscription type", claudecli.AuthStatus{AuthMethod: "claude.ai", SubscriptionType: "max", PlanTier: "max_5x"}, domain.AuthSubscription, "Max 5x"},
		{"underscored subscription type", claudecli.AuthStatus{AuthMethod: "claude.ai", SubscriptionType: "pro_plus"}, domain.AuthSubscription, "Pro Plus"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := tt.status.Account(testDir, "Work", pollTime)

			if account.Auth != tt.wantAuth {
				t.Errorf("Auth = %v, want %v", account.Auth, tt.wantAuth)
			}
			if account.Plan != tt.wantPlan {
				t.Errorf("Plan = %q, want %q", account.Plan, tt.wantPlan)
			}
		})
	}
}

func TestAuthStatus_AccountCarriesIdentity(t *testing.T) {
	status := claudecli.AuthStatus{
		LoggedIn: true, AuthMethod: "claude.ai", Email: "dev@example.com", OrgName: "Example Org", PlanTier: "max_20x",
	}

	got := status.Account(testDir, "Work", pollTime)

	want := domain.Account{
		Dir: testDir, Label: "Work", Email: "dev@example.com", Plan: "Max 20x", Org: "Example Org",
		Auth: domain.AuthSubscription, ProbedAt: pollTime,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Account = %+v, want %+v", got, want)
	}
}
