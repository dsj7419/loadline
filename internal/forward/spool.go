package forward

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsj7419/loadline/internal/protocol"
)

const (
	// DefaultMaxFiles is how many events a spool keeps when its MaxFiles is
	// not set.
	DefaultMaxFiles = 5000

	spoolExt   = ".json"
	badDirName = "bad"
	// nameSuffixBytes is the randomness in a file name that keeps two events
	// stamped in the same nanosecond apart.
	nameSuffixBytes = 4
	// maxSpoolFile is the largest file a drain reads. An envelope is its
	// payload, capped at protocol.MaxPayloadBytes, and a few hundred bytes.
	maxSpoolFile = protocol.MaxPayloadBytes + 4<<10

	// A put writes its event to a temporary file named tempPrefix, digits
	// and tempSuffix before it publishes it under its final name.
	tempPrefix = ".put-"
	tempSuffix = ".tmp"
	// orphanAge is how old a put's temporary file must be to count as left
	// behind by a put that never finished. A put takes milliseconds.
	orphanAge = time.Hour
)

var (
	// ErrUndeliverable is what a Drain function wraps to say that it can
	// never take an event, as opposed to not taking it yet.
	ErrUndeliverable = errors.New("the event can never be delivered")

	// errUndecodable marks a spooled file that holds no valid envelope.
	errUndecodable = errors.New("not a valid envelope")

	// testHookBeforePublish, when a test sets it, runs once a put's file is
	// written and before it is published under its final name.
	testHookBeforePublish func()
)

// Spool keeps the hook events the server did not take, one file per event,
// until a drain delivers them. Every hook is its own process, so several may
// put to one spool at once, and a drain may run meanwhile: a file appears
// whole under its final name or not at all.
type Spool struct {
	Dir string
	// MaxFiles caps the events kept; a put beyond it deletes the oldest.
	// Zero means DefaultMaxFiles.
	MaxFiles int
}

// spooled is a spool file, named <unixnano>-<8 hex>.json after its event's
// time.
type spooled struct {
	name   string
	at     int64
	suffix string
}

// Put stores env, then deletes the oldest events while the spool holds more
// than MaxFiles. An error means env was not stored; trimming is best effort,
// since another process may be deleting or reading the same files.
func (s Spool) Put(env protocol.Envelope) error {
	if err := env.Validate(); err != nil {
		return fmt.Errorf("spool event: %w", err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("spool event: encode: %w", err)
	}
	suffix := make([]byte, nameSuffixBytes)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("spool event: name: %w", err)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("spool event: create spool directory: %w", err)
	}
	// An event time before 1970, which only a broken clock gives, sorts
	// first rather than making a name that no drain would read.
	stamp := max(env.At.UnixNano(), 0)
	name := strconv.FormatInt(stamp, 10) + "-" + hex.EncodeToString(suffix) + spoolExt
	if err := writeFile(s.Dir, name, data); err != nil {
		return fmt.Errorf("spool event: %w", err)
	}
	s.trim()
	return nil
}

// writeFile writes data to a new user-only file in dir and renames it to
// name, so that a drain never sees the file half written.
func writeFile(dir, name string, data []byte) error {
	file, err := os.CreateTemp(dir, tempPrefix+"*"+tempSuffix)
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		_ = os.Remove(file.Name())
		return fmt.Errorf("write temporary file: %w", err)
	}
	if testHookBeforePublish != nil {
		testHookBeforePublish()
	}
	if err := os.Rename(file.Name(), filepath.Join(dir, name)); err != nil {
		_ = os.Remove(file.Name())
		return fmt.Errorf("publish spool file: %w", err)
	}
	return nil
}

// trim deletes the oldest events beyond the cap, and the temporary files
// that puts left behind. A file it cannot delete was taken by a drain or
// another trim, or is open in another process, which Windows refuses to
// delete; the next put tries again.
func (s Spool) trim() {
	files, orphans, err := s.scan()
	if err != nil {
		return
	}
	s.remove(orphans)
	limit := s.MaxFiles
	if limit <= 0 {
		limit = DefaultMaxFiles
	}
	for _, f := range files[:max(len(files)-limit, 0)] {
		_ = os.Remove(filepath.Join(s.Dir, f.name))
	}
}

