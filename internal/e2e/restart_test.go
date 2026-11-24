package e2e_test

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/clocktest"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/tracker"
)

// A server that stops and starts again shows what the first one saw: the
// session, its state changes and its model totals, and the account's rate
// limits.
func TestServe_RestoresAfterRestart(t *testing.T) {
	t.Setenv("LOADLINE_HOME", t.TempDir())
	paths, err := config.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	home := t.TempDir()
	dirA := config.ResolveConfigDir("", home)
	if err := os.MkdirAll(string(dirA), 0o700); err != nil {
		t.Fatalf("create %s: %v", dirA, err)
	}
	logs, err := os.ReadFile(otlpFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	clock := clocktest.New(fixtureCallAt.Add(time.Minute))
	fake := &claude{agents: make(map[domain.ConfigDir]string)}
	fake.list(dirA, `[{"sessionId":"`+string(s1)+`","kind":"interactive","pid":4242,"status":"busy",`+
		`"cwd":"/work/demo","startedAt":1795299700000}]`)
	options := func() cli.ServeOptions {
		return cli.ServeOptions{Paths: paths, Home: home, Port: freePort(t), NoOpen: true, Runner: fake, Clock: clock.Now}
	}

	first := serve(t, options())
	first.send(forward.HookPath, protocol.KindHook, dirA, clock.Now(),
		`{"session_id":"`+string(s1)+`","hook_event_name":"SessionStart","source":"startup","cwd":"/work/demo"}`)
	first.send(forward.StatusPath, protocol.KindStatus, dirA, clock.Now(),
		`{"session_id":"`+string(s1)+`","model":{"id":"claude-opus-5-5","display_name":"Opus 5.5"},`+
			`"context_window":{"used_percentage":31},"rate_limits":{"seven_day":{"used_percentage":42,"resets_at":1795400000}}}`)
	first.postLogs(logs)
	var before tracker.View
	eventually(t, "the model call to count toward s1", func() bool {
		before = first.snapshot()
		s, ok := session(before, s1)
		return ok && len(s.Models) > 0 && s.State == domain.StateWorking
	})
	// Until it stops, the server may still poll, which finds s1 busy as the
	// wait did, and probe the account, which sets who owns it: nothing that
	// is compared below changes.
	first.stop()

	// The server restores the history before it listens. Its own poll and
	// probe may come before this snapshot, and change nothing compared, as
	// above.
	second := serve(t, options())
	after := second.snapshot()

	was, _ := session(before, s1)
	is, ok := session(after, s1)
	if !ok {
		t.Fatalf("sessions %v after the restart, want s1 among them", after.Sessions)
	}
	if !reflect.DeepEqual(is.Models, was.Models) || !reflect.DeepEqual(after.Models, before.Models) {
		t.Errorf("model totals after the restart: s1 %+v of %+v; want s1 %+v of %+v as before",
			is.Models, after.Models, was.Models, before.Models)
	}
	if !reflect.DeepEqual(is.Timeline, was.Timeline) || is.Model != "Opus 5.5" || is.ContextPct != 31 {
		t.Errorf("s1 after the restart %+v, want its timeline %+v, model and context as before", is, was.Timeline)
	}
	if len(after.Accounts) != 1 || after.Accounts[0].SevenDay == nil || after.Accounts[0].SevenDay.UsedPct != 42 {
		t.Errorf("accounts after the restart %+v, want the week at 42%% as before", after.Accounts)
	}
}
