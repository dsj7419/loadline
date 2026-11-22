package launch_test

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/dsj7419/loadline/internal/launch"
)

func TestSpawnDetached_StartsTheProgramAndReturnsAtOnce(t *testing.T) {
	c := expectChild(t, true)
	args := []string{"serve", "--no-open", "a & b"}

	started := time.Now()
	err := launch.SpawnDetached(testBinary(t), args...)
	took := time.Since(started)

	if err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}
	// The program waits to be released, so a SpawnDetached that waited for
	// it would have taken the whole hold limit.
	if took > holdLimit/3 {
		t.Errorf("SpawnDetached took %v, want it to return while the program runs", took)
	}
	report := c.read(t)
	if c.exited() {
		t.Error("the program exited before it was released")
	}
	if !slices.Equal(report.Args, args) {
		t.Errorf("the program got arguments %q, want %q", report.Args, args)
	}
	assertInHome(t, report)
	if !report.Detached {
		t.Error("the program is attached to its spawner's console or session, want it detached")
	}
	if report.Hidden != wantServerHidden {
		t.Errorf("the program started hidden = %v, want %v", report.Hidden, wantServerHidden)
	}
}

// A hook's standard streams are pipes that Claude Code reads to their end.
// A server that kept them would hold the hook open for as long as it runs.
func TestSpawnDetached_KeepsNoneOfTheSpawnersStreams(t *testing.T) {
	c := expectChild(t, true)
	t.Setenv(helperEnv, asSpawner)
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readEnd.Close() })
	spawner := exec.CommandContext(t.Context(), testBinary(t)) //nolint:gosec // G204: the test binary itself
	spawner.Stdout, spawner.Stderr = writeEnd, writeEnd
	if err := spawner.Start(); err != nil {
		t.Fatalf("start the spawner: %v", err)
	}
	// exec watches the test's context until the spawner is waited for, so a
	// test that fails before it waits must still wait, or the watch outlives
	// the test.
	wait := sync.OnceValue(spawner.Wait)
	t.Cleanup(func() { _ = wait() })
	_ = writeEnd.Close()
	output := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(readEnd)
		output <- data
	}()

	select {
	case got := <-output:
		if string(got) != "spawned\n" {
			t.Errorf("the spawner wrote %q, want it to report the spawn", got)
		}
	case <-time.After(holdLimit / 3):
		t.Fatal("the spawner's output stayed open after it exited: the program it spawned holds it")
	}
	if err := wait(); err != nil {
		t.Errorf("the spawner failed: %v", err)
	}
	c.read(t)
	if c.exited() {
		t.Error("the spawned program exited before it was released, so the test proves nothing")
	}
}

func TestSpawnDetached_FailsForAMissingProgram(t *testing.T) {
	missing := testBinary(t) + "-missing"

	if err := launch.SpawnDetached(missing); err == nil {
		t.Error("SpawnDetached of a missing program succeeded, want an error")
	}
}

func TestOpen_StartsTheBrowserWithTheURLAsOneArgument(t *testing.T) {
	c := expectChild(t, false)

	if err := c.open(dashboardURL, launch.Browser{Pref: testBinary(t), AppWindow: true}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	report := c.read(t)
	if want := []string{"--app=" + dashboardURL}; !slices.Equal(report.Args, want) {
		t.Errorf("the browser got arguments %q, want %q", report.Args, want)
	}
	assertInHome(t, report)
	if report.Hidden {
		t.Error("the browser was told to start hidden, want its window shown")
	}
	// On Windows a browser is a windowed program, which uses no console, so
	// whether it shares one says nothing; on Unix it leads a session of its
	// own, so that closing the terminal that ran loadline open spares it.
	if runtime.GOOS != "windows" && !report.Detached {
		t.Error("the browser shares its caller's session, want one of its own")
	}
}

func TestOpen_RefusesAnUnknownBrowser(t *testing.T) {
	if err := launch.Open(dashboardURL, launch.Browser{Pref: "opera"}); err == nil {
		t.Error("Open with an unknown browser succeeded, want an error")
	}
}

// A browser may run on for a while after it has said it is exiting, as a
// race-enabled binary on a loaded machine does, so a test's end waits on
// reaped rather than on the browser saying so. That holds only if reaped is
// closed once the reaper has waited for the browser, and never before.
func TestOpenBrowser_ClosesReapedOnlyOnceTheBrowserHasExited(t *testing.T) {
	// Registered first, so that it runs last, once the child's own cleanup
	// has waited on reaped. This browser lingers past goleak's retries, so a
	// reaper still waiting for it is found.
	t.Cleanup(func() { goleak.VerifyNone(t) })
	c := expectChild(t, false)
	t.Setenv(lingerEnv, "1")

	if err := c.openBrowser(dashboardURL, launch.Browser{Pref: testBinary(t)}); err != nil {
		t.Fatalf("OpenBrowser: %v", err)
	}

	c.read(t)
	c.waitExit(t)
	// The browser runs on for lingerAfterExit once it has said it is
	// exiting, so nothing can have reaped it yet: a reaped already closed
	// was closed without waiting for it.
	select {
	case <-c.reaped:
		t.Fatalf("reaped was closed while %s still runs", c.process())
	default:
	}
}
