package forward_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dsj7419/loadline/internal/deadlinetest"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/server"
)

// received is one request a test server saw.
type received struct {
	method, path string
	header       http.Header
	body         []byte
}

// recorder is a test server handler that records each request and answers
// with status.
type recorder struct {
	status int

	mu       sync.Mutex
	requests []received
}

func (rec *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec.mu.Lock()
	rec.requests = append(rec.requests, received{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
	rec.mu.Unlock()
	w.WriteHeader(rec.status)
	_, _ = io.WriteString(w, "{}")
}

func (rec *recorder) seen() []received {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]received(nil), rec.requests...)
}

func TestClient_SendPostsTheEnvelopeWithTheToken(t *testing.T) {
	rec := &recorder{status: http.StatusOK}
	client := forward.Client{BaseURL: startServer(t, rec), Token: testToken}
	env := envelopeAt(1, t0)

	if err := client.Send(t.Context(), forward.HookPath, env); err != nil {
		t.Fatalf("Send: %v", err)
	}

	requests := rec.seen()
	if len(requests) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(requests))
	}
	got := requests[0]
	if got.method != http.MethodPost || got.path != "/ingest/hook" {
		t.Errorf("request = %s %s, want POST /ingest/hook", got.method, got.path)
	}
	if ct := got.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if token := got.header.Get(tokenHeader); token != string(testToken) {
		t.Errorf("%s = %q, want the token", tokenHeader, token)
	}
	var sent protocol.Envelope
	if err := json.Unmarshal(got.body, &sent); err != nil {
		t.Fatalf("decode the posted body: %v", err)
	}
	if sent.ID != env.ID || !sent.At.Equal(env.At) || string(sent.Payload) != string(env.Payload) {
		t.Errorf("posted envelope = %+v, want %+v", sent, env)
	}
}

func TestClient_StatusPathIsTheStatusRoute(t *testing.T) {
	if forward.StatusPath != "/ingest/status" || forward.HookPath != "/ingest/hook" {
		t.Errorf("paths = %q and %q, want /ingest/hook and /ingest/status", forward.HookPath, forward.StatusPath)
	}
}

// The real server takes what the client sends: the Host header, the token
// header and the envelope all pass its checks.
func TestClient_SendIsAcceptedByTheServer(t *testing.T) {
	ts, port := unstartedServer(t)
	sink := &recordingSink{}
	ts.Config.Handler = server.New(server.Deps{State: sink, Token: testToken, Port: port}).Handler()
	ts.Start()
	client := forward.Client{BaseURL: forward.LoopbackURL(port), Token: testToken}

	if err := client.Send(t.Context(), forward.HookPath, envelopeAt(1, t0)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	events := sink.applied()
	if len(events) != 1 {
		t.Fatalf("server applied %d events, want 1", len(events))
	}
	if hook, ok := events[0].(domain.Hook); !ok || hook.Session != "s1" {
		t.Errorf("applied %#v, want the hook of session s1", events[0])
	}
}

func TestClient_SendFailsUnlessTheServerTakesTheEvent(t *testing.T) {
	for _, status := range []int{
		http.StatusMultipleChoices,
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := forward.Client{BaseURL: startServer(t, &recorder{status: status}), Token: testToken}

			err := client.Send(t.Context(), forward.HookPath, envelopeAt(1, t0))

			if err == nil {
				t.Fatalf("Send succeeded on status %d, want an error", status)
			}
			if strings.Contains(err.Error(), string(testToken)) {
				t.Errorf("error %q holds the token", err)
			}
		})
	}
}

func TestClient_SendAcceptsAny2xx(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent} {
		client := forward.Client{BaseURL: startServer(t, &recorder{status: status}), Token: testToken}

		if err := client.Send(t.Context(), forward.HookPath, envelopeAt(1, t0)); err != nil {
			t.Errorf("Send on status %d: %v, want nil", status, err)
		}
	}
}

func TestClient_SendFailsWhenNothingListens(t *testing.T) {
	client := forward.Client{BaseURL: closedPortURL(t), Token: testToken}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	if err := client.Send(ctx, forward.HookPath, envelopeAt(1, t0)); err == nil {
		t.Fatal("Send succeeded with no server, want an error")
	}
}

