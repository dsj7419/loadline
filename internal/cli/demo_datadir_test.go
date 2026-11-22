package cli_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/cli"
	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/fsperm"
)

// A demo holds no real data, takes no events, installs no binary, and no
// hook runs anything from its data directory. So it runs from a folder that
// others could move aside, as a temporary folder can be, and says in its log
// that it let that pass. A real server refuses the same folder, and the demo
// still makes its data directory private.
func TestServe_DemoRunsFromADataDirOthersCouldMoveAside(t *testing.T) {
	f := newServeFixture(t)
	movable := movableFolder(t)
	data := filepath.Join(movable, "data")
	f.paths = config.Paths{
		DataDir:    data,
		ConfigFile: f.paths.ConfigFile,
		TokenFile:  filepath.Join(data, "token"),
		SpoolDir:   filepath.Join(data, "spool"),
		DBFile:     filepath.Join(data, "loadline.db"),
	}
	var stderr strings.Builder
	// Bounded, so that a server that wrongly starts gives way to the check.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if code := cli.Serve(ctx, f.options(), cli.IO{In: strings.NewReader(""), Out: io.Discard, Err: &stderr}); code != 1 ||
		!strings.Contains(stderr.String(), movable+" lets ") {
		t.Fatalf("Serve = %d, stderr %q; want 1 and %s named as a folder others can move aside", code, stderr.String(), movable)
	}

	f.start(func(o *cli.ServeOptions) { o.Demo = true }).stop(t)

	relaxed := ""
	for line := range strings.Lines(f.logText()) {
		if strings.Contains(line, `msg="data directory check relaxed for the demo"`) {
			relaxed = line
		}
	}
	// The log quotes the folder, as a Go string.
	quoted := strings.Trim(strconv.Quote(movable), `"`)
	if !strings.Contains(relaxed, "level=WARN") || !strings.Contains(relaxed, quoted) {
		t.Errorf("log %q, want a warning that the demo let %s pass", f.logText(), movable)
	}
	if others, err := fsperm.Exposure(data); err != nil || others != "" {
		t.Errorf("the demo's data directory admits %q (%v), want it private", others, err)
	}
}

// The demo lets only that check pass: a data directory that holds what is
// not Loadline's is still refused, since making it private would take it
// from its owner.
func TestServe_DemoStillRefusesADataDirThatHoldsOtherThings(t *testing.T) {
	f := newServeFixture(t)
	// The token is there already, so it is the server's own check that
	// finds the file, not the token's creation.
	f.token()
	if err := os.WriteFile(filepath.Join(f.paths.DataDir, "notes.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := f.options()
	o.Demo = true
	var stderr strings.Builder
	// Bounded, so that a demo that wrongly starts gives way to the check.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	code := cli.Serve(ctx, o, cli.IO{In: strings.NewReader(""), Out: io.Discard, Err: &stderr})

	if code != 1 || !strings.Contains(stderr.String(), "notes.txt, which is not Loadline's") {
		t.Errorf("demo Serve = %d, stderr %q; want 1 and the folder refused for notes.txt", code, stderr.String())
	}
}
