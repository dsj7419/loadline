// Package forward carries envelopes from loadline hook and loadline tap to
// the server on the loopback interface, and keeps the hook events the server
// could not take in a spool until it can. Its client also calls the server's
// API for the commands that report on it or open its dashboard.
package forward

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dsj7419/loadline/internal/protocol"
)

const (
	// HookPath is the server's ingest route for hook envelopes.
	HookPath = "/ingest/hook"
	// StatusPath is the server's ingest route for status-line envelopes.
	StatusPath = "/ingest/status"

	tokenHeader = "X-Loadline-Token" //nolint:gosec // G101: the name of the header, not a credential
	healthPath  = "/healthz"
	appName     = "loadline"

	// maxAnswer is as much of an answer as the client reads. The server's
	// answers to it are a few dozen bytes.
	maxAnswer = 4 << 10
	// defaultHealthTimeout bounds Healthy unless a Client sets its own. A
	// server on the loopback interface that is up answers in well under a
	// millisecond, and a hook or a tap cannot wait much longer.
	defaultHealthTimeout = 150 * time.Millisecond
)

// direct is the HTTP client used when a Client names none. It never goes
// through a proxy, so the token and the events stay on the loopback
// interface whatever HTTP_PROXY says, and it follows no redirect, which
// would carry the token to wherever it points.
var direct = &http.Client{
	Transport: &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{}).DialContext,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Client posts envelopes to a Loadline server.
type Client struct {
	// BaseURL is the server's address, such as LoopbackURL(port).
	BaseURL string
	// Token is the ingest token. Healthy does not need it.
	Token []byte
	// HTTP sends the requests. Nil means a client that never uses a proxy
	// and follows no redirect.
	HTTP *http.Client
	// HealthTimeout bounds Healthy. Zero means 150 ms, which the hook and
	// tap hot path can afford; a caller off it may give a busy server longer.
	HealthTimeout time.Duration
}

// StatusError is the error of a Get or Post that the server answered with a
// status other than 200.
type StatusError struct {
	// Method is the request method, such as GET.
	Method string
	Path   string
	// Code is the status code, such as 401.
	Code int
	// Status is the status line, such as "401 Unauthorized".
	Status string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s %s: the server answered %s", strings.ToLower(e.Method), e.Path, e.Status)
}

// LoopbackURL returns the base URL of a server listening on port of the
// loopback interface.
func LoopbackURL(port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

// Send posts env to the ingest route at path and reports an error unless the
// server takes it with a 2xx answer. The context bounds the whole exchange.
func (c Client) Send(ctx context.Context, path string, env protocol.Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("encode envelope: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, string(c.Token))

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("post envelope: %w", err)
	}
	defer closeAnswer(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("post envelope to %s: the server answered %s", path, resp.Status)
	}
	return nil
}

// Get fetches path with the token and returns the answer, which must be a
// 200 of at most limit bytes; any other status is a *StatusError. The context
// bounds the whole exchange.
func (c Client) Get(ctx context.Context, path string, limit int64) ([]byte, error) {
	return c.call(ctx, http.MethodGet, path, limit)
}

// Post posts to path with the token and no body, and returns the answer,
// which must be a 200 of at most limit bytes. The context bounds the whole
// exchange.
func (c Client) Post(ctx context.Context, path string, limit int64) ([]byte, error) {
	return c.call(ctx, http.MethodPost, path, limit)
}

func (c Client) call(ctx context.Context, method, path string, limit int64) ([]byte, error) {
	request := strings.ToLower(method) + " " + path
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set(tokenHeader, string(c.Token))

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", request, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Method: method, Path: path, Code: resp.StatusCode, Status: resp.Status}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", request, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s: the answer is over %d bytes", request, limit)
	}
	return body, nil
}

// Healthy reports whether a Loadline server answers at BaseURL. It gives up
// after HealthTimeout, or sooner when ctx ends, and anything but a 200 whose
// body, within its first 4 KiB, names the app counts as no.
func (c Client) Healthy(ctx context.Context) bool {
	_, ok := c.ServerVersion(ctx)
	return ok
}

// ServerVersion returns the version the Loadline server at BaseURL names in
// its health answer, and whether one answers, as Healthy finds it.
func (c Client) ServerVersion(ctx context.Context) (string, bool) {
	timeout := c.HealthTimeout
	if timeout <= 0 {
		timeout = defaultHealthTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+healthPath, http.NoBody)
	if err != nil {
		return "", false
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return "", false
	}
	var health struct {
		App     string `json:"app"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(answer, &health); err != nil || health.App != appName {
		return "", false
	}
	return health.Version, true
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return direct
}

// closeAnswer reads what is left of a short answer, so that its connection
// can carry the next request, and closes it.
func closeAnswer(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
	_ = resp.Body.Close()
}