// deadlineRecorder is an HTTP transport that answers no request: it notes
// how long each one had left before its deadline as it was made, and fails
// it. In a synctest bubble, whose clock stands still while nothing waits,
// that is exactly the bound the request was made with.
type deadlineRecorder struct {
	mu   sync.Mutex
	left []time.Duration
}

// noDeadline is what deadlineRecorder notes for a request without one.
const noDeadline time.Duration = -1

var errNotSent = errors.New("not sent: the transport only notes deadlines")

func (r *deadlineRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	left := noDeadline
	if deadline, ok := req.Context().Deadline(); ok {
		left = time.Until(deadline)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.left = append(r.left, left)
	return nil, errNotSent
}

// assertBound fails t unless exactly one request was made, bounded by want.
func (r *deadlineRecorder) assertBound(t *testing.T, want time.Duration) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.left) != 1 || r.left[0] != want {
		t.Errorf("requests were made with %v left before their deadline (%v is none), want one with %v", r.left, noDeadline, want)
	}
}

// Send bounds its post by its caller's context, and by nothing of its own.
func TestClient_SendPostsWithTheContextsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var rec deadlineRecorder
		client := forward.Client{BaseURL: forward.LoopbackURL(1), Token: testToken, HTTP: &http.Client{Transport: &rec}}
		ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
		defer cancel()

		if err := client.Send(ctx, forward.HookPath, envelopeAt(1, t0)); err == nil {
			t.Error("Send = nil with nothing sent, want an error")
		}

		rec.assertBound(t, 400*time.Millisecond)
	})
}

// Against a server that holds its post, Send returns once the context's
// deadline has passed, and no later than a little after.
func TestClient_SendStopsAtTheDeadline(t *testing.T) {
	const deadline = 400 * time.Millisecond
	// The answer comes, but long after Send should have given up on it.
	client := forward.Client{BaseURL: startHungServer(t, 2*deadlinetest.Limit(deadline)), Token: testToken}
	// Started before the deadline is set, so that the deadline is counted
	// in full.
	start := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), deadline)
	defer cancel()

	err := client.Send(ctx, forward.HookPath, envelopeAt(1, t0))

	deadlinetest.AssertGaveUpAt(t, "Send", time.Since(start), deadline)
	if err == nil {
		t.Error("Send = nil against a hung server, want an error")
	}
}

// A redirect would carry the token to wherever it points.
func TestClient_SendDoesNotFollowRedirects(t *testing.T) {
	elsewhere := &recorder{status: http.StatusOK}
	target := startServer(t, elsewhere)
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+"/ingest/hook", http.StatusTemporaryRedirect)
	})
	client := forward.Client{BaseURL: startServer(t, redirect), Token: testToken}

	err := client.Send(t.Context(), forward.HookPath, envelopeAt(1, t0))

	if err == nil {
		t.Error("Send succeeded through a redirect, want an error")
	}
	if n := len(elsewhere.seen()); n != 0 {
		t.Errorf("the redirect target saw %d requests, want none", n)
	}
}

// A large answer is not read to its end: the client reads a few KiB at most.
func TestClient_SendReadsLittleOfTheAnswer(t *testing.T) {
	endless := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		chunk := bytes.Repeat([]byte("x"), 32<<10)
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	client := forward.Client{BaseURL: startServer(t, endless), Token: testToken}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	if err := client.Send(ctx, forward.HookPath, envelopeAt(1, t0)); err != nil {
		t.Errorf("Send: %v, want nil without reading the whole answer", err)
	}
}

