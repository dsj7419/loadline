package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/protocol"
)

// hookInput is a PostToolUse payload whose content fields carry the canary,
// with a tool response of responseBytes.
func hookInput(t *testing.T, event string, responseBytes int) string {
	t.Helper()
	return marshal(t, map[string]any{
		"session_id":      "s1",
		"transcript_path": filepath.Join("home", canary+".jsonl"),
		"cwd":             filepath.Join("projects", "demo"),
		"permission_mode": "default",
		"hook_event_name": event,
		"source":          "startup",
		"tool_name":       "Write",
		"prompt":          canary,
		"tool_input":      map[string]any{"file_path": "notes.txt", "content": canary},
		"tool_response": map[string]any{
			"success": true,
			"output":  canary + strings.Repeat("x", responseBytes),
		},
		"last_assistant_message": canary,
	})
}

func (f *fixture) hook(input io.Reader) int {
	return cli.RunHook(f.t.Context(), input, f.env(), f.paths, f.cfg)
}

// hookFields are the payload fields Loadline keeps from a hook.
var hookFields = []string{
	"session_id", "hook_event_name", "source", "reason", "notification_type",
	"tool_name", "agent_id", "agent_type", "permission_mode", "cwd",
}

// assertHookAllowlisted checks that a forwarded or spooled hook carries the
// allowlisted fields of hookInput and nothing else.
func assertHookAllowlisted(t *testing.T, env protocol.Envelope, event string) {
	t.Helper()
	fields := payloadFields(t, env)
	for name := range fields {
		if !slices.Contains(hookFields, name) {
			t.Errorf("payload carries %q, which is not allowlisted", name)
		}
	}
	for name, want := range map[string]string{
		"session_id":      `"s1"`,
		"hook_event_name": `"` + event + `"`,
		"tool_name":       `"Write"`,
		"permission_mode": `"default"`,
	} {
		if got := string(fields[name]); got != want {
			t.Errorf("payload %s = %s, want %s", name, got, want)
		}
	}
	assertNoContent(t, env.Payload)
}

func TestRunHook_ForwardsTheAllowlistedFields(t *testing.T) {
	cli.SetHookSendTimeout(t, postWindow)
	f := newFixture(t)
	srv := f.serve()

	if code := f.hook(strings.NewReader(hookInput(t, "PostToolUse", 16))); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	posts := srv.received()
	if len(posts) != 1 || posts[0].path != "/ingest/hook" {
		t.Fatalf("server received %d posts, want one to /ingest/hook", len(posts))
	}
	assertNoContent(t, posts[0].raw)
	f.assertEnvelope(t, posts[0].envelope, protocol.KindHook)
	assertHookAllowlisted(t, posts[0].envelope, "PostToolUse")
	events := srv.sink.applied()
	if len(events) != 1 {
		t.Fatalf("server applied %d events, want 1", len(events))
	}
	hook, ok := events[0].(domain.Hook)
	if !ok || hook.Session != "s1" || hook.Name != "PostToolUse" || hook.PID != 4242 ||
		hook.Dir != domain.ConfigDir(f.wantConfigDir()) {
		t.Errorf("server applied %#v, want the PostToolUse of s1 from pid 4242 in %s", events[0], f.wantConfigDir())
	}
	if spooled := f.spooled(); len(spooled) != 0 {
		t.Errorf("spooled %d events after a delivery, want none", len(spooled))
	}
	if started := f.spawns.started(); len(started) != 0 {
		t.Errorf("started %v, want nothing", started)
	}
}

func TestRunHook_LargePayloadForwardsAllowlistOnly(t *testing.T) {
	cli.SetHookSendTimeout(t, postWindow)
	f := newFixture(t)
	srv := f.serve()
	input := hookInput(t, "PostToolUse", 5<<20)

	if code := f.hook(strings.NewReader(input)); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	posts := srv.received()
	if len(posts) != 1 {
		t.Fatalf("server received %d posts, want 1", len(posts))
	}
	if len(posts[0].raw) > 4<<10 {
		t.Errorf("forwarded %d bytes for a %d byte input, want a few hundred", len(posts[0].raw), len(input))
	}
	assertNoContent(t, posts[0].raw)
	assertHookAllowlisted(t, posts[0].envelope, "PostToolUse")
	if !bytes.Contains(posts[0].envelope.Payload, []byte(`"tool_name":"Write"`)) {
		t.Errorf("payload %s lost the tool name", posts[0].envelope.Payload)
	}
}

