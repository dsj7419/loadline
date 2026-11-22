package claudecli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsj7419/loadline/internal/claudecli"
	"github.com/dsj7419/loadline/internal/domain"
)

// fuzzFixture reads a fixture for a fuzz target's seed corpus.
func fuzzFixture(f *testing.F, name string) []byte {
	f.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		f.Fatalf("read fixture: %v", err)
	}
	return data
}

// FuzzAgents reads whatever claude prints for `agents --json`. It gives the
// polls or an error, never both; every poll is keyed by a session, stamped
// with the dir and time it was asked for, of a kind Loadline knows, and
// storable, and there are never more than the 5000 entries Agents accepts.
func FuzzAgents(f *testing.F) {
	for _, seed := range [][]byte{
		fuzzFixture(f, "agents.json"),
		[]byte(`[]`), []byte(`null`), []byte(``), []byte(`{"sessionId":"s"}`),
		[]byte(`[1,"x",null,{"sessionId":"s","kind":"background","state":"done"}]`),
		[]byte(`[{"sessionId":"s","pid":"41001","startedAt":1e30,"kind":7,"cwd":["x"]}]`),
		[]byte(`[{"sessionId":"s","startedAt":253402300800000}]`),
		[]byte(`[{"sessionId":"s"}]  []`),
		[]byte(`[{"sessionId":"s"`),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, out []byte) {
		polls, err := claudecli.Client{R: &fakeRunner{out: out}}.Agents(context.Background(), testDir, pollTime)
		if err != nil {
			if polls != nil {
				t.Errorf("Agents gave %d polls with the error %v", len(polls), err)
			}
			return
		}
		if len(polls) > 5000 {
			t.Errorf("Agents gave %d polls, more than the 5000 entries it accepts", len(polls))
		}
		for _, p := range polls {
			if p.Session == "" || p.Dir != testDir || !p.At.Equal(pollTime) {
				t.Errorf("poll %+v, want a session in %s at %v", p, testDir, pollTime)
			}
			if p.Kind != domain.KindInteractive && p.Kind != domain.KindBackground {
				t.Errorf("poll %+v has the kind %v", p, p.Kind)
			}
			if _, err := json.Marshal(p.StartedAt); err != nil {
				t.Errorf("poll %+v has a start time that cannot be stored: %v", p, err)
			}
		}
	})
}

// FuzzAuthStatus reads whatever claude prints for `auth status`, and exits
// with. Only exit code 0, or 1 with a report, which is how claude says
// nobody is logged in, gives a status; the account that status describes is
// the dir it was asked about, probed when it was asked, signed in a way
// Loadline knows.
func FuzzAuthStatus(f *testing.F) {
	for _, seed := range [][]byte{
		fuzzFixture(f, "auth_status.json"),
		[]byte(`{"loggedIn":false,"authMethod":"none"}`),
		[]byte(`{"loggedIn":true,"authMethod":"api_key","apiProvider":"bedrock"}`),
		[]byte(`{"loggedIn":true,"authMethod":7,"email":["a"]}`),
		[]byte(`{"authMethod":"claude.ai"}`),
		[]byte(`{"loggedIn":"yes"}`),
		[]byte(`{"loggedIn":true,"planTier":"max__20x_","subscriptionType":"pro"}`),
		[]byte(`null`), []byte(``), []byte(`[]`),
	} {
		for _, exitCode := range []uint8{0, 1, 2} {
			f.Add(seed, exitCode)
		}
	}

	f.Fuzz(func(t *testing.T, out []byte, exitCode uint8) {
		var runErr error
		if exitCode != 0 {
			runErr = fmt.Errorf("run claude: %w", fakeExitError{code: int(exitCode)})
		}
		client := claudecli.Client{R: &fakeRunner{out: out, err: runErr}}

		status, err := client.AuthStatus(context.Background(), testDir)
		if err != nil {
			return
		}
		if exitCode > 1 {
			t.Errorf("AuthStatus took the output of a claude that exited %d: %+v", exitCode, status)
		}
		again, err := client.AuthStatus(context.Background(), testDir)
		if err != nil || again != status {
			t.Errorf("AuthStatus read the same output as %+v, then %+v (%v)", status, again, err)
		}
		account := status.Account(testDir, "Work", pollTime)
		if account.Dir != testDir || account.Label != "Work" || !account.ProbedAt.Equal(pollTime) {
			t.Errorf("account %+v, want %s labeled Work, probed at %v", account, testDir, pollTime)
		}
		switch account.Auth {
		case domain.AuthUnknown, domain.AuthSubscription, domain.AuthAPIKey, domain.AuthThirdParty:
		default:
			t.Errorf("account %+v signs in as %v", account, account.Auth)
		}
	})
}
