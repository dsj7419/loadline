package forward_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestClient_SendStopsAtTheDeadline(t *testing.T) {
	client := forward.Client{BaseURL: startHungServer(t), Token: testToken}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()

	err := client.Send(ctx, forward.HookPath, envelopeAt(1, t0))

	if elapsed := time.Since(start); err == nil || elapsed > 200*time.Millisecond {
		t.Errorf("Send = %v after %v against a hung server, want an error by the 100 ms deadline", err, elapsed)
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

// Healthy bounds itself, so a hung server cannot hold the caller.
func TestClient_HealthyGivesUpOnAHungServer(t *testing.T) {
	client := forward.Client{BaseURL: startHungServer(t)}
	start := time.Now()

	healthy := client.Healthy(context.Background())

	if elapsed := time.Since(start); healthy || elapsed > 250*time.Millisecond {
		t.Errorf("Healthy() = %v after %v against a hung server, want false within its 150 ms", healthy, elapsed)
	}
}

func TestLoopbackURL(t *testing.T) {
	if got := forward.LoopbackURL(8289); got != "http://127.0.0.1:8289" {
		t.Errorf("LoopbackURL(8289) = %q, want http://127.0.0.1:8289", got)
	}
}
