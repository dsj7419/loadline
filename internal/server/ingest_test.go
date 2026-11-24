package server_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/otlp"
	"github.com/dsj7419/loadline/internal/protocol"
)

const (
	maxBody     = 1 << 20
	maxLogsBody = 4 << 20

	// refill is how long the rate limit takes to earn one request back.
	refill = 2 * time.Millisecond
)

func apiRequestRecord(session, model string, costUSD float64) string {
	return fmt.Sprintf(`{"timeUnixNano":"1795299794980000000","attributes":[`+
		`{"key":"event.name","value":{"stringValue":"api_request"}},`+
		`{"key":"session.id","value":{"stringValue":%q}},`+
		`{"key":"model","value":{"stringValue":%q}},`+
		`{"key":"input_tokens","value":{"intValue":10}},`+
		`{"key":"cost_usd","value":{"doubleValue":%g}}]}`, session, model, costUSD)
}

// promptRecord is a user_prompt event, which carries content and must be
// ignored.
const promptRecord = `{"timeUnixNano":"1795299794980000000","attributes":[` +
	`{"key":"event.name","value":{"stringValue":"user_prompt"}},` +
	`{"key":"session.id","value":{"stringValue":"s1"}},` +
	`{"key":"prompt","value":{"stringValue":"` + canary + `"}}]}`