// Fifty megabytes on stdin is far beyond any real payload. The hook must
// still get through it, in memory that does not grow with the input.
func TestRunHook_FiftyMegabytesOfStdinStayWithinBoundedMemory(t *testing.T) {
	cli.SetHookSendTimeout(t, postWindow)
	f := newFixture(t)
	srv := f.serve()
	input := []byte(hookInput(t, "PostToolUse", 50<<20))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()

	code := f.hook(bytes.NewReader(input))

	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if posts := srv.received(); len(posts) != 1 {
		t.Errorf("server received %d posts, want 1", len(posts))
	} else {
		assertHookAllowlisted(t, posts[0].envelope, "PostToolUse")
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("50 MiB stdin: %v, %d KiB allocated", elapsed, allocated>>10)
	if allocated > 8<<20 {
		t.Errorf("allocated %d MiB for a %d MiB input, want under 8 MiB", allocated>>20, len(input)>>20)
	}
}

func TestRunHook_ServerDownSpoolsAndSpawnsOnSessionStart(t *testing.T) {
	f := newFixture(t)
	f.token()
	f.serverDown()
	start := time.Now()

	code := f.hook(strings.NewReader(hookInput(t, "SessionStart", 16)))

	elapsed := time.Since(start)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if started := f.spawns.started(); !slices.Equal(started, []string{testExe + " serve"}) {
		t.Errorf("started %v, want [%s serve]", started, testExe)
	}
	spooled := f.spooled()
	if len(spooled) != 1 {
		t.Fatalf("spooled %d events, want 1", len(spooled))
	}
	f.assertEnvelope(t, spooled[0], protocol.KindHook)
	assertHookAllowlisted(t, spooled[0], "SessionStart")
	// 150 ms to find the server gone, then 300 ms for the send.
	if bound := hotPathBound(600 * time.Millisecond); elapsed > bound {
		t.Errorf("hook took %v with the server down, want its 150 ms probe and 300 ms send at most, within %v", elapsed, bound)
	}
}

func TestRunHook_OtherEventsDoNotStartTheServer(t *testing.T) {
	f := newFixture(t)
	f.token()
	f.serverDown()

	if code := f.hook(strings.NewReader(hookInput(t, "PostToolUse", 16))); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	if started := f.spawns.started(); len(started) != 0 {
		t.Errorf("started %v for a PostToolUse, want nothing", started)
	}
	if spooled := f.spooled(); len(spooled) != 1 {
		t.Errorf("spooled %d events, want 1", len(spooled))
	}
}

// Against a server that takes connections and never answers, the hook
// gives up its check at its 150 ms and its post at its 300 ms, starts a
// server and spools the event.
func TestRunHook_GivesUpOnAHungServer(t *testing.T) {
	f := newFixture(t)
	f.serve(func(s *ingestServer) { s.slowHealth, s.hold = time.Hour, time.Hour })
	start := time.Now()

	code := f.hook(strings.NewReader(hookInput(t, "SessionStart", 16)))

	elapsed := time.Since(start)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if started := f.spawns.started(); !slices.Equal(started, []string{testExe + " serve"}) {
		t.Errorf("started %v, want [%s serve]", started, testExe)
	}
	if spooled := f.spooled(); len(spooled) != 1 {
		t.Errorf("spooled %d events, want 1", len(spooled))
	}
	if bound := hotPathBound(600 * time.Millisecond); elapsed > bound {
		t.Errorf("hook took %v against a hung server, want its 150 ms check and 300 ms send at most, within %v", elapsed, bound)
	}
}

// A running server is left alone. This one answers its health check later
// than the hook's own 150 ms would wait, so that the check is seen to take
// the Env's bound, which a loaded machine cannot outlast.
func TestRunHook_SessionStartLeavesARunningServerAlone(t *testing.T) {
	cli.SetHookSendTimeout(t, postWindow)
	f := newFixture(t)
	f.healthTimeout = probeWindow
	srv := f.serve(func(s *ingestServer) { s.slowHealth = 300 * time.Millisecond })

	f.hook(strings.NewReader(hookInput(t, "SessionStart", 16)))

	if started := f.spawns.started(); len(started) != 0 {
		t.Errorf("started %v with the server up, want nothing", started)
	}
	if posts := srv.received(); len(posts) != 1 {
		t.Errorf("server received %d posts, want 1", len(posts))
	}
}

// A spooled copy keeps the ID of the event the server may already have
// taken, so that the server recognizes it when the spool is drained.
func TestRunHook_RefusedEventIsSpooledUnderItsID(t *testing.T) {
	for _, tt := range []struct {
		name string
		opt  func(*ingestServer)
		// window bounds the hook's post: past the time a loaded machine
		// takes to deliver it, and short of the hold of a server that does
		// not answer, which the hook must give up on.
		window time.Duration
	}{
		{name: "an error answer", opt: func(s *ingestServer) { s.answer = http.StatusServiceUnavailable }, window: postWindow},
		{name: "no answer", opt: func(s *ingestServer) { s.hold = 2 * time.Second }, window: time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cli.SetHookSendTimeout(t, tt.window)
			f := newFixture(t)
			srv := f.serve(tt.opt)

			if code := f.hook(strings.NewReader(hookInput(t, "Stop", 16))); code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}

			posts, spooled := srv.received(), f.spooled()
			if len(posts) != 1 || len(spooled) != 1 {
				t.Fatalf("server received %d posts and spool holds %d events, want 1 and 1", len(posts), len(spooled))
			}
			if posts[0].envelope.ID == "" || spooled[0].ID != posts[0].envelope.ID {
				t.Errorf("spooled ID %q, want the ID %q the server was sent", spooled[0].ID, posts[0].envelope.ID)
			}
		})
	}
}

