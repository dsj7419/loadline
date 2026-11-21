package protocol_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/protocol"
)

const (
	demoSession = "00000000-0000-4000-8000-000000000001"
	demoCwd     = `C:\Users\example\projects\demo`
	leakMarker  = "CONTENT-LEAK"
)

var allowlistedHookKeys = []string{
	"session_id", "hook_event_name", "source", "reason", "notification_type",
	"tool_name", "agent_id", "agent_type", "permission_mode", "cwd",
}

func fixture(tb testing.TB, name string) []byte {
	tb.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		tb.Fatalf("read fixture: %v", err)
	}
	return data
}

func readHook(t *testing.T, data []byte) protocol.HookInput {
	t.Helper()

	in, err := protocol.ReadHookInput(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadHookInput: %v", err)
	}
	return in
}

func marshal(t *testing.T, v any) string {
	t.Helper()

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(data)
}

func TestReadHookInput_DropsContent(t *testing.T) {
	data := fixture(t, "hook_posttooluse.json")
	for _, key := range []string{"tool_input", "tool_response"} {
		if !strings.Contains(string(data), `"`+key+`"`) {
			t.Fatalf("fixture has no %s; the test would prove nothing", key)
		}
	}

	in := readHook(t, data)

	out := marshal(t, in)
	for _, dropped := range []string{
		"tool_input", "tool_response", "transcript_path", "tool_use_id", leakMarker, "notes.txt",
	} {
		if strings.Contains(out, dropped) {
			t.Errorf("re-marshaled input still contains %q: %s", dropped, out)
		}
	}
	want := protocol.HookInput{
		SessionID:      demoSession,
		HookEventName:  "PostToolUse",
		ToolName:       "Write",
		PermissionMode: "default",
		Cwd:            demoCwd,
	}
	if in != want {
		t.Errorf("ReadHookInput() = %+v, want %+v", in, want)
	}
}

func TestReadHookInput_UserPromptDropsPrompt(t *testing.T) {
	data := fixture(t, "hook_userpromptsubmit.json")
	if !strings.Contains(string(data), `"prompt"`) {
		t.Fatal("fixture has no prompt; the test would prove nothing")
	}

	in := readHook(t, data)

	out := marshal(t, in)
	for _, dropped := range []string{"prompt", "billing", "customer", leakMarker} {
		if strings.Contains(out, dropped) {
			t.Errorf("re-marshaled input still contains %q: %s", dropped, out)
		}
	}
	if in.HookEventName != "UserPromptSubmit" || in.SessionID != demoSession {
		t.Errorf("ReadHookInput() = %+v, lost the event identity", in)
	}
}

func TestReadHookInput_KeepsOnlyAllowlistedFields(t *testing.T) {
	tests := []struct {
		fixture string
		want    protocol.HookInput
	}{
		{
			fixture: "hook_sessionstart.json",
			want: protocol.HookInput{
				SessionID: demoSession, HookEventName: "SessionStart", Source: "resume", Cwd: demoCwd,
			},
		},
		{
			fixture: "hook_sessionend.json",
			want: protocol.HookInput{
				SessionID: demoSession, HookEventName: "SessionEnd", Reason: "prompt_input_exit", Cwd: demoCwd,
			},
		},
		{
			fixture: "hook_notification.json",
			want: protocol.HookInput{
				SessionID: demoSession, HookEventName: "Notification", NotificationType: "permission_prompt",
				PermissionMode: "default", Cwd: demoCwd,
			},
		},
		{
			fixture: "hook_stopfailure.json",
			want: protocol.HookInput{
				SessionID: demoSession, HookEventName: "StopFailure", PermissionMode: "default", Cwd: demoCwd,
			},
		},
		{
			fixture: "hook_subagentstart.json",
			want: protocol.HookInput{
				SessionID: demoSession, HookEventName: "SubagentStart", AgentID: "agent-00000000000000demo",
				AgentType: "Explore", PermissionMode: "default", Cwd: demoCwd,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			in := readHook(t, fixture(t, tt.fixture))

			if in != tt.want {
				t.Errorf("ReadHookInput() = %+v, want %+v", in, tt.want)
			}

			var kept map[string]json.RawMessage
			if err := json.Unmarshal([]byte(marshal(t, in)), &kept); err != nil {
				t.Fatalf("Unmarshal re-marshaled input: %v", err)
			}
			for key := range kept {
				if !isAllowlisted(key) {
					t.Errorf("re-marshaled input carries %q, which is not on the allowlist", key)
				}
			}
		})
	}
}