func logsExport(records ...string) []byte {
	return []byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[` + strings.Join(records, ",") + `]}]}]}`)
}

func TestIngest_RequiresToken(t *testing.T) {
	hook, status := hookEnvelope(t, eventID(1)), statusEnvelope(t, eventID(2))
	routes := []struct {
		name string
		make func() *http.Request
	}{
		{name: "hook", make: func() *http.Request { return hookRequest(hook) }},
		{name: "status", make: func() *http.Request { return statusRequest(status) }},
		{name: "logs", make: func() *http.Request { return logsRequest(logsExport(apiRequestRecord("s1", "m", 1))) }},
		{name: "diag", make: func() *http.Request { return tokenRequest(http.MethodGet, "/api/diag", nil) }},
		{name: "login code", make: func() *http.Request { return tokenRequest(http.MethodPost, "/api/login-code", nil) }},
	}
	credentials := []struct {
		name string
		set  func(r *http.Request)
	}{
		{name: "no token", set: func(r *http.Request) { r.Header.Del(tokenHeader) }},
		{name: "empty token", set: func(r *http.Request) { r.Header.Set(tokenHeader, "") }},
		{name: "wrong token", set: func(r *http.Request) { r.Header.Set(tokenHeader, strings.Repeat("f", len(testToken))) }},
		{name: "truncated token", set: func(r *http.Request) { r.Header.Set(tokenHeader, string(testToken[:len(testToken)-1])) }},
		{name: "token with a suffix", set: func(r *http.Request) { r.Header.Set(tokenHeader, string(testToken)+"0") }},
		{name: "token in upper case", set: func(r *http.Request) { r.Header.Set(tokenHeader, strings.ToUpper(string(testToken))) }},
		{name: "token as a bearer", set: func(r *http.Request) {
			r.Header.Del(tokenHeader)
			r.Header.Set("Authorization", "Bearer "+string(testToken))
		}},
		{name: "token in another header", set: func(r *http.Request) {
			r.Header.Del(tokenHeader)
			r.Header.Set("X-Token", string(testToken))
		}},
		{name: "UI cookie", set: func(r *http.Request) {
			r.Header.Del(tokenHeader)
			addCookie(r, uiCookie, uiCookieValue(testToken))
		}},
	}
	for _, route := range routes {
		for _, cred := range credentials {
			t.Run(route.name+"/"+cred.name, func(t *testing.T) {
				h := newHarness(t)
				r := route.make()
				cred.set(r)

				assertJSONError(t, h.do(r), http.StatusUnauthorized)
				h.assertNothingApplied(t)
			})
		}
		t.Run(route.name+"/the token", func(t *testing.T) {
			h := newHarness(t)
			assertStatus(t, h.do(route.make()), http.StatusOK)
		})
	}
}

func TestIngest_HookAppliesAndKicks(t *testing.T) {
	h := newHarness(t)

	rec := h.do(hookRequest(hookEnvelope(t, eventID(1))))

	assertStatus(t, rec, http.StatusOK)
	assertJSONBody(t, rec, `{}`)
	events, requests := h.sink.recorded()
	want := domain.Hook{
		At:      eventTime,
		Dir:     domain.ConfigDir(configDir),
		Session: "s1",
		PID:     testPID,
		Name:    "SessionStart",
		Source:  "startup",
	}
	if len(events) != 1 || events[0] != want {
		t.Errorf("applied %+v, want [%+v]", events, want)
	}
	if len(requests) != 0 {
		t.Errorf("recorded requests %+v, want none", requests)
	}
	kicks, dirs := h.observed()
	if kicks != 1 {
		t.Errorf("kicked %d times, want 1", kicks)
	}
	if len(dirs) != 1 || dirs[0] != domain.ConfigDir(configDir) {
		t.Errorf("noted dirs %v, want [%s]", dirs, configDir)
	}
}

func TestIngest_StatusAppliesAndNotesTheDir(t *testing.T) {
	h := newHarness(t)

	rec := h.do(statusRequest(statusEnvelope(t, eventID(1))))

	assertStatus(t, rec, http.StatusOK)
	assertJSONBody(t, rec, `{}`)
	events, _ := h.sink.recorded()
	if len(events) != 1 {
		t.Fatalf("applied %d events, want 1", len(events))
	}
	got, ok := events[0].(domain.Status)
	if !ok {
		t.Fatalf("applied %T, want domain.Status", events[0])
	}
	if got.Session != "s1" || got.Dir != domain.ConfigDir(configDir) || !got.At.Equal(eventTime) ||
		got.Metrics.Model != "Opus" || got.Limits.SevenDay == nil || got.Limits.SevenDay.UsedPct != 42 {
		t.Errorf("applied %+v, want the status line of s1 in %s", got, configDir)
	}
	kicks, dirs := h.observed()
	if kicks != 0 {
		t.Errorf("kicked %d times, want 0: a status line does not start a poll", kicks)
	}
	if len(dirs) != 1 || dirs[0] != domain.ConfigDir(configDir) {
		t.Errorf("noted dirs %v, want [%s]", dirs, configDir)
	}
}

// A status line's rate limits are judged by the process it came from, which
// its envelope names as a hook's does, and an envelope that names none leaves
// it unknown.
func TestIngest_AStatusLineCarriesItsProcess(t *testing.T) {
	withoutPID := envelopeWith(t, protocol.KindStatus, statusPayload, func(fields map[string]json.RawMessage) {
		delete(fields, "pid")
	})
	for name, tc := range map[string]struct {
		body []byte
		want int
	}{
		"the envelope's pid": {body: statusEnvelope(t, eventID(1)), want: testPID},
		"no pid":             {body: withoutPID, want: 0},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)

			assertStatus(t, h.do(statusRequest(tc.body)), http.StatusOK)

			events, _ := h.sink.recorded()
			if len(events) != 1 {
				t.Fatalf("applied %d events, want 1", len(events))
			}
			if got, ok := events[0].(domain.Status); !ok || got.PID != tc.want {
				t.Errorf("applied %+v, want the status line of pid %d", events[0], tc.want)
			}
		})
	}
}

func TestIngest_RejectsOversizedBody(t *testing.T) {
	routes := []struct {
		name string
		make func(body []byte) *http.Request
		body []byte
	}{
		{name: "hook", make: hookRequest, body: hookEnvelope(t, eventID(1))},
		{name: "status", make: statusRequest, body: statusEnvelope(t, eventID(1))},
	}
	for _, route := range routes {
		t.Run(route.name+" at the cap", func(t *testing.T) {
			h := newHarness(t)
			assertStatus(t, h.do(route.make(padTo(t, route.body, maxBody))), http.StatusOK)
		})
		t.Run(route.name+" over the cap", func(t *testing.T) {
			h := newHarness(t)
			assertJSONError(t, h.do(route.make(padTo(t, route.body, maxBody+1))), http.StatusRequestEntityTooLarge)
			h.assertNothingApplied(t)
		})
	}
}

func TestIngest_RejectsMalformedEnvelope(t *testing.T) {
	set := func(key, value string) func(map[string]json.RawMessage) {
		return func(f map[string]json.RawMessage) { f[key] = json.RawMessage(value) }
	}
	remove := func(key string) func(map[string]json.RawMessage) {
		return func(f map[string]json.RawMessage) { delete(f, key) }
	}
	deepPayload := `{"session_id":"s1","x":` + strings.Repeat("[", 20000) + strings.Repeat("]", 20000) + `}`

	hook := hookEnvelope(t, eventID(1))
	bigPayload := `{"session_id":"s1","x":"` + strings.Repeat("a", protocol.MaxPayloadBytes) + `"}`

	// Each case names the check that must refuse it, so that no check passes
	// only because a later one catches what it let through.
	const (
		malformedEnvelope = "malformed envelope"
		invalidEnvelope   = "invalid envelope"
		wrongKind         = "envelope kind does not match the route"
		noPayload         = "envelope has no payload"
		malformedPayload  = "malformed payload"
		noSession         = "payload has no session"
	)
	tests := []struct {
		name   string
		route  func([]byte) *http.Request
		body   []byte
		reason string
	}{
		{name: "empty body", reason: malformedEnvelope},
		{name: "not JSON", body: []byte(canary), reason: malformedEnvelope},
		{name: "truncated", body: hook[:len(hook)-1], reason: malformedEnvelope},
		{name: "array", body: []byte(`["` + canary + `"]`), reason: malformedEnvelope},
		{name: "null", body: []byte(`null`), reason: invalidEnvelope},
		{name: "string", body: []byte(`"` + canary + `"`), reason: malformedEnvelope},
		{
			name: "unknown field", reason: malformedEnvelope,
			body: envelopeWith(t, protocol.KindHook, hookPayload, set("extra", `"`+canary+`"`)),
		},
		{name: "trailing data", body: slices.Concat(hook, []byte(` {"`+canary+`":1}`)), reason: malformedEnvelope},
		{name: "two envelopes", body: slices.Concat(hook, hookEnvelope(t, eventID(2))), reason: malformedEnvelope},
		// encoding/json refuses to marshal a value nested this deep, so the
		// payload is spliced in.
		{
			name: "deeply nested payload", reason: malformedEnvelope,
			body: bytes.Replace(hook, []byte(hookPayload), []byte(deepPayload), 1),
		},
		{
			name: "unsupported version", reason: invalidEnvelope,
			body: envelopeWith(t, protocol.KindHook, hookPayload, set("v", "2")),
		},
		{
			name: "bad event id", reason: invalidEnvelope,
			body: envelopeWith(t, protocol.KindHook, hookPayload, set("id", `"`+canary+`"`)),
		},
		{
			name: "relative config dir", reason: invalidEnvelope,
			body: envelopeWith(t, protocol.KindHook, hookPayload, set("configDir", `"`+canary+`/.claude"`)),
		},
		{name: "no time", body: envelopeWith(t, protocol.KindHook, hookPayload, remove("at")), reason: invalidEnvelope},
		{
			name: "time past year 9999 in UTC", reason: invalidEnvelope,
			body: envelopeWith(t, protocol.KindHook, hookPayload, set("at", `"9999-12-31T23:59:59-01:00"`)),
		},
		{
			name: "status time past year 9999 in UTC", route: statusRequest, reason: invalidEnvelope,
			body: envelopeWith(t, protocol.KindStatus, statusPayload, set("at", `"9999-12-31T23:59:59-01:00"`)),
		},
		{
			name: "time before the epoch", reason: invalidEnvelope,
			body: envelopeWith(t, protocol.KindHook, hookPayload, set("at", `"1969-12-31T23:59:59Z"`)),
		},
		{
			name: "unknown kind", reason: invalidEnvelope,
			body: envelopeWith(t, protocol.KindHook, hookPayload, set("kind", `"poll"`)),
		},
		{
			name: "payload over the envelope limit", reason: invalidEnvelope,
			body: envelope(t, protocol.KindHook, eventID(1), bigPayload),
		},
		{name: "status envelope on the hook route", body: statusEnvelope(t, eventID(1)), reason: wrongKind},
		{name: "hook envelope on the status route", route: statusRequest, body: hook, reason: wrongKind},
		{
			name: "no payload", reason: noPayload,
			body: envelopeWith(t, protocol.KindHook, hookPayload, remove("payload")),
		},
		{
			name: "null payload", reason: noPayload,
			body: envelopeWith(t, protocol.KindHook, hookPayload, set("payload", "null")),
		},
		{
			name: "no status payload", route: statusRequest, reason: noPayload,
			body: envelopeWith(t, protocol.KindStatus, statusPayload, remove("payload")),
		},
		{
			name: "null status payload", route: statusRequest, reason: noPayload,
			body: envelopeWith(t, protocol.KindStatus, statusPayload, set("payload", "null")),
		},
		{
			name: "payload is a number", reason: malformedPayload,
			body: envelopeWith(t, protocol.KindHook, hookPayload, set("payload", "5")),
		},
		{
			name: "payload is a string", reason: malformedPayload,
			body: envelopeWith(t, protocol.KindHook, hookPayload, set("payload", `"`+canary+`"`)),
		},
		{
			name: "payload is an array", route: statusRequest, reason: malformedPayload,
			body: envelopeWith(t, protocol.KindStatus, statusPayload, set("payload", `[]`)),
		},
		{
			name: "hook payload without a session", reason: noSession,
			body: envelope(t, protocol.KindHook, eventID(1), `{"hook_event_name":"`+canary+`"}`),
		},
		{
			name: "status payload without a session", route: statusRequest, reason: noSession,
			body: envelope(t, protocol.KindStatus, eventID(1), `{"session_name":"`+canary+`"}`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			route := tt.route
			if route == nil {
				route = hookRequest
			}

			rec := h.do(route(tt.body))

			assertJSONError(t, rec, http.StatusBadRequest)
			assertJSONBody(t, rec, `{"error":"`+tt.reason+`"}`)
			h.assertNothingApplied(t)
		})
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// A hook or a tap stamps its event with its own clock before it posts it,
// so an event dated more than 5 s ahead of the server's clock comes from
// before a step back of the system clock, or from a broken one. It is taken
// as happening now, so that it cannot date its session ahead of all that
// follows.
func TestIngest_EventTimeAheadOfTheServerClockIsTakenAsNow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ahead time.Duration
		want  time.Time
	}{
		{name: "an hour ahead", ahead: time.Hour, want: t0},
		{name: "just over 5 s ahead", ahead: 5*time.Second + time.Nanosecond, want: t0},
		{name: "5 s ahead", ahead: 5 * time.Second, want: t0.Add(5 * time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := func(f map[string]json.RawMessage) {
				f["at"] = json.RawMessage(`"` + t0.Add(tc.ahead).Format(time.RFC3339Nano) + `"`)
			}
			h := newHarness(t)
			assertStatus(t, h.do(hookRequest(envelopeWith(t, protocol.KindHook, hookPayload, at))), http.StatusOK)
			assertStatus(t, h.do(statusRequest(envelopeWith(t, protocol.KindStatus, statusPayload, func(f map[string]json.RawMessage) {
				at(f)
				f["id"] = json.RawMessage(`"` + eventID(2) + `"`)
			}))), http.StatusOK)
			spooled := spooledEnvelope(t, protocol.KindHook, eventID(3), hookPayload)
			spooled.At = t0.Add(tc.ahead)
			if err := h.srv.ApplySpooledHook(spooled); err != nil {
				t.Fatalf("ApplySpooledHook: %v", err)
			}

			events, _ := h.sink.recorded()
			if len(events) != 3 {
				t.Fatalf("applied %d events, want 3", len(events))
			}
			for _, e := range events {
				switch e := e.(type) {
				case domain.Hook:
					if !e.At.Equal(tc.want) {
						t.Errorf("hook dated %v, want %v", e.At, tc.want)
					}
				case domain.Status:
					if !e.At.Equal(tc.want) || !e.Limits.ObservedAt.Equal(tc.want) {
						t.Errorf("status dated %v, limits observed at %v; want both %v", e.At, e.Limits.ObservedAt, tc.want)
					}
				default:
					t.Errorf("applied %T, want hooks and a status", e)
				}
			}
		})
	}
}

func TestIngest_UnreadableBodyIsABadRequest(t *testing.T) {
	h := newHarness(t)
	r := hookRequest(nil)
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader([]byte(`{"v":1,`)), failingReader{}))

	assertJSONError(t, h.do(r), http.StatusBadRequest)
	h.assertNothingApplied(t)
}

func TestIngest_DuplicateEventIDAppliedOnce(t *testing.T) {
	h := newHarness(t)
	first := hookEnvelope(t, eventID(1))

	assertStatus(t, h.do(hookRequest(first)), http.StatusOK)
	assertStatus(t, h.do(hookRequest(first)), http.StatusOK)
	assertStatus(t, h.do(statusRequest(envelope(t, protocol.KindStatus, eventID(1), statusPayload))), http.StatusOK)

	events, _ := h.sink.recorded()
	kicks, dirs := h.observed()
	if len(events) != 1 || kicks != 1 || len(dirs) != 1 {
		t.Fatalf("applied %d events, kicked %d times and noted %d dirs; want each once", len(events), kicks, len(dirs))
	}

	assertStatus(t, h.do(hookRequest(hookEnvelope(t, eventID(2)))), http.StatusOK)
	if events, _ := h.sink.recorded(); len(events) != 2 {
		t.Errorf("applied %d events, want 2: a new ID is a new event", len(events))
	}
}

func TestIngest_DuplicateIDsAreForgottenAfter15Minutes(t *testing.T) {
	h := newHarness(t)
	body := hookEnvelope(t, eventID(1))
	assertStatus(t, h.do(hookRequest(body)), http.StatusOK)

	h.clock.Advance(15*time.Minute - time.Nanosecond)
	assertStatus(t, h.do(hookRequest(body)), http.StatusOK)
	if events, _ := h.sink.recorded(); len(events) != 1 {
		t.Fatalf("applied %d events, want 1: the ID is still recent", len(events))
	}

	h.clock.Advance(time.Nanosecond)
	assertStatus(t, h.do(hookRequest(body)), http.StatusOK)
	if events, _ := h.sink.recorded(); len(events) != 2 {
		t.Errorf("applied %d events, want 2: the ID is 15 minutes old", len(events))
	}
}

func TestIngest_DuplicateIDsAreBoundedTo4096(t *testing.T) {
	h := newHarness(t)
	post := func(i int) {
		t.Helper()
		h.clock.Advance(refill)
		assertStatus(t, h.do(statusRequest(statusEnvelope(t, eventID(i)))), http.StatusOK)
	}
	applied := func() int {
		events, _ := h.sink.recorded()
		return len(events)
	}

	post(0)
	for i := 1; i < 4096; i++ {
		post(i)
	}
	post(0)
	if got := applied(); got != 4096 {
		t.Fatalf("applied %d events, want 4096: the first ID is among the last 4096", got)
	}

	post(4096)
	post(0)
	if got := applied(); got != 4098 {
		t.Errorf("applied %d events, want 4098: the first ID fell out of the last 4096", got)
	}
}

func TestIngest_EventWhoseApplyPanicsIsAppliedOnRetry(t *testing.T) {
	h := newHarness(t)
	body := hookEnvelope(t, eventID(1))
	h.sink.panicOnNextApply()

	if !h.doPanicking(hookRequest(body)) {
		t.Fatal("applying the event did not panic")
	}
	assertStatus(t, h.do(hookRequest(body)), http.StatusOK)
	assertStatus(t, h.do(hookRequest(body)), http.StatusOK)

	events, _ := h.sink.recorded()
	kicks, _ := h.observed()
	if len(events) != 1 || kicks != 1 {
		t.Errorf("applied %d events and kicked %d times, want the retry applied once", len(events), kicks)
	}
}

func TestIngest_RetryAfterAPanicStaysDeduplicated(t *testing.T) {
	h := newHarness(t)
	post := func(i int) {
		t.Helper()
		h.clock.Advance(refill)
		assertStatus(t, h.do(statusRequest(statusEnvelope(t, eventID(i)))), http.StatusOK)
	}
	h.sink.panicOnNextApply()
	if !h.doPanicking(statusRequest(statusEnvelope(t, eventID(0)))) {
		t.Fatal("applying the event did not panic")
	}
	post(0)

	// Push the failed attempt, but not the retry, out of the last 4096.
	for i := 1; i < 4096; i++ {
		post(i)
	}
	post(0)

	if events, _ := h.sink.recorded(); len(events) != 4096 {
		t.Errorf("applied %d events, want 4096: the retry is still among the last 4096", len(events))
	}
}

func TestIngest_SameIDPostedConcurrentlyIsAppliedOnce(t *testing.T) {
	h := newHarness(t)
	body := hookEnvelope(t, eventID(1))

	codes := h.concurrently(64, func(int) *http.Request { return hookRequest(body) })

	if got := countCodes(codes); got[http.StatusOK] != 64 {
		t.Errorf("statuses = %v, want 64 200s", got)
	}
	events, _ := h.sink.recorded()
	kicks, _ := h.observed()
	if len(events) != 1 || kicks != 1 {
		t.Errorf("applied %d events and kicked %d times, want 1 each", len(events), kicks)
	}
}

func TestIngest_ParallelIngestAdmitsNoMoreThanTheBurst(t *testing.T) {
	h := newHarness(t)

	codes := h.concurrently(1500, func(i int) *http.Request {
		return statusRequest(statusEnvelope(t, eventID(i)))
	})

	if got := countCodes(codes); got[http.StatusOK] != 1000 || got[http.StatusTooManyRequests] != 500 {
		t.Errorf("statuses = %v, want 1000 200s and 500 429s in one instant", got)
	}
}

func TestIngest_RateLimited(t *testing.T) {
	h := newHarness(t)
	for i := range 1000 {
		assertStatus(t, h.do(statusRequest(statusEnvelope(t, eventID(i)))), http.StatusOK)
	}

	for _, r := range []*http.Request{
		statusRequest(statusEnvelope(t, eventID(1000))),
		hookRequest(hookEnvelope(t, eventID(1001))),
		logsRequest(logsExport(apiRequestRecord("s1", "m", 1))),
	} {
		rec := h.do(r)
		assertJSONError(t, rec, http.StatusTooManyRequests)
		if got := rec.Header().Get("Retry-After"); got != "1" {
			t.Errorf("Retry-After = %q, want 1", got)
		}
	}
	if events, requests := h.sink.recorded(); len(events) != 1000 || len(requests) != 0 {
		t.Errorf("applied %d events and %d requests, want 1000 and 0", len(events), len(requests))
	}

	h.clock.Advance(refill)
	assertStatus(t, h.do(statusRequest(statusEnvelope(t, eventID(1002)))), http.StatusOK)
	assertJSONError(t, h.do(statusRequest(statusEnvelope(t, eventID(1003)))), http.StatusTooManyRequests)
}

func TestIngest_RateLimitRefillsAt500PerSecondUpToTheBurst(t *testing.T) {
	h := newHarness(t)
	next := 0
	accepted := func() int {
		n := 0
		for {
			next++
			rec := h.do(statusRequest(statusEnvelope(t, eventID(next))))
			if rec.Code == http.StatusTooManyRequests {
				return n
			}
			assertStatus(t, rec, http.StatusOK)
			n++
		}
	}

	if got := accepted(); got != 1000 {
		t.Fatalf("accepted %d requests at once, want the burst of 1000", got)
	}
	h.clock.Advance(time.Second)
	if got := accepted(); got != 500 {
		t.Errorf("accepted %d requests after a second, want 500", got)
	}
	h.clock.Advance(10 * time.Second)
	if got := accepted(); got != 1000 {
		t.Errorf("accepted %d requests after ten idle seconds, want the burst of 1000", got)
	}
	h.clock.Advance(refill - time.Nanosecond)
	if got := accepted(); got != 0 {
		t.Errorf("accepted %d requests before a request was earned, want 0", got)
	}
	h.clock.Advance(time.Nanosecond)
	if got := accepted(); got != 1 {
		t.Errorf("accepted %d requests once one was earned, want 1", got)
	}
}

func TestIngest_RateLimitSurvivesTheClockSteppingBack(t *testing.T) {
	h := newHarness(t)
	for i := range 1000 {
		assertStatus(t, h.do(statusRequest(statusEnvelope(t, eventID(i)))), http.StatusOK)
	}

	h.clock.Advance(-time.Hour)
	assertJSONError(t, h.do(statusRequest(statusEnvelope(t, eventID(1000)))), http.StatusTooManyRequests)
	h.clock.Advance(refill)

	assertStatus(t, h.do(statusRequest(statusEnvelope(t, eventID(1001)))), http.StatusOK)
}

func TestIngest_RejectedCredentialsDoNotSpendTheRateLimit(t *testing.T) {
	h := newHarness(t)
	for i := range 2000 {
		r := statusRequest(statusEnvelope(t, eventID(i)))
		r.Header.Set(tokenHeader, strings.Repeat("f", len(testToken)))
		assertStatus(t, h.do(r), http.StatusUnauthorized)
	}

	for i := range 1000 {
		assertStatus(t, h.do(statusRequest(statusEnvelope(t, eventID(i)))), http.StatusOK)
	}
}

func TestOTLP_RecordsRequests(t *testing.T) {
	fixture, err := os.ReadFile("../otlp/testdata/api_request.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	tests := []struct {
		name string
		body []byte
	}{
		{name: "captured export", body: fixture},
		{name: "several requests among other events", body: logsExport(
			apiRequestRecord("s1", "claude-opus-5-5", 0.25),
			promptRecord,
			apiRequestRecord("s2", "claude-haiku-4-5", 0.5),
		)},
		{name: "no requests", body: logsExport(promptRecord)},
		{name: "empty export", body: []byte(`{"resourceLogs":[]}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, err := otlp.DecodeLogs(bytes.NewReader(tt.body), maxLogsBody)
			if err != nil {
				t.Fatalf("DecodeLogs: %v", err)
			}
			h := newHarness(t)

			rec := h.do(logsRequest(tt.body))

			assertStatus(t, rec, http.StatusOK)
			assertJSONBody(t, rec, `{}`)
			events, got := h.sink.recorded()
			if len(got) != len(want) {
				t.Fatalf("recorded %d requests, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("request %d = %+v, want %+v", i, got[i], want[i])
				}
			}
			if len(events) != 0 {
				t.Errorf("applied %d events, want none", len(events))
			}
		})
	}
}