func TestRunHook_InvalidJSONExitsZero(t *testing.T) {
	inputs := map[string]string{
		"empty":      "",
		"not JSON":   "hook " + canary,
		"truncated":  `{"session_id":"s1","hook_event_name":"Stop","prompt":"` + canary,
		"null":       "null",
		"an array":   `["s1"]`,
		"a string":   `"s1"`,
		"no session": `{"hook_event_name":"Stop","prompt":"` + canary + `"}`,
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			srv := f.serve()

			if code := f.hook(strings.NewReader(input)); code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}

			if posts := srv.received(); len(posts) != 0 {
				t.Errorf("server received %d posts, want none", len(posts))
			}
			if spooled := f.spooled(); len(spooled) != 0 {
				t.Errorf("spooled %d events, want none", len(spooled))
			}
		})
	}
}

// A field too long to forward, over 4 KiB, is left out; the rest of the
// event still goes, and never makes an envelope the server, or a spool,
// would refuse.
func TestRunHook_DropsFieldsTooLargeToForward(t *testing.T) {
	cli.SetHookSendTimeout(t, postWindow)
	for name, tt := range map[string]struct {
		input   string
		dropped []string
	}{
		"a member too long to keep": {
			input:   `{"session_id":"s1","hook_event_name":"Stop","cwd":"` + strings.Repeat("d", 70<<10) + `","tool_name":"Bash"}`,
			dropped: []string{"cwd"},
		},
		"fields that would overflow the payload together": {
			input: `{"session_id":"s1","hook_event_name":"Stop","tool_name":"Bash","cwd":"` + strings.Repeat("d", 40<<10) +
				`","agent_type":"` + strings.Repeat("a", 40<<10) + `"}`,
			dropped: []string{"cwd", "agent_type"},
		},
		"a field just over 4 KiB": {
			input:   `{"session_id":"s1","hook_event_name":"Stop","tool_name":"Bash","cwd":"` + strings.Repeat("d", 4<<10+1) + `"}`,
			dropped: []string{"cwd"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			srv := f.serve()

			if code := f.hook(strings.NewReader(tt.input)); code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}

			posts := srv.received()
			if len(posts) != 1 || len(srv.sink.applied()) != 1 {
				t.Fatalf("server received %d posts and applied %d events, want 1 and 1", len(posts), len(srv.sink.applied()))
			}
			fields := payloadFields(t, posts[0].envelope)
			for _, name := range tt.dropped {
				if _, ok := fields[name]; ok {
					t.Errorf("payload carries the oversized %s", name)
				}
			}
			if string(fields["tool_name"]) != `"Bash"` || string(fields["session_id"]) != `"s1"` {
				t.Errorf("payload = %.300s, want the rest of the event", posts[0].envelope.Payload)
			}
		})
	}
}

func TestRunHook_KeepsAFieldOf4KiB(t *testing.T) {
	cli.SetHookSendTimeout(t, postWindow)
	f := newFixture(t)
	srv := f.serve()
	cwd := strings.Repeat("d", 4<<10)

	f.hook(strings.NewReader(`{"session_id":"s1","hook_event_name":"Stop","cwd":"` + cwd + `"}`))

	posts := srv.received()
	if len(posts) != 1 || string(payloadFields(t, posts[0].envelope)["cwd"]) != `"`+cwd+`"` {
		t.Errorf("server received %d posts, want one with the 4 KiB cwd", len(posts))
	}
}

