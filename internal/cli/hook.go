package cli

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/protocol"
)

// hookSendTimeout bounds the hook's post to the server. Hooks run
// asynchronously, so this bounds only how long the hook process lives.
const hookSendTimeout = 300 * time.Millisecond

// sessionStart is the hook event that starts the server when none runs.
const sessionStart = "SessionStart"

// runHookCommand runs the hook command for this process.
func runHookCommand(ctx context.Context, stdio IO) int {
	paths, err := config.DefaultPaths()
	if err != nil {
		return exitOK
	}
	env := processEnv()
	return RunHook(ctx, stdio.In, env, paths, loadConfig(paths, env.Home))
}

// RunHook forwards the Claude Code hook event on in to the server and
// returns 0, whatever happens. It writes nothing anywhere, since Claude Code
// reads what a hook prints, and every failure is silent, since a hook must
// not disturb the session it reports on.
//
// Only the allowlisted fields of the event, protocol.HookInput, leave the
// process. On SessionStart it starts the server if none answers. An event
// the server does not take is spooled, under the event ID it was sent with,
// for the server to drain later; one the server could never take, such as
// an event without a session, is dropped.
func RunHook(ctx context.Context, in io.Reader, env Env, paths config.Paths, cfg config.Config) int {
	input, err := protocol.ReadHookInput(in)
	if err != nil {
		return exitOK
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return exitOK
	}
	envelope, err := newEnvelope(env, protocol.KindHook, payload)
	if err != nil {
		return exitOK
	}
	if _, err := protocol.HookEvent(envelope, input); err != nil {
		return exitOK
	}

	client := forward.Client{BaseURL: forward.LoopbackURL(cfg.Port)}
	if input.HookEventName == sessionStart && !client.Healthy(ctx) {
		_ = env.Spawn(env.Exe, "serve")
	}

	spool := forward.Spool{Dir: paths.SpoolDir, MaxFiles: forward.DefaultMaxFiles}
	token, err := config.EnsureToken(paths.TokenFile)
	if err != nil {
		_ = spool.Put(envelope)
		return exitOK
	}
	client.Token = token
	ctx, cancel := context.WithTimeout(ctx, hookSendTimeout)
	defer cancel()
	if err := client.Send(ctx, forward.HookPath, envelope); err != nil {
		_ = spool.Put(envelope)
	}
	return exitOK
}
