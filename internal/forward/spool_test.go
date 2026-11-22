package forward_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/protocol"
)

var spoolName = regexp.MustCompile(`^[0-9]+-[0-9a-f]{8}\.json$`)

func newSpool(t *testing.T, maxFiles int) forward.Spool {
	t.Helper()
	return forward.Spool{Dir: filepath.Join(t.TempDir(), "spool"), MaxFiles: maxFiles}
}

func put(t *testing.T, s forward.Spool, env protocol.Envelope) {
	t.Helper()
	if err := s.Put(env); err != nil {
		t.Fatalf("Put(%s): %v", env.ID, err)
	}
}

// names returns the names of the regular files in dir, sorted.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			files = append(files, e.Name())
		}
	}
	return files
}

// drainIDs drains s and returns the IDs delivered, in order.
func drainIDs(t *testing.T, s forward.Spool) []string {
	t.Helper()
	var ids []string
	n, err := s.Drain(func(env protocol.Envelope) error {
		ids = append(ids, env.ID)
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if n != len(ids) {
		t.Errorf("Drain reported %d events, delivered %d", n, len(ids))
	}
	return ids
}

func id(i int) string { return fmt.Sprintf("%016x", i) }

func TestSpool_PutWritesOneFilePerEvent(t *testing.T) {
	s := newSpool(t, 0)
	env := envelopeAt(1, t0)

	put(t, s, env)
	put(t, s, envelopeAt(2, t0.Add(time.Second)))

	files := names(t, s.Dir)
	if len(files) != 2 {
		t.Fatalf("spool holds %v, want two files", files)
	}
	for _, name := range files {
		if !spoolName.MatchString(name) {
			t.Errorf("file name %q is not <unixnano>-<8 hex>.json", name)
		}
	}
	var got []protocol.Envelope
	if _, err := s.Drain(func(e protocol.Envelope) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(got) != 2 || got[0].ID != env.ID || !got[0].At.Equal(env.At) || got[0].ConfigDir != env.ConfigDir ||
		got[0].PID != env.PID || string(got[0].Payload) != string(env.Payload) {
		t.Errorf("drained %+v, want the envelopes put, the first %+v", got, env)
	}
}

func TestSpool_NamesFilesByTheEventTime(t *testing.T) {
	s := newSpool(t, 0)
	at := time.Unix(1795400000, 123456789)

	put(t, s, envelopeAt(1, at))

	files := names(t, s.Dir)
	if len(files) != 1 || !strings.HasPrefix(files[0], "1795400000123456789-") {
		t.Errorf("spool holds %v, want one file named after %d", files, at.UnixNano())
	}
}

func TestSpool_FilesAreUserOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not keep Unix permission bits")
	}
	s := newSpool(t, 0)
	put(t, s, envelopeAt(1, t0))

	dir, err := os.Stat(s.Dir)
	if err != nil {
		t.Fatalf("stat spool dir: %v", err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("spool dir mode = %o, want 700", perm)
	}
	file, err := os.Stat(filepath.Join(s.Dir, names(t, s.Dir)[0]))
	if err != nil {
		t.Fatalf("stat spool file: %v", err)
	}
	if perm := file.Mode().Perm(); perm != 0o600 {
		t.Errorf("spool file mode = %o, want 600", perm)
	}
}

func TestSpool_PutRefusesAnInvalidEnvelope(t *testing.T) {
	s := newSpool(t, 0)
	env := envelopeAt(1, t0)
	env.ID = "not-an-id"

	if err := s.Put(env); err == nil {
		t.Error("Put accepted an envelope with a bad ID, want an error")
	}
	if files := names(t, s.Dir); len(files) != 0 {
		t.Errorf("spool holds %v, want nothing", files)
	}
}

func TestSpool_PutFailsWhenTheDirectoryCannotBeMade(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	s := forward.Spool{Dir: filepath.Join(blocker, "spool")}

	if err := s.Put(envelopeAt(1, t0)); err == nil {
		t.Error("Put succeeded under a regular file, want an error")
	}
}

func TestSpool_DrainOldestFirstAndCap(t *testing.T) {
	s := newSpool(t, 3)
	// Put out of time order: the drain follows the events' times.
	for _, i := range []int{3, 1, 5, 2, 4} {
		put(t, s, envelopeAt(i, t0.Add(time.Duration(i)*time.Second)))
	}

	if files := names(t, s.Dir); len(files) != 3 {
		t.Fatalf("spool holds %d files over a cap of 3, want 3", len(files))
	}
	if got, want := drainIDs(t, s), []string{id(3), id(4), id(5)}; !slices.Equal(got, want) {
		t.Errorf("drained %v, want the three newest oldest first %v", got, want)
	}
	if files := names(t, s.Dir); len(files) != 0 {
		t.Errorf("spool holds %v after draining, want nothing", files)
	}
}

func TestSpool_DefaultCapIs5000(t *testing.T) {
	if forward.DefaultMaxFiles != 5000 {
		t.Errorf("DefaultMaxFiles = %d, want 5000", forward.DefaultMaxFiles)
	}
}

func TestSpool_DrainStopsAtTheFirstFailure(t *testing.T) {
	s := newSpool(t, 0)
	for i := 1; i <= 4; i++ {
		put(t, s, envelopeAt(i, t0.Add(time.Duration(i)*time.Second)))
	}
	refused := errors.New("server unavailable")
	var tried []string

	n, err := s.Drain(func(env protocol.Envelope) error {
		tried = append(tried, env.ID)
		if env.ID == id(2) {
			return refused
		}
		return nil
	})

	if n != 1 || !errors.Is(err, refused) {
		t.Errorf("Drain = %d, %v; want 1 and the delivery error", n, err)
	}
	if want := []string{id(1), id(2)}; !slices.Equal(tried, want) {
		t.Errorf("tried %v, want %v and then a stop", tried, want)
	}
	if got, want := drainIDs(t, s), []string{id(2), id(3), id(4)}; !slices.Equal(got, want) {
		t.Errorf("next drain delivered %v, want the rest %v", got, want)
	}
}

func TestSpool_CorruptFileMovedToBad(t *testing.T) {
	s := newSpool(t, 0)
	put(t, s, envelopeAt(1, t0.Add(time.Second)))
	put(t, s, envelopeAt(5, t0.Add(5*time.Second)))
	invalid := envelopeAt(3, t0)
	invalid.V = protocol.SchemaVersion + 1
	invalidJSON, err := json.Marshal(invalid)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	corrupt := map[string][]byte{
		"2000000000000000000-0000000a.json": []byte(`{"v":1,"id":`),
		"2000000000000000001-0000000b.json": invalidJSON,
		"2000000000000000002-0000000c.json": []byte(`[]`),
		"2000000000000000003-0000000d.json": append([]byte(`{"v":1,"pad":"`), make([]byte, 2<<20)...),
	}
	for name, data := range corrupt {
		if err := os.WriteFile(filepath.Join(s.Dir, name), data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if got, want := drainIDs(t, s), []string{id(1), id(5)}; !slices.Equal(got, want) {
		t.Errorf("drained %v, want the good events %v", got, want)
	}
	if files := names(t, s.Dir); len(files) != 0 {
		t.Errorf("spool holds %v after draining, want nothing", files)
	}
	bad := names(t, filepath.Join(s.Dir, "bad"))
	if want := slices.Sorted(maps.Keys(corrupt)); !slices.Equal(bad, want) {
		t.Errorf("bad/ holds %v, want the corrupt files %v", bad, want)
	}
}

func TestSpool_DrainIgnoresOtherFiles(t *testing.T) {
	s := newSpool(t, 0)
	put(t, s, envelopeAt(1, t0))
	for _, name := range []string{".put-123.tmp", "notes.txt", "12-zz.json", "12-0000000a.json.tmp"} {
		if err := os.WriteFile(filepath.Join(s.Dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if got := drainIDs(t, s); !slices.Equal(got, []string{id(1)}) {
		t.Errorf("drained %v, want only the spooled event", got)
	}
	if files := names(t, s.Dir); len(files) != 4 {
		t.Errorf("spool holds %v, want the four foreign files left alone", files)
	}
}

func TestSpool_DrainOfAMissingSpoolIsEmpty(t *testing.T) {
	s := newSpool(t, 0)

	n, err := s.Drain(func(protocol.Envelope) error {
		t.Error("delivered an event from a spool that does not exist")
		return nil
	})

	if n != 0 || err != nil {
		t.Errorf("Drain = %d, %v; want 0, nil", n, err)
	}
}

// Hooks run as separate processes, so puts race; each must land whole.
func TestSpool_ConcurrentPutsKeepEveryEvent(t *testing.T) {
	s := newSpool(t, 0)
	const writers = 64
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Go(func() { errs[i] = s.Put(envelopeAt(i, t0)) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got := drainIDs(t, s)
	if len(got) != writers {
		t.Fatalf("drained %d events, want %d", len(got), writers)
	}
	slices.Sort(got)
	if got = slices.Compact(got); len(got) != writers {
		t.Errorf("drained %d distinct events, want %d", len(got), writers)
	}
	if bad := names(t, filepath.Join(s.Dir, "bad")); len(bad) != 0 {
		t.Errorf("bad/ holds %v, want nothing", bad)
	}
}