func isAllowlisted(key string) bool {
	for _, allowed := range allowlistedHookKeys {
		if key == allowed {
			return true
		}
	}
	return false
}

func TestReadHookInput_NoFixtureContentSurvives(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "hook_*.json"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no hook fixtures found: %v", err)
	}
	for _, path := range fixtures {
		t.Run(filepath.Base(path), func(t *testing.T) {
			out := marshal(t, readHook(t, fixture(t, filepath.Base(path))))

			if strings.Contains(out, leakMarker) || strings.Contains(out, "transcript") {
				t.Errorf("content survived decoding: %s", out)
			}
		})
	}
}

func TestReadHookInput_AllFieldsOptional(t *testing.T) {
	for _, input := range []string{
		`{}`,
		`null`,
		`{"session_id":null,"source":null,"agent_id":null}`,
		`{"unrelated":{"nested":[1,2,{"a":null}]}}`,
	} {
		in, err := protocol.ReadHookInput(strings.NewReader(input))
		if err != nil {
			t.Errorf("ReadHookInput(%s): %v", input, err)
		}
		if in != (protocol.HookInput{}) {
			t.Errorf("ReadHookInput(%s) = %+v, want the zero value", input, in)
		}
	}
}

func TestReadHookInput_ReadsOnlyTheFirstValue(t *testing.T) {
	in := readHook(t, []byte(`{"session_id":"first"}{"session_id":"second"}`))

	if in.SessionID != "first" {
		t.Errorf("SessionID = %q, want first", in.SessionID)
	}
}

func TestReadHookInput_RejectsUnreadableInput(t *testing.T) {
	for name, input := range map[string]string{
		"empty":         ``,
		"blank":         "  \n",
		"truncated":     `{"session_id":"s","prompt":"unfinished`,
		"not an object": `["session_id"]`,
		"a string":      `"session_id"`,
		"bad syntax":    `{session_id: s}`,
		"wrong type":    `{"session_id":42}`,
		"object field":  `{"source":{"kind":"resume"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			in, err := protocol.ReadHookInput(strings.NewReader(input))

			if err == nil {
				t.Errorf("ReadHookInput(%q) = %+v, want an error", input, in)
			}
			if in != (protocol.HookInput{}) {
				t.Errorf("ReadHookInput(%q) returned %+v alongside an error", input, in)
			}
		})
	}
}

func TestReadHookInput_ReportsReadErrors(t *testing.T) {
	failure := errors.New("pipe closed")
	r := io.MultiReader(strings.NewReader(`{"session_id":"s",`), failingReader{failure})

	_, err := protocol.ReadHookInput(r)

	if !errors.Is(err, failure) {
		t.Errorf("ReadHookInput() = %v, want it to wrap %v", err, failure)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

// repeatReader yields n copies of one byte without holding them in memory.
type repeatReader struct {
	b byte
	n int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.n)
	for i := range p[:n] {
		p[i] = r.b
	}
	r.n -= n
	return n, nil
}

const largePayloadBytes = 5 << 20

// largePrompt is a UserPromptSubmit payload whose prompt is one string of
// largePayloadBytes, flanked by the fields that are kept.
func largePrompt() io.Reader {
	return io.MultiReader(
		strings.NewReader(`{"session_id":"s","prompt":"`),
		&repeatReader{b: 'x', n: largePayloadBytes},
		strings.NewReader(`","hook_event_name":"UserPromptSubmit"}`),
	)
}

// largeToolResponse is a PostToolUse payload whose tool_response is a
// largePayloadBytes array of small values.
func largeToolResponse() io.Reader {
	const element = `{"line":"xxxxxxxxxxxxxxxx"},`
	return io.MultiReader(
		strings.NewReader(`{"session_id":"s","tool_response":[`),
		&repeatedStringReader{s: element, n: largePayloadBytes / len(element)},
		strings.NewReader(`null],"hook_event_name":"PostToolUse"}`),
	)
}

type repeatedStringReader struct {
	s string
	n int
	r *strings.Reader
}

func (r *repeatedStringReader) Read(p []byte) (int, error) {
	if r.r == nil || r.r.Len() == 0 {
		if r.n == 0 {
			return 0, io.EOF
		}
		r.n--
		r.r = strings.NewReader(r.s)
	}
	return r.r.Read(p)
}

func TestReadHookInput_LargeContentIsNotRetained(t *testing.T) {
	for name, source := range map[string]func() io.Reader{
		"a 5 MiB prompt":        largePrompt,
		"a 5 MiB tool response": largeToolResponse,
	} {
		t.Run(name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)

			in, err := protocol.ReadHookInput(source())
			if err != nil {
				t.Fatalf("ReadHookInput: %v", err)
			}

			runtime.GC()
			runtime.ReadMemStats(&after)
			runtime.KeepAlive(in)
			if in.SessionID != "s" || in.HookEventName == "" {
				t.Errorf("ReadHookInput() = %+v, lost the fields that follow the content", in)
			}
			if after.HeapAlloc > before.HeapAlloc+1<<20 {
				t.Errorf("heap grew from %d to %d bytes and stayed there, want the content released",
					before.HeapAlloc, after.HeapAlloc)
			}
		})
	}
}

