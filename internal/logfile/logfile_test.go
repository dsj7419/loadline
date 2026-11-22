package logfile_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/dsj7419/loadline/internal/logfile"
)

func open(t *testing.T, path string, maxBytes int64, keep int) *logfile.File {
	t.Helper()
	f, err := logfile.Open(path, maxBytes, keep)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func write(t *testing.T, f *logfile.File, text string) {
	t.Helper()
	n, err := f.Write([]byte(text))
	if err != nil || n != len(text) {
		t.Fatalf("Write(%q) = %d, %v; want %d, nil", text, n, err, len(text))
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s exists (stat error %v), want it gone", filepath.Base(path), err)
	}
}

func TestFile_CreatesItsDirectoryAndAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "loadline.log")
	f := open(t, path, 1<<20, 2)

	write(t, f, "first\n")
	write(t, f, "second\n")

	if got := read(t, path); got != "first\nsecond\n" {
		t.Errorf("log holds %q, want both lines", got)
	}
}

func TestFile_KeepsWhatAnEarlierRunWrote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loadline.log")
	if err := os.WriteFile(path, []byte("earlier run\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f := open(t, path, 1<<20, 2)

	write(t, f, "this run\n")

	if got := read(t, path); got != "earlier run\nthis run\n" {
		t.Errorf("log holds %q, want the earlier run's line kept", got)
	}
}

func TestFile_RotatesPastTheLimitKeepingTwoOldFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loadline.log")
	f := open(t, path, 20, 2)

	// Each line is 9 bytes, so a file holds two.
	for i := range 7 {
		write(t, f, fmt.Sprintf("line %03d\n", i))
	}

	if got, want := read(t, path), "line 006\n"; got != want {
		t.Errorf("current file holds %q, want %q", got, want)
	}
	if got, want := read(t, path+".1"), "line 004\nline 005\n"; got != want {
		t.Errorf("%s.1 holds %q, want %q", filepath.Base(path), got, want)
	}
	if got, want := read(t, path+".2"), "line 002\nline 003\n"; got != want {
		t.Errorf("%s.2 holds %q, want %q", filepath.Base(path), got, want)
	}
	assertMissing(t, path+".3")
}

func TestFile_KeepsAsManyOldFilesAsAsked(t *testing.T) {
	for keep, want := range map[int][]string{
		0: {"line 4\n"},
		1: {"line 4\n", "line 3\n"},
	} {
		t.Run(fmt.Sprintf("keep %d", keep), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "loadline.log")
			f := open(t, path, 7, keep)
			for i := range 5 {
				write(t, f, fmt.Sprintf("line %d\n", i))
			}

			for i, text := range want {
				name := path
				if i > 0 {
					name = fmt.Sprintf("%s.%d", path, i)
				}
				if got := read(t, name); got != text {
					t.Errorf("%s holds %q, want %q", filepath.Base(name), got, text)
				}
			}
			assertMissing(t, fmt.Sprintf("%s.%d", path, len(want)))
		})
	}
}

func TestFile_RotationCountsWhatAnEarlierRunWrote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loadline.log")
	if err := os.WriteFile(path, []byte("earlier run\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f := open(t, path, 16, 2)

	write(t, f, "this run\n")

	if got := read(t, path+".1"); got != "earlier run\n" {
		t.Errorf("%s.1 holds %q, want the earlier run's file", filepath.Base(path), got)
	}
	if got := read(t, path); got != "this run\n" {
		t.Errorf("current file holds %q, want the new line alone", got)
	}
}

func TestFile_WritesARecordLongerThanTheLimitWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loadline.log")
	f := open(t, path, 8, 2)
	long := strings.Repeat("x", 30) + "\n"

	write(t, f, "short\n")
	write(t, f, long)
	write(t, f, "after\n")

	if got := read(t, path+".1"); got != long {
		t.Errorf("%s.1 holds %q, want the long record whole", filepath.Base(path), got)
	}
	if got := read(t, path); got != "after\n" {
		t.Errorf("current file holds %q, want %q", got, "after\n")
	}
}

func TestFile_ConcurrentWritesKeepEveryRecordWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loadline.log")
	const writers, lines = 16, 200
	f := open(t, path, 1<<20, 2)

	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range lines {
				if _, err := fmt.Fprintf(f, "writer %02d line %03d\n", w, i); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	got := strings.Split(strings.TrimSuffix(read(t, path), "\n"), "\n")
	if len(got) != writers*lines {
		t.Fatalf("log holds %d lines, want %d", len(got), writers*lines)
	}
	for _, line := range got {
		var w, i int
		if n, err := fmt.Sscanf(line, "writer %02d line %03d", &w, &i); n != 2 || err != nil {
			t.Fatalf("line %q is torn", line)
		}
	}
}

func TestFile_ConcurrentWritesAcrossRotations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loadline.log")
	f := open(t, path, 4<<10, 2)
	record := strings.Repeat("r", 99) + "\n"

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				if _, err := f.Write([]byte(record)); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	for _, name := range []string{path, path + ".1", path + ".2"} {
		data := read(t, name)
		if len(data) > 4<<10 || len(data)%len(record) != 0 || strings.Count(data, record) != len(data)/len(record) {
			t.Errorf("%s holds %d bytes, want whole records within 4 KiB", filepath.Base(name), len(data))
		}
	}
	assertMissing(t, path+".3")
}

func TestFile_WriteAfterCloseFails(t *testing.T) {
	f, err := logfile.Open(filepath.Join(t.TempDir(), "loadline.log"), 1<<20, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := f.Write([]byte("late\n")); !errors.Is(err, os.ErrClosed) {
		t.Errorf("Write after Close = %v, want os.ErrClosed", err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}

func TestFile_IsUserOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not keep Unix permission bits")
	}
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "loadline.log")
	f := open(t, path, 10, 2)
	write(t, f, "0123456789\n")
	write(t, f, "rotated\n")

	for name, want := range map[string]fs.FileMode{dir: 0o700, path: 0o600, path + ".1": 0o600} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", filepath.Base(name), got, want)
		}
	}
}

func TestOpen_RefusesALimitOfNothing(t *testing.T) {
	for _, tc := range []struct {
		maxBytes int64
		keep     int
	}{{0, 2}, {-1, 2}, {1 << 20, -1}} {
		path := filepath.Join(t.TempDir(), "loadline.log")
		if _, err := logfile.Open(path, tc.maxBytes, tc.keep); err == nil {
			t.Errorf("Open with %d bytes keeping %d accepted it", tc.maxBytes, tc.keep)
		}
		assertMissing(t, path)
	}
}

func TestOpen_FailsWhenThePathIsADirectory(t *testing.T) {
	path := t.TempDir()

	if _, err := logfile.Open(path, 1<<20, 2); err == nil || !strings.Contains(err.Error(), "log file") {
		t.Errorf("Open of a directory = %v, want an error about the log file", err)
	}
}
