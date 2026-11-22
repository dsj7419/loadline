package cli

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/launch"
	"github.com/dsj7419/loadline/internal/protocol"
)

// Env is what the hook and tap commands take from their process: its
// environment and clock, its own executable, and a way to start the server.
type Env struct {
	Getenv func(string) string
	// Home is the user's home directory, which holds Claude Code's default
	// configuration directory.
	Home string
	Now  func() time.Time
	// Exe is this program's executable, which Spawn runs to start the
	// server.
	Exe   string
	Spawn func(exe string, args ...string) error
}

// processEnv returns the Env of this process. A home directory or an
// executable that cannot be found is left empty: the config dir then
// resolves against the working directory, and starting the server fails,
// which costs nothing the next event cannot make up.
func processEnv() Env {
	home, _ := os.UserHomeDir()
	exe, _ := os.Executable()
	return Env{Getenv: os.Getenv, Home: home, Now: time.Now, Exe: exe, Spawn: launch.SpawnDetached}
}

// newEnvelope wraps payload for the server under a fresh event ID, stamped
// with the time, the Claude Code config dir and Claude Code's process ID. It
// refuses to build an envelope the server would refuse.
func newEnvelope(env Env, kind protocol.Kind, payload []byte) (protocol.Envelope, error) {
	id, err := protocol.NewEventID()
	if err != nil {
		return protocol.Envelope{}, err
	}
	envelope := protocol.Envelope{
		V:         protocol.SchemaVersion,
		ID:        id,
		Kind:      kind,
		At:        env.Now(),
		ConfigDir: string(config.ResolveConfigDir(env.Getenv("CLAUDE_CONFIG_DIR"), env.Home)),
		PID:       claudePID(env.Getenv),
		Payload:   payload,
	}
	if err := envelope.Validate(); err != nil {
		return protocol.Envelope{}, fmt.Errorf("build envelope: %w", err)
	}
	return envelope, nil
}

// claudePID reads CLAUDE_PID, which Claude Code sets to its own process ID in
// the processes it starts. Anything but a positive number means no PID.
func claudePID(getenv func(string) string) int {
	pid, err := strconv.Atoi(getenv("CLAUDE_PID"))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// loadConfig reads the config file, or gives the defaults when it cannot: a
// broken config file must not cost Claude Code its hooks or status line.
func loadConfig(paths config.Paths, home string) config.Config {
	cfg, err := config.Load(paths.ConfigFile, home)
	if err != nil {
		return config.Defaults()
	}
	return cfg
}