func BenchmarkReadHookInput(b *testing.B) {
	for name, source := range map[string]func() io.Reader{
		"5MiBPrompt":       largePrompt,
		"5MiBToolResponse": largeToolResponse,
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, err := protocol.ReadHookInput(source()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func FuzzReadHookInput(f *testing.F) {
	for _, name := range []string{
		"hook_posttooluse.json", "hook_userpromptsubmit.json", "hook_sessionstart.json",
		"hook_notification.json", "hook_stopfailure.json", "hook_subagentstart.json",
	} {
		f.Add(fixture(f, name))
	}
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"session_id":`))
	f.Add([]byte(`{"SESSION_ID":"s","tool\u005fname":"t","cwd":null}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		in, err := protocol.ReadHookInput(bytes.NewReader(data))
		if err != nil {
			return
		}

		// Whatever was decoded must survive the hop to the server unchanged.
		again, err := protocol.ReadHookInput(strings.NewReader(marshal(t, in)))
		if err != nil || again != in {
			t.Errorf("re-reading %+v = %+v, %v", in, again, err)
		}
		_, _ = protocol.HookEvent(validEnvelope(t), in)
	})
}

func TestHookEvent(t *testing.T) {
	env := validEnvelope(t)
	in := protocol.HookInput{
		SessionID:        demoSession,
		HookEventName:    "Notification",
		Source:           "resume",
		Reason:           "other",
		NotificationType: "permission_prompt",
		ToolName:         "Bash",
		AgentID:          "agent-1",
		AgentType:        "Explore",
		PermissionMode:   "plan",
		Cwd:              demoCwd,
	}

	got, err := protocol.HookEvent(env, in)
	if err != nil {
		t.Fatalf("HookEvent: %v", err)
	}

	want := domain.Hook{
		At:               envelopeTime,
		Dir:              domain.ConfigDir(env.ConfigDir),
		Session:          demoSession,
		PID:              4242,
		Name:             "Notification",
		Source:           "resume",
		Reason:           "other",
		NotificationType: "permission_prompt",
		ToolName:         "Bash",
		AgentID:          "agent-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("HookEvent() = %+v, want %+v", got, want)
	}
}

func TestHookEvent_RequiresSessionID(t *testing.T) {
	_, err := protocol.HookEvent(validEnvelope(t), protocol.HookInput{HookEventName: "Stop"})

	if err == nil {
		t.Error("HookEvent() accepted an input without a session_id")
	}
}

func TestHookEvent_FromFixture(t *testing.T) {
	env := validEnvelope(t)
	env.PID = 0

	got, err := protocol.HookEvent(env, readHook(t, fixture(t, "hook_sessionend.json")))
	if err != nil {
		t.Fatalf("HookEvent: %v", err)
	}

	if got.Name != "SessionEnd" || got.Reason != "prompt_input_exit" || got.Session != demoSession || got.PID != 0 {
		t.Errorf("HookEvent() = %+v", got)
	}
}