// A token that cannot be read leaves the hook nothing to send with, so the
// event waits in the spool for a server that can read it.
func TestRunHook_UnreadableTokenSpools(t *testing.T) {
	f := newFixture(t)
	srv := f.serve()
	if err := os.WriteFile(f.paths.TokenFile, []byte("not a token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	if code := f.hook(strings.NewReader(hookInput(t, "Stop", 16))); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	if posts := srv.received(); len(posts) != 0 {
		t.Errorf("server received %d posts without a token, want none", len(posts))
	}
	if spooled := f.spooled(); len(spooled) != 1 {
		t.Errorf("spooled %d events, want 1", len(spooled))
	}
}

func TestRunHook_UnwritableSpoolStillExitsZero(t *testing.T) {
	f := newFixture(t)
	f.token()
	f.serverDown()
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	f.paths.SpoolDir = filepath.Join(blocker, "spool")

	if code := f.hook(strings.NewReader(hookInput(t, "Stop", 16))); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestRunHook_PIDOnlyWhenCLAUDE_PIDIsAPositiveNumber(t *testing.T) {
	cli.SetHookSendTimeout(t, postWindow)
	for value, want := range map[string]int{"4242": 4242, "": 0, "0": 0, "-7": 0, "12ab": 0, "99999999999999999999": 0} {
		t.Run("CLAUDE_PID="+value, func(t *testing.T) {
			f := newFixture(t)
			srv := f.serve()
			f.vars["CLAUDE_PID"] = value

			f.hook(strings.NewReader(hookInput(t, "Stop", 16)))

			posts := srv.received()
			if len(posts) != 1 || posts[0].envelope.PID != want {
				t.Errorf("posts %+v, want one with pid %d", posts, want)
			}
		})
	}
}

func TestRunHook_DefaultConfigDirIsUnderHome(t *testing.T) {
	cli.SetHookSendTimeout(t, postWindow)
	f := newFixture(t)
	srv := f.serve()
	delete(f.vars, "CLAUDE_CONFIG_DIR")

	f.hook(strings.NewReader(hookInput(t, "Stop", 16)))

	posts := srv.received()
	want := f.wantConfigDir()
	if len(posts) != 1 || posts[0].envelope.ConfigDir != want {
		t.Errorf("posts %+v, want one from %s", posts, want)
	}
	if !strings.HasSuffix(want, ".claude") {
		t.Errorf("default config dir %q, want <home>/.claude", want)
	}
}

// Every invocation through the command line writes nothing at all, on every
// path: Claude Code reads a hook's output.
func TestRunHook_NeverWritesStdout(t *testing.T) {
	cli.SetHookSendTimeout(t, postWindow)
	inputs := map[string]string{
		"an event":     hookInput(t, "PostToolUse", 1<<20),
		"invalid JSON": `{"session_id":`,
		"no input":     "",
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			home := t.TempDir()
			t.Setenv("LOADLINE_HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
			f.paths.TokenFile = filepath.Join(home, "token")
			srv := f.serve()
			writeConfig(t, filepath.Join(home, "config.json"), f.cfg.Port)
			var stdout, stderr bytes.Buffer

			code := cli.Run(context.Background(), []string{"hook", "--unexpected"}, cli.IO{
				In: strings.NewReader(input), Out: &stdout, Err: &stderr,
			})

			if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Errorf("hook = %d with stdout %q and stderr %q, want 0 and nothing written", code, stdout.String(), stderr.String())
			}
			if name == "an event" && len(srv.received()) != 1 {
				t.Errorf("server received %d posts, want 1", len(srv.received()))
			}
		})
	}
}

// Without a usable LOADLINE_HOME there is nowhere to read the token from or
// spool to, and still the hook must exit 0, silently.
func TestRunHook_ExitsZeroWithoutItsDirectories(t *testing.T) {
	t.Setenv("LOADLINE_HOME", filepath.Join("relative", "loadline"))
	var stdout, stderr bytes.Buffer

	code := cli.Run(context.Background(), []string{"hook"}, cli.IO{
		In: strings.NewReader(hookInput(t, "Stop", 0)), Out: &stdout, Err: &stderr,
	})

	if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("hook = %d with stdout %q and stderr %q, want 0 and nothing written", code, stdout.String(), stderr.String())
	}
}

func writeConfig(t *testing.T, path string, port int) {
	t.Helper()
	data, err := json.Marshal(map[string]int{"port": port})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
