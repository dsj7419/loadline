package datadir_test

// What Loadline itself leaves in its data directory in ordinary use counts
// as private, so that Secure writes nothing to it: every list it writes on a
// folder Windows passes down to all the folder holds, the files other
// Loadline processes are writing among them.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
	"github.com/dsj7419/loadline/internal/install"
	"github.com/dsj7419/loadline/internal/logfile"
	"github.com/dsj7419/loadline/internal/protocol"
	"github.com/dsj7419/loadline/internal/store"
)

// usedHome returns a data directory after Loadline has been at work in it:
// a token, an install and an upgrade, spooled events, one of them set aside
// as undecodable, a log that has rotated, and a history opened and closed.
func usedHome(t *testing.T) (home, top string) {
	t.Helper()
	top = privateTop(t)
	home = filepath.Join(top, "loadline")
	if _, err := config.EnsureToken(filepath.Join(home, "token")); err != nil {
		t.Fatal(err)
	}
	for _, binary := range []string{"loadline v1", "loadline v2, longer"} {
		src := filepath.Join(top, "download.exe")
		if err := os.WriteFile(src, []byte(binary), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := install.SelfInstall(src, home); err != nil {
			t.Fatalf("install %s: %v", binary, err)
		}
	}
	spool := forward.Spool{Dir: filepath.Join(home, "spool")}
	for i := range 3 {
		event := hookEvent(t)
		event.At = time.Unix(1_700_000_000, int64(i))
		if err := spool.Put(event); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(spool.Dir, "5-0000000a.json"))
	if _, err := spool.Drain(func(event protocol.Envelope) error {
		if event.At.Nanosecond() == 0 {
			return nil
		}
		return context.Canceled
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("the drain = %v, want it to stop at the second event", err)
	}
	logs, err := logfile.Open(filepath.Join(home, "loadline.log"), 64, 2)
	if err != nil {
		t.Fatal(err)
	}
	for range 6 {
		if _, err := logs.Write([]byte(strings.Repeat("x", 40) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := logs.Close(); err != nil {
		t.Fatal(err)
	}
	history, err := store.Open(t.Context(), filepath.Join(home, "loadline.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := history.Close(); err != nil {
		t.Fatal(err)
	}
	return home, top
}

// accessLists returns the access list of every path in dir.
func accessLists(t *testing.T, dir string) map[string]string {
	t.Helper()
	lists := map[string]string{}
	if err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		lists[path] = fspermtest.AccessList(t, path)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return lists
}

func TestSecure_WritesNothingToWhatLoadlineItselfLeaves(t *testing.T) {
	home, top := usedHome(t)
	before := accessLists(t, home)
	if len(before) < 10 {
		t.Fatalf("the data directory holds only %d paths, so the test proves little: %v", len(before), before)
	}
	for path, list := range before {
		if path == home {
			continue
		}
		if private, err := fsperm.EntryPrivate(path); err != nil || !private {
			t.Errorf("%s, which Loadline left, does not count as private (%v): %s", path, err, list)
		}
	}

	for range 2 {
		if err := datadir.Secure(home, top); err != nil {
			t.Fatalf("Secure: %v", err)
		}
	}

	for path, list := range accessLists(t, home) {
		if list != before[path] {
			t.Errorf("Secure rewrote the access list of %s from %s to %s", path, before[path], list)
		}
	}
	if got, err := datadir.Check(home, top); err != nil || got != "" {
		t.Errorf("Check = %q, %v; want \"\"", got, err)
	}
}