func TestClient_Healthy(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "loadline", status: http.StatusOK, body: `{"app":"loadline","version":"1.2.3"}`, want: true},
		{name: "another app", status: http.StatusOK, body: `{"app":"other"}`},
		{name: "no app", status: http.StatusOK, body: `{}`},
		{name: "not JSON", status: http.StatusOK, body: `loadline`},
		{name: "a JSON string", status: http.StatusOK, body: `"loadline"`},
		{name: "an error status", status: http.StatusInternalServerError, body: `{"app":"loadline"}`},
		{name: "a redirect", status: http.StatusFound, body: `{"app":"loadline"}`},
		{
			name: "an answer past 4 KiB", status: http.StatusOK,
			body: `{"pad":"` + strings.Repeat("x", 4<<10) + `","app":"loadline"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var paths []string
			var mu sync.Mutex
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.Method+" "+r.URL.Path)
				mu.Unlock()
				if tt.status == http.StatusFound {
					w.Header().Set("Location", "/healthz/elsewhere")
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			client := forward.Client{BaseURL: startServer(t, h)}

			if got := client.Healthy(t.Context()); got != tt.want {
				t.Errorf("Healthy() = %v, want %v", got, tt.want)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(paths) != 1 || paths[0] != "GET /healthz" {
				t.Errorf("requests = %v, want one GET /healthz", paths)
			}
		})
	}
}

func TestClient_HealthyAgainstTheServer(t *testing.T) {
	ts, port := unstartedServer(t)
	ts.Config.Handler = server.New(server.Deps{State: &recordingSink{}, Port: port}).Handler()
	ts.Start()

	if !(forward.Client{BaseURL: forward.LoopbackURL(port)}).Healthy(t.Context()) {
		t.Error("Healthy() = false against a Loadline server, want true")
	}
}

func TestClient_UnhealthyWhenNothingListens(t *testing.T) {
	if (forward.Client{BaseURL: closedPortURL(t)}).Healthy(t.Context()) {
		t.Error("Healthy() = true with no server, want false")
	}
}

// Healthy bounds its check by the Client's HealthTimeout, or by the default
// without one, exactly.
func TestClient_HealthyBoundsItsCheckByItsTimeout(t *testing.T) {
	for name, tt := range map[string]struct{ healthTimeout, want time.Duration }{
		"by default":                      {want: forward.DefaultHealthTimeout},
		"with a HealthTimeout of its own": {healthTimeout: 400 * time.Millisecond, want: 400 * time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var rec deadlineRecorder
				client := forward.Client{BaseURL: forward.LoopbackURL(1), HTTP: &http.Client{Transport: &rec}, HealthTimeout: tt.healthTimeout}

				if client.Healthy(t.Context()) {
					t.Error("Healthy() = true with nothing sent, want false")
				}

				rec.assertBound(t, tt.want)
			})
		})
	}
}

// Healthy bounds itself, so a hung server cannot hold the caller.
func TestClient_HealthyGivesUpOnAHungServer(t *testing.T) {
	const deadline = 400 * time.Millisecond
	// The answer comes, but long after Healthy should have given up on it.
	client := forward.Client{BaseURL: startHungServer(t, 2*deadlinetest.Limit(deadline)), HealthTimeout: deadline}
	start := time.Now()

	healthy := client.Healthy(context.Background())

	deadlinetest.AssertGaveUpAt(t, "Healthy", time.Since(start), deadline)
	if healthy {
		t.Error("Healthy() = true against a hung server, want false")
	}
}

// A Client without a HealthTimeout, such as the hook's check on SessionStart
// that a server answers, waits 150 ms at most, as the plan sets it: the hot
// path can afford no more.
func TestClient_DefaultHealthTimeoutIs150ms(t *testing.T) {
	if forward.DefaultHealthTimeout != 150*time.Millisecond {
		t.Errorf("Healthy waits %v by default, want 150ms", forward.DefaultHealthTimeout)
	}
}

// A caller off the hot path, such as the doctor, can wait longer for a busy
// server than a hook can.
func TestClient_HealthTimeoutLetsHealthyWaitLonger(t *testing.T) {
	slow := startServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(400 * time.Millisecond):
		}
		_, _ = io.WriteString(w, `{"app":"loadline"}`)
	}))

	if (forward.Client{BaseURL: slow}).Healthy(t.Context()) {
		t.Error("Healthy() = true after 400 ms with the default timeout, want false within its 150 ms")
	}
	if !(forward.Client{BaseURL: slow, HealthTimeout: 5 * time.Second}).Healthy(t.Context()) {
		t.Error("Healthy() = false with a 5 s HealthTimeout against a server answering in 400 ms, want true")
	}
}

func TestLoopbackURL(t *testing.T) {
	if got := forward.LoopbackURL(8289); got != "http://127.0.0.1:8289" {
		t.Errorf("LoopbackURL(8289) = %q, want http://127.0.0.1:8289", got)
	}
}

func TestClient_GetSendsTheTokenAndReturnsTheAnswer(t *testing.T) {
	rec := &recorder{status: http.StatusOK}
	client := forward.Client{BaseURL: startServer(t, rec), Token: testToken}

	body, err := client.Get(t.Context(), "/api/snapshot", 1<<10)

	if err != nil || string(body) != "{}" {
		t.Fatalf("Get = %q, %v; want the answer", body, err)
	}
	seen := rec.seen()
	if len(seen) != 1 || seen[0].method != http.MethodGet || seen[0].path != "/api/snapshot" {
		t.Fatalf("server saw %+v, want one GET of /api/snapshot", seen)
	}
	if got := seen[0].header.Get(tokenHeader); got != string(testToken) {
		t.Errorf("token header = %q, want the token", got)
	}
}

func TestClient_GetFailsUnlessTheServerAnswers200(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusNoContent} {
		client := forward.Client{BaseURL: startServer(t, &recorder{status: status}), Token: testToken}

		body, err := client.Get(t.Context(), "/api/snapshot", 1<<10)

		statusErr, ok := errors.AsType[*forward.StatusError](err)
		if !ok || statusErr.Code != status {
			t.Errorf("Get against a %d = %q, %v; want a StatusError with that code", status, body, err)
			continue
		}
		want := fmt.Sprintf("get /api/snapshot: the server answered %d %s", status, http.StatusText(status))
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err, want)
		}
	}
}

func TestClient_GetRefusesAnAnswerOverTheLimit(t *testing.T) {
	big := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 1<<10+1))
	})
	client := forward.Client{BaseURL: startServer(t, big), Token: testToken}

	if _, err := client.Get(t.Context(), "/api/snapshot", 1<<10); err == nil || !strings.Contains(err.Error(), "1024 bytes") {
		t.Errorf("Get = %v, want an error naming the 1024 byte limit", err)
	}
	exact := forward.Client{BaseURL: startServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 1<<10))
	})), Token: testToken}
	if body, err := exact.Get(t.Context(), "/api/snapshot", 1<<10); err != nil || len(body) != 1<<10 {
		t.Errorf("Get of exactly the limit = %d bytes, %v; want it all", len(body), err)
	}
}

func TestClient_GetDoesNotFollowRedirects(t *testing.T) {
	elsewhere := &recorder{status: http.StatusOK}
	target := startServer(t, elsewhere)
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+"/api/snapshot", http.StatusFound)
	})
	client := forward.Client{BaseURL: startServer(t, redirect), Token: testToken}

	if _, err := client.Get(t.Context(), "/api/snapshot", 1<<10); err == nil {
		t.Error("Get succeeded through a redirect, want an error")
	}
	if n := len(elsewhere.seen()); n != 0 {
		t.Errorf("the redirect target saw %d requests, want none", n)
	}
}

func TestClient_PostSendsTheTokenAndNoBodyAndReturnsTheAnswer(t *testing.T) {
	rec := &recorder{status: http.StatusOK}
	client := forward.Client{BaseURL: startServer(t, rec), Token: testToken}

	body, err := client.Post(t.Context(), "/api/login-code", 1<<10)

	if err != nil || string(body) != "{}" {
		t.Fatalf("Post = %q, %v; want the answer", body, err)
	}
	seen := rec.seen()
	if len(seen) != 1 || seen[0].method != http.MethodPost || seen[0].path != "/api/login-code" || len(seen[0].body) != 0 {
		t.Fatalf("server saw %+v, want one POST of /api/login-code with no body", seen)
	}
	if got := seen[0].header.Get(tokenHeader); got != string(testToken) {
		t.Errorf("token header = %q, want the token", got)
	}
}

func TestClient_PostFailsUnlessTheServerAnswers200(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNoContent} {
		client := forward.Client{BaseURL: startServer(t, &recorder{status: status}), Token: testToken}

		if body, err := client.Post(t.Context(), "/api/login-code", 1<<10); err == nil || !strings.Contains(err.Error(), "post /api/login-code") {
			t.Errorf("Post against a %d = %q, %v; want an error naming the request", status, body, err)
		}
	}
}

// The real server hands the client a login code: the Host header, the
// token and the method all pass its checks.
func TestClient_PostGetsALoginCodeFromTheServer(t *testing.T) {
	ts, port := unstartedServer(t)
	ts.Config.Handler = server.New(server.Deps{State: &recordingSink{}, Token: testToken, Port: port}).Handler()
	ts.Start()
	client := forward.Client{BaseURL: forward.LoopbackURL(port), Token: testToken}

	body, err := client.Post(t.Context(), "/api/login-code", 1<<10)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}

	var answer struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || len(answer.Code) != 32 {
		t.Errorf("Post = %s, %v; want a 32-digit login code", body, err)
	}
}
