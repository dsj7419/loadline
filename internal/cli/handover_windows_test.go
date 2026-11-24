package cli_test

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/dsj7419/loadline/internal/cli"
)

// The old server ran the binary install moved aside. Once its process is
// gone, install removes that binary, trying again while something, such as
// a scanner, still holds it for a moment.
func TestInstall_RemovesTheBinaryTheOldServerRanOnceItIsLetGo(t *testing.T) {
	home, dir, fake := handOverHome(t)
	aside := asideBinary(t, home)
	held, err := os.Open(aside)
	if err != nil {
		t.Fatal(err)
	}
	var letGo sync.Once
	release := func() { letGo.Do(func() { _ = held.Close() }) }
	t.Cleanup(release)
	tries := 0
	cli.AfterAsideRemoval(t, func() {
		tries++
		release()
	})
	cli.SetServerSpawn(t, func(string, ...string) error {
		fakeAt(t, fake.port)
		return nil
	})

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 0 || got.stderr != "" {
		t.Fatalf("install = %d, stderr %q; want 0 and nothing", got.code, got.stderr)
	}
	if want := removedLine(aside) + restartNotice + "\n"; !strings.HasSuffix(got.stdout, want) {
		t.Errorf("stdout does not end with %q:\n%s", want, got.stdout)
	}
	if _, err := os.Lstat(aside); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s is still there (%v)", aside, err)
	}
	if tries < 2 {
		t.Errorf("install tried %d times to remove what an upgrade moved aside, want it to try again once the hold ended", tries)
	}
}

// A binary that stays held, as by a scanner that does not let go, is tried
// again for the half second install gives it, with pauses that double from
// 1 ms up to 50 ms: no more than 15 tries fit in it, however fast the
// machine, where a loop that did not pause makes hundreds.
func TestInstall_PausesBetweenTriesAtABinaryThatStaysHeld(t *testing.T) {
	home, dir, fake := handOverHome(t)
	aside := asideBinary(t, home)
	held, err := os.Open(aside)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	tries := 0
	cli.AfterAsideRemoval(t, func() { tries++ })
	cli.SetServerSpawn(t, func(string, ...string) error {
		fakeAt(t, fake.port)
		return nil
	})

	got := run(t, "install", "--config-dir", dir, "--yes")

	if got.code != 0 || got.stderr != "" {
		t.Fatalf("install = %d, stderr %q; want 0 and nothing", got.code, got.stderr)
	}
	if tries < 2 || tries > 15 {
		t.Errorf("install tried %d times to remove a binary that stayed held, want it to try again, pausing between tries: from 2 to 15 times", tries)
	}
	if _, err := os.Lstat(aside); err != nil {
		t.Errorf("%s, which stayed held, is gone (%v); want it left for a later run", aside, err)
	}
}

// Install tries again only once it has stopped the server that may have run
// what an upgrade moved aside. With a server it could not ask, or none, a
// binary still held stays held, and one try is all it takes.
func TestInstall_TriesOnceToRemoveAsideBinariesWhenNoServerStopped(t *testing.T) {
	for name, setUp := range map[string]func(t *testing.T) (home, dir string){
		"no server": func(t *testing.T) (string, string) {
			t.Helper()
			cli.SetElevated(t, false)
			return sandbox(t, ownerSettings)
		},
		"a server that cannot be asked": func(t *testing.T) (string, string) {
			t.Helper()
			home, dir, _ := handOverHome(t, earlier(http.StatusMethodNotAllowed))
			return home, dir
		},
	} {
		t.Run(name, func(t *testing.T) {
			home, dir := setUp(t)
			noStarts(t)
			aside := asideBinary(t, home)
			held, err := os.Open(aside)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = held.Close() })
			tries := 0
			cli.AfterAsideRemoval(t, func() { tries++ })

			got := run(t, "install", "--config-dir", dir, "--yes")

			if got.code != 0 || got.stderr != "" {
				t.Fatalf("install = %d, stderr %q; want 0 and nothing", got.code, got.stderr)
			}
			if tries != 1 {
				t.Errorf("install tried %d times to remove what an upgrade moved aside, want once", tries)
			}
		})
	}
}
