package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/launch"
	"github.com/dsj7419/loadline/internal/timer"
)

const (
	loginCodePath = "/api/login-code"
	// serverStartWait bounds the wait for a server that open started to
	// answer, and serverPollEvery spaces the checks.
	serverStartWait = 3 * time.Second
	serverPollEvery = 50 * time.Millisecond
	// loginCodeTimeout bounds the request for a login code.
	loginCodeTimeout = 5 * time.Second
	// maxLoginCodeAnswer is far more than the server's answer, a code of 32
	// hex digits in a small JSON object.
	maxLoginCodeAnswer = 1 << 10
	// loginCodeLength is the length of a login code a Loadline server mints.
	loginCodeLength = 32
)

// OpenOptions is what OpenDashboard needs.
type OpenOptions struct {
	Paths config.Paths
	// Home is the user's home directory, which an account's configDir in
	// the config may start from.
	Home string
	// Exe is this program's executable, which Spawn runs to start the
	// server.
	Exe string
	// Spawn starts the server. Nil means launch.SpawnDetached.
	Spawn func(exe string, args ...string) error
	// Open opens the dashboard's URL in a browser. Nil means launch.Open.
	Open func(url string, b launch.Browser) error
	// After schedules the wait for a server that is starting. Nil means
	// wall-clock timers.
	After timer.AfterFunc
}

// runOpenCommand runs the open command for this process. It takes no
// arguments.
func runOpenCommand(ctx context.Context, args []string, stdio IO) int {
	if code, ok := parseFlags(newFlagSet("open", stdio), args); !ok {
		return code
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stdio, err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(stdio, fmt.Errorf("find this program's executable: %w", err))
	}
	// Without a home directory, Load refuses only an account that names its
	// config dir from home, and says so.
	home, _ := os.UserHomeDir()
	return OpenDashboard(ctx, OpenOptions{Paths: paths, Home: home, Exe: exe}, stdio)
}

// OpenDashboard opens the dashboard in the configured browser, signed in by
// a login code the server mints for it, and returns 0, or 1 with the reason
// on stderr. When no server answers, it starts one first, with --no-open so
// that the server does not open a window of its own, and waits up to 3 s
// for it to answer.
func OpenDashboard(ctx context.Context, o OpenOptions, stdio IO) int {
	cfg, err := config.Load(o.Paths.ConfigFile, o.Home)
	if err != nil {
		return fail(stdio, err)
	}
	client := forward.Client{BaseURL: forward.LoopbackURL(cfg.Port)}
	if !client.Healthy(ctx) {
		start := serverStart{exe: o.Exe, spawn: o.Spawn, after: o.After, wait: serverStartWait}
		if err := startServer(ctx, client, cfg.Port, start); err != nil {
			return fail(stdio, fmt.Errorf("%w; run loadline serve to see why", err))
		}
	}
	if client.Token, err = config.EnsureToken(o.Paths.TokenFile); err != nil {
		return fail(stdio, err)
	}
	// Before the login code is asked for, so that none of the minute it is
	// valid for goes on making the profile ready.
	browser, err := dashboardBrowser(cfg, o.Paths)
	if err != nil {
		_, _ = fmt.Fprintf(stdio.Err, "loadline: warning: opening the dashboard in the browser's default profile: %v\n", err)
	}
	code, err := loginCode(ctx, client, cfg.Port)
	if err != nil {
		return fail(stdio, err)
	}

	open := o.Open
	if open == nil {
		open = launch.Open
	}
	loginURL := client.BaseURL + "/login?code=" + url.QueryEscape(code)
	if err := open(loginURL, browser); err != nil {
		return fail(stdio, fmt.Errorf("open the dashboard: %w", err))
	}
	return exitOK
}

// serverStart is what starting a server takes.
type serverStart struct {
	// exe is the binary the server runs.
	exe string
	// spawn starts it. Nil means launch.SpawnDetached.
	spawn func(exe string, args ...string) error
	// after schedules the wait for it to answer. Nil means wall-clock timers.
	after timer.AfterFunc
	// wait bounds that wait.
	wait time.Duration
}

// startServer starts the server, with --no-open so that it does not open a
// window of its own, and waits until client finds it answering on port, for
// up to start.wait. What to run when it fails is the caller's to say.
func startServer(ctx context.Context, client forward.Client, port int, start serverStart) error {
	spawn := start.spawn
	if spawn == nil {
		spawn = launch.SpawnDetached
	}
	if err := spawn(start.exe, "serve", "--no-open"); err != nil {
		return fmt.Errorf("start the server: %w", err)
	}

	wait, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := start.after.Start(start.wait, cancel)
	defer stop()
	for !client.Healthy(wait) {
		if start.after.Sleep(wait, serverPollEvery) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for the server: %w", err)
		}
		return fmt.Errorf("the server started on port %d did not answer within %v", port, start.wait)
	}
	return nil
}

// loginCode asks the server on port for a login code, and takes only one of
// the form a Loadline server mints (isLoginCode).
func loginCode(ctx context.Context, client forward.Client, port int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, loginCodeTimeout)
	defer cancel()
	body, err := client.Post(ctx, loginCodePath, maxLoginCodeAnswer)
	if err != nil {
		return "", fmt.Errorf("get a login code: %w", err)
	}
	var answer struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return "", fmt.Errorf("get a login code: read the server's answer: %w", err)
	}
	if !isLoginCode(answer.Code) {
		return "", fmt.Errorf("get a login code: the server on port %d did not answer as Loadline does, "+
			"so the dashboard was not opened; run loadline doctor to check the server", port)
	}
	return answer.Code, nil
}

// isLoginCode reports whether code has the form a Loadline server mints its
// login codes in: 16 random bytes in lower-case hex. The code goes into the
// URL the browser opens, and a batch file given as the browser gets that URL
// through cmd.exe, which expands a %NAME% in it, inside quotes too. Whatever
// answers on the port chooses the code, and a code whose escapes spelled a
// variable's name would put its value in the URL, for the browser to send
// to whatever answers; a hex code escapes to itself.
func isLoginCode(code string) bool {
	if len(code) != loginCodeLength {
		return false
	}
	for _, c := range []byte(code) {
		digit, letter := '0' <= c && c <= '9', 'a' <= c && c <= 'f'
		if !digit && !letter {
			return false
		}
	}
	return true
}
