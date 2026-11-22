package config_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
)

func ensureToken(t *testing.T, path string) []byte {
	t.Helper()

	token, err := config.EnsureToken(path)
	if err != nil {
		t.Fatalf("EnsureToken(%s): %v", path, err)
	}
	return token
}

func TestEnsureToken_CreatesRandomHexToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")

	token := ensureToken(t, path)

	raw, err := hex.DecodeString(string(token))
	if err != nil {
		t.Fatalf("token %q is not hex: %v", token, err)
	}
	if len(raw) != 32 {
		t.Errorf("token carries %d random bytes, want 32", len(raw))
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if !bytes.Equal(onDisk, token) {
		t.Errorf("token file holds %q, EnsureToken returned %q", onDisk, token)
	}
}

func TestEnsureToken_StableAcrossCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")

	first := ensureToken(t, path)
	second := ensureToken(t, path)

	if !bytes.Equal(first, second) {
		t.Errorf("second call returned %q, want the first token %q", second, first)
	}
}

func TestEnsureToken_DifferentFilesGetDifferentTokens(t *testing.T) {
	dir := t.TempDir()

	// Each in a data directory of its own, which holds nothing but
	// Loadline's.
	a := ensureToken(t, filepath.Join(dir, "a", "token"))
	b := ensureToken(t, filepath.Join(dir, "b", "token"))

	if bytes.Equal(a, b) {
		t.Errorf("two new token files got the same token %q", a)
	}
}

func TestEnsureToken_CreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "loadline", "token")

	ensureToken(t, path)

	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		t.Fatalf("parent directory was not created: %v", err)
	}
}

func TestEnsureToken_FileModeUserOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file mode bits do not express user-only access")
	}
	path := filepath.Join(t.TempDir(), "state", "token")

	ensureToken(t, path)

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("token file mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat token directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("token directory mode = %o, want 700", got)
	}
}

func TestEnsureToken_LeavesOnlyTheTokenFile(t *testing.T) {
	dir := t.TempDir()

	ensureToken(t, filepath.Join(dir, "token"))

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read token directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "token" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("directory holds %v, want only [token]", names)
	}
}

func TestEnsureToken_ToleratesTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	const token = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write token fixture: %v", err)
	}

	if got := ensureToken(t, path); string(got) != token {
		t.Errorf("EnsureToken = %q, want %q", got, token)
	}
}

func TestEnsureToken_CorruptFileIsAnErrorAndStaysUntouched(t *testing.T) {
	for name, contents := range map[string]string{
		"empty":        "",
		"too short":    "00112233",
		"not hex":      "zz112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
		"odd length":   "00112233445566778899aabbccddeeff00112233445566778899aabbccddeef",
		"too long":     "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff00",
		"two tokens":   "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff\n00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
		"binary noise": "\x00\x01\x02\x03",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("write token fixture: %v", err)
			}

			if token, err := config.EnsureToken(path); err == nil {
				t.Fatalf("EnsureToken accepted a corrupt token file and returned %q", token)
			}

			onDisk, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read token file: %v", err)
			}
			if string(onDisk) != contents {
				t.Errorf("corrupt token file was rewritten to %q", onDisk)
			}
		})
	}
}

func TestEnsureToken_UnreadablePathIsAnError(t *testing.T) {
	if _, err := config.EnsureToken(t.TempDir()); err == nil {
		t.Fatal("EnsureToken accepted a directory as the token file")
	}
}

func TestEnsureToken_UncreatableDirectoryIsAnError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := config.EnsureToken(filepath.Join(blocker, "state", "token")); err == nil {
		t.Fatal("EnsureToken succeeded with a regular file where the directory belongs")
	}
}

func TestEnsureToken_ConcurrentCallersAgreeOnOneToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "token")
	const callers = 16

	tokens := make([][]byte, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tokens[i], errs[i] = config.EnsureToken(path)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	for i, token := range tokens {
		if !bytes.Equal(token, tokens[0]) {
			t.Errorf("caller %d got %q, caller 0 got %q", i, token, tokens[0])
		}
	}
	if onDisk, err := os.ReadFile(path); err != nil || !bytes.Equal(onDisk, tokens[0]) {
		t.Errorf("token file holds %q (err %v), callers agreed on %q", onDisk, err, tokens[0])
	}
}

func TestReadToken_ReadsWithoutCreating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")

	if _, err := config.ReadToken(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadToken of a missing file: err = %v, want fs.ErrNotExist", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadToken created %s", path)
	}

	created := ensureToken(t, path)
	got, err := config.ReadToken(path)
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}
	if !bytes.Equal(got, created) {
		t.Errorf("ReadToken = %q, want %q", got, created)
	}
}

func TestReadToken_RejectsWhatEnsureTokenRejects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("not a token"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := config.ReadToken(path); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadToken of a corrupt file: err = %v, want a format error", err)
	}
}