func TestOTLP_ContentRules(t *testing.T) {
	tests := []struct {
		name            string
		contentType     []string
		contentEncoding []string
		want            int
	}{
		{name: "JSON", contentType: []string{"application/json"}, want: http.StatusOK},
		{name: "JSON with a charset", contentType: []string{"application/json; charset=utf-8"}, want: http.StatusOK},
		{name: "JSON in mixed case", contentType: []string{"Application/JSON"}, want: http.StatusOK},
		{name: "identity encoding", contentType: []string{"application/json"}, contentEncoding: []string{"identity"}, want: http.StatusOK},
		{name: "empty encoding", contentType: []string{"application/json"}, contentEncoding: []string{""}, want: http.StatusOK},
		{name: "no content type", want: http.StatusUnsupportedMediaType},
		{name: "protobuf", contentType: []string{"application/x-protobuf"}, want: http.StatusUnsupportedMediaType},
		{name: "plain text", contentType: []string{"text/plain"}, want: http.StatusUnsupportedMediaType},
		{name: "JSON prefix", contentType: []string{"application/jsonx"}, want: http.StatusUnsupportedMediaType},
		{name: "unparsable content type", contentType: []string{"application/json; charset"}, want: http.StatusUnsupportedMediaType},
		{name: "gzip", contentType: []string{"application/json"}, contentEncoding: []string{"gzip"}, want: http.StatusUnsupportedMediaType},
		{
			name: "identity, then gzip", contentType: []string{"application/json"},
			contentEncoding: []string{"identity", "gzip"}, want: http.StatusUnsupportedMediaType,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			r := tokenRequest(http.MethodPost, "/v1/logs", logsExport(apiRequestRecord("s1", "m", 1)))
			r.Header["Content-Type"] = tt.contentType
			r.Header["Content-Encoding"] = tt.contentEncoding

			rec := h.do(r)

			if tt.want != http.StatusOK {
				assertJSONError(t, rec, tt.want)
				h.assertNothingApplied(t)
				return
			}
			assertStatus(t, rec, http.StatusOK)
			if _, requests := h.sink.recorded(); len(requests) != 1 {
				t.Errorf("recorded %d requests, want 1", len(requests))
			}
		})
	}
}

func TestOTLP_RejectsMalformedAndOversizedBodies(t *testing.T) {
	valid := logsExport(apiRequestRecord("s1", "m", 1))
	tests := []struct {
		name string
		body []byte
		want int
	}{
		{name: "at the cap", body: padTo(t, valid, maxLogsBody), want: http.StatusOK},
		{name: "over the cap", body: padTo(t, valid, maxLogsBody+1), want: http.StatusRequestEntityTooLarge},
		{name: "empty", body: nil, want: http.StatusBadRequest},
		{name: "not JSON", body: []byte(canary), want: http.StatusBadRequest},
		{name: "truncated", body: valid[:len(valid)-1], want: http.StatusBadRequest},
		{name: "wrong shape", body: []byte(`{"resourceLogs":"` + canary + `"}`), want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)

			rec := h.do(logsRequest(tt.body))

			if tt.want == http.StatusOK {
				assertStatus(t, rec, http.StatusOK)
				return
			}
			assertJSONError(t, rec, tt.want)
			h.assertNothingApplied(t)
		})
	}
}