// Drain hands the spooled events to fn, oldest first, and deletes each one
// fn takes. It stops at the first error from fn and leaves that event and
// the ones after it for the next drain, unless the error wraps
// ErrUndeliverable. Such an event, like a file that holds no valid envelope,
// is moved to the bad directory, out of the way, and the drain goes on.
// Drain also deletes the temporary files that puts left behind. It returns
// how many events fn took.
func (s Spool) Drain(fn func(protocol.Envelope) error) (int, error) {
	files, orphans, err := s.scan()
	if err != nil {
		return 0, err
	}
	s.remove(orphans)
	drained := 0
	var quarantineErrs []error
	for _, f := range files {
		path := filepath.Join(s.Dir, f.name)
		env, err := readEnvelope(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// Another drain or a trim took it.
			continue
		case errors.Is(err, errUndecodable):
			if err := s.quarantine(f.name); err != nil {
				quarantineErrs = append(quarantineErrs, err)
			}
			continue
		case err != nil:
			return drained, errors.Join(append(quarantineErrs, err)...)
		}

		if err := fn(env); err != nil {
			if errors.Is(err, ErrUndeliverable) {
				if err := s.quarantine(f.name); err != nil {
					quarantineErrs = append(quarantineErrs, err)
				}
				continue
			}
			return drained, errors.Join(append(quarantineErrs, fmt.Errorf("deliver spooled event %s: %w", f.name, err))...)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			// Delivered but still spooled: the next drain sends it again,
			// under the same event ID, which the server recognizes.
			return drained, errors.Join(append(quarantineErrs, fmt.Errorf("remove delivered event: %w", err))...)
		}
		drained++
	}
	return drained, errors.Join(quarantineErrs...)
}

// scan returns the spool's event files, oldest first, and the names of the
// temporary files that puts left behind: a hook killed between writing its
// event and publishing it leaves one, as Claude Code does to the hooks still
// running when a `claude -p` session ends. Other files, such as a put's
// temporary file while the put runs and the bad directory, are neither.
func (s Spool) scan() (files []spooled, orphans []string, err error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list spool: %w", err)
	}
	files = make([]spooled, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		if isOrphan(entry) {
			orphans = append(orphans, entry.Name())
			continue
		}
		if f, ok := parseName(entry.Name()); ok {
			files = append(files, f)
		}
	}
	slices.SortFunc(files, func(a, b spooled) int {
		return cmp.Or(cmp.Compare(a.at, b.at), strings.Compare(a.suffix, b.suffix))
	})
	return files, orphans, nil
}

// isOrphan reports whether entry is a put's temporary file older than any
// put that is still running could have made.
func isOrphan(entry fs.DirEntry) bool {
	name := entry.Name()
	if !strings.HasPrefix(name, tempPrefix) || !strings.HasSuffix(name, tempSuffix) {
		return false
	}
	info, err := entry.Info()
	return err == nil && time.Since(info.ModTime()) > orphanAge
}

// remove deletes the named files, as far as it can: one that another process
// holds open, or that a drain or a trim took first, is left to the next try.
func (s Spool) remove(names []string) {
	for _, name := range names {
		_ = os.Remove(filepath.Join(s.Dir, name))
	}
}

func parseName(name string) (spooled, bool) {
	base, ok := strings.CutSuffix(name, spoolExt)
	if !ok {
		return spooled{}, false
	}
	stamp, suffix, ok := strings.Cut(base, "-")
	if !ok || len(suffix) != 2*nameSuffixBytes || !isLowerHex(suffix) {
		return spooled{}, false
	}
	at, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || at < 0 {
		return spooled{}, false
	}
	return spooled{name: name, at: at, suffix: suffix}, true
}

func isLowerHex(s string) bool {
	for i := range len(s) {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// readEnvelope reads the envelope stored at path. A file that is too large,
// is not JSON or fails the envelope's checks is errUndecodable.
func readEnvelope(path string) (protocol.Envelope, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("open spooled event: %w", err)
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maxSpoolFile+1))
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("read spooled event: %w", err)
	}
	if len(data) > maxSpoolFile {
		return protocol.Envelope{}, fmt.Errorf("spooled event is over %d bytes: %w", maxSpoolFile, errUndecodable)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return protocol.Envelope{}, fmt.Errorf("decode spooled event: %w: %w", errUndecodable, err)
	}
	if err := env.Validate(); err != nil {
		return protocol.Envelope{}, fmt.Errorf("check spooled event: %w: %w", errUndecodable, err)
	}
	return env, nil
}

// quarantine moves a spool file into the bad directory.
func (s Spool) quarantine(name string) error {
	bad := filepath.Join(s.Dir, badDirName)
	if err := os.MkdirAll(bad, 0o700); err != nil {
		return fmt.Errorf("create %s directory: %w", badDirName, err)
	}
	err := os.Rename(filepath.Join(s.Dir, name), filepath.Join(bad, name))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("move undecodable spool file %s aside: %w", name, err)
	}
	return nil
}
