package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/clocktest"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/domain"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/store"
	"github.com/dsj7419/loadline/internal/tracker"
)

var restartT0 = time.Date(2026, time.November, 21, 18, 0, 0, 0, time.UTC)

// heldLoad holds the history's Load until release is closed, and closes
// loading as Load begins.
type heldLoad struct {
	historyDB
	loading, release chan struct{}
}

func (h heldLoad) Load(ctx context.Context, since time.Time) (restored tracker.Restored, skipped int, err error) {
	close(h.loading)
	select {
	case <-h.release:
	case <-ctx.Done():
	}
	return h.historyDB.Load(ctx, since)
}

// listingClaude lists one busy session for every config dir.
type listingClaude struct{ agents string }

func (c listingClaude) Run(_ context.Context, _ domain.ConfigDir, args ...string) ([]byte, error) {
	switch strings.Join(args, " ") {
	case "agents --json --all":
		return []byte(c.agents), nil
	case "auth status --json":
		return []byte(`{"loggedIn":true,"authMethod":"claude.ai","email":"dev@example.com"}`), nil
	default:
		return nil, fmt.Errorf("unexpected claude %q", args)
	}
}

// storedSession is what an earlier run kept of s1: working, with two
// subagents told apart by ID, and two state changes.
func storedSession(t *testing.T, path string, dir domain.ConfigDir) {
	t.Helper()
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("open the history: %v", err)
	}
	defer func() { _ = db.Close() }()
	s := domain.NewSession("s1", dir, restartT0.Add(-time.Hour))
	s.PID = 4242
	if err := db.UpsertSession(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	s.State, s.StateAt, s.LastSeenAt, s.Subagents = domain.StateWorking, restartT0.Add(-10*time.Minute), restartT0.Add(-time.Minute), 2
	s = s.WithSubagentIDs(domain.SubagentIDs{Running: []string{"agent-1", "agent-2"}})
	if err := db.UpsertSession(t.Context(), s); err != nil {
		t.Fatal(err)
	}
}

// An event that arrives while the history is being restored would make its
// session look new, and the restored copy, its subagents and its timeline
// would be lost and then overwritten. Ingest waits for the restore; the
// health check does not.
func TestServe_TakesNoEventUntilTheHistoryIsRestored(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	data := filepath.Join(dir, "data")
	paths := config.Paths{
		DataDir: data, ConfigFile: filepath.Join(dir, "config.json"), TokenFile: filepath.Join(data, "token"),
		SpoolDir: filepath.Join(data, "spool"), DBFile: filepath.Join(data, "loadline.db"),
	}
	account := config.ResolveConfigDir("", home)
	if err := os.MkdirAll(string(account), 0o700); err != nil {
		t.Fatal(err)
	}
	storedSession(t, paths.DBFile, account)
	token, err := config.EnsureToken(paths.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listening on %v, want a TCP address", listener.Addr())
	}
	port := addr.Port
	clock := clocktest.New(restartT0)
	runner := listingClaude{agents: `[{"sessionId":"s1","kind":"interactive","pid":4242,"status":"busy","cwd":"/work"}]`}
	s := newInstance(t.Context(), ServeOptions{Paths: paths, Home: home, NoOpen: true, Runner: runner, Clock: clock.Now, Ticker: clock},
		config.Defaults(), port, token, slog.New(slog.DiscardHandler))
	if s.history == nil {
		t.Fatal("the history did not open")
	}
	held := heldLoad{historyDB: s.history.db, loading: make(chan struct{}), release: make(chan struct{})}
	s.history.db = held

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- s.run(ctx, listener) }()
	<-held.loading

	client := forward.Client{BaseURL: forward.LoopbackURL(port), Token: token}
	if !client.Healthy(t.Context()) {
		t.Error("the server is not healthy while it restores")
	}
	statusLine := protocol.Envelope{
		V: protocol.SchemaVersion, ID: "0000000000000001", Kind: protocol.KindStatus, At: restartT0, ConfigDir: string(account),
		PID: 4242, Payload: json.RawMessage(`{"session_id":"s1","model":{"id":"claude-opus-5-5","display_name":"Opus 5.5"}}`),
	}
	if err := client.Send(t.Context(), forward.StatusPath, statusLine); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("a status line during the restore: %v, want it refused with 503", err)
	}

	close(held.release)
	deadline := time.Now().Add(5 * time.Second)
	statusLine.ID = "0000000000000002"
	for client.Send(t.Context(), forward.StatusPath, statusLine) != nil {
		if time.Now().After(deadline) {
			t.Fatal("ingest did not open once the history was restored")
		}
		time.Sleep(5 * time.Millisecond)
	}

	var seen tracker.SessionView
	for _, v := range s.tracker.Snapshot().Sessions {
		if v.ID == "s1" {
			seen = v
		}
	}
	wantTimeline := []tracker.TransitionView{
		{At: restartT0.Add(-time.Hour), State: domain.StateStarting},
		{At: restartT0.Add(-10 * time.Minute), State: domain.StateWorking},
	}
	if seen.Subagents != 2 || seen.Model != "Opus 5.5" || !reflect.DeepEqual(seen.Timeline, wantTimeline) {
		t.Errorf("s1 in the view %+v, want its 2 subagents, the status line's model and the restored timeline %v", seen, wantTimeline)
	}

	cancel()
	if code := <-done; code != exitOK {
		t.Errorf("run = %d, want %d", code, exitOK)
	}
	db, err := store.Open(t.Context(), paths.DBFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	kept, _, err := db.Load(t.Context(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(kept.Sessions) != 1 {
		t.Fatalf("the history holds sessions %+v, want s1", kept.Sessions)
	}
	ids := kept.Sessions[0].SubagentIDs()
	if kept.Sessions[0].Subagents != 2 || !reflect.DeepEqual(ids.Running, []string{"agent-1", "agent-2"}) || len(kept.Timelines["s1"]) != 2 {
		t.Errorf("the history keeps s1 with %d subagents, IDs %+v and timeline %+v; want 2, agent-1 and agent-2, and the two changes",
			kept.Sessions[0].Subagents, ids, kept.Timelines["s1"])
	}
}
