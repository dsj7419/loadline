package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/fsperm"
)

// localAppData is a folder that stands for %LOCALAPPDATA% on a standard
// profile: the user's alone, with every folder above it trusted.
func localAppData(t *testing.T) string {
	t.Helper()
	local := filepath.Join(t.TempDir(), "AppData", "Local")
	if err := fsperm.MkdirAll(local); err != nil {
		t.Fatal(err)
	}
	return local
}

// holdOpen opens path to read it, sharing it for reading and writing but
// not for deletion, as a virus scanner does, until the test ends or the
// returned func lets go.
func holdOpen(t *testing.T, path string) (letGo func()) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatalf("hold %s: %v", path, err)
	}
	var once sync.Once
	letGo = func() { once.Do(func() { _ = windows.CloseHandle(handle) }) }
	t.Cleanup(letGo)
	return letGo
}

// stillWorks checks that a token left with its own temporary name beside it
// is the token still: it is read, install's check and Secure take the data
// directory, Secure removes the temporary name, and doctor finds nothing.
func stillWorks(t *testing.T, local, dir string, token []byte) {
	t.Helper()
	path := filepath.Join(dir, "token")
	if temps, err := filepath.Glob(filepath.Join(dir, ".token-*")); err != nil || len(temps) != 1 {
		t.Fatalf("the fixture left %v (%v), want the token's one temporary name", temps, err)
	}
	if again, err := config.EnsureToken(path); err != nil || !bytes.Equal(again, token) {
		t.Errorf("EnsureToken again = %q, %v; want the token %q", again, err, token)
	}
	if err := datadir.Verify(dir, local); err != nil {
		t.Errorf("Verify, install's first check: %v", err)
	}
	if temps, _ := filepath.Glob(filepath.Join(dir, ".token-*")); len(temps) != 1 {
		t.Errorf("Verify, which changes nothing, left %v", temps)
	}
	if err := datadir.Secure(dir, local); err != nil {
		t.Errorf("Secure, which install and every server start run: %v", err)
	}
	if temps, _ := filepath.Glob(filepath.Join(dir, ".token-*")); len(temps) != 0 {
		t.Errorf("Secure left %v, the token's temporary name", temps)
	}
	if problem, err := datadir.Check(dir, local); err != nil || problem != "" {
		t.Errorf("Check, doctor's data directory line = %q, %v; want nothing", problem, err)
	}
}

// The removal fails: something holds the temporary file without sharing it
// for deletion at the moment EnsureToken is done with it.
func TestEnsureToken_LeavesAUsableTokenWhenItCannotRemoveTheTemporaryName(t *testing.T) {
	local := localAppData(t)
	dir := filepath.Join(local, "loadline")
	var letGo func()
	config.SetBeforePublish(t, func(temp string) { letGo = holdOpen(t, temp) })

	token, err := config.EnsureToken(filepath.Join(dir, "token"))
	if letGo != nil {
		letGo()
	}
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}

	stillWorks(t, local, dir, token)
}

// The process is ended between linking the token into place and removing
// the temporary name, as Claude Code ends hooks: the token is left under
// two names.
func TestEnsureToken_LeavesAUsableTokenWhenItsCreatorEndsBeforeItTidiesUp(t *testing.T) {
	local := localAppData(t)
	dir := filepath.Join(local, "loadline")
	path := filepath.Join(dir, "token")
	token, err := config.EnsureToken(path)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}
	if err := os.Link(path, filepath.Join(dir, ".token-1234567890")); err != nil {
		t.Fatal(err)
	}

	stillWorks(t, local, dir, token)
}

// A temporary name that cannot be removed right now, since something still
// holds it, stays; Secure goes on, and doctor finds nothing.
func TestSecure_GoesOnWhenItCannotRemoveTheTokensTemporaryName(t *testing.T) {
	local := localAppData(t)
	dir := filepath.Join(local, "loadline")
	path := filepath.Join(dir, "token")
	if _, err := config.EnsureToken(path); err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}
	leftover := filepath.Join(dir, ".token-1234567890")
	if err := os.Link(path, leftover); err != nil {
		t.Fatal(err)
	}
	holdOpen(t, leftover)

	if err := datadir.Secure(dir, local); err != nil {
		t.Errorf("Secure = %v, want it to go on", err)
	}
	if _, err := os.Lstat(leftover); err != nil {
		t.Errorf("%s, held, is gone (%v), so the test proves nothing", leftover, err)
	}
	if problem, err := datadir.Check(dir, local); err != nil || problem != "" {
		t.Errorf("Check = %q, %v; want nothing", problem, err)
	}
}

// heldWhileNew opens each file in dir to read it, sharing it for reading
// and writing but not for deletion, and holds it for hold, again and again
// until stop closes, as a virus scanner or a backup agent does with each new
// file.
func heldWhileNew(dir string, hold time.Duration, stop <-chan struct{}) {
	for {
		entries, _ := os.ReadDir(dir)
		var handles []windows.Handle
		for _, entry := range entries {
			name, err := windows.UTF16PtrFromString(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
				windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
			if err == nil {
				handles = append(handles, handle)
			}
		}
		time.Sleep(hold)
		for _, handle := range handles {
			_ = windows.CloseHandle(handle)
		}
		select {
		case <-stop:
			return
		default:
		}
	}
}

// Several first callers at once while two such readers hold every new file:
// each gets the one token, and install, every server start and doctor then
// take the data directory, whatever temporary names were left behind.
func TestEnsureToken_FirstCallersWhileAReaderHoldsNewFiles(t *testing.T) {
	top := t.TempDir()
	dir := filepath.Join(top, "state")
	path := filepath.Join(dir, "token")
	const callers = 16
	tokens := make([][]byte, callers)
	errs := make([]error, callers)
	start, stop := make(chan struct{}), make(chan struct{})
	var wg, readers sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			<-start
			tokens[i], errs[i] = config.EnsureToken(path)
		})
	}
	for _, hold := range []time.Duration{3 * time.Millisecond, 5 * time.Millisecond} {
		readers.Go(func() {
			<-start
			heldWhileNew(dir, hold, stop)
		})
	}
	close(start)
	wg.Wait()
	close(stop)
	readers.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
		if !bytes.Equal(tokens[i], tokens[0]) {
			t.Errorf("caller %d got %q, caller 0 got %q", i, tokens[i], tokens[0])
		}
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".token-*"))
	if err := datadir.Secure(dir, top); err != nil {
		t.Errorf("with %d temporary names left behind, Secure = %v", len(left), err)
	}
	if problem, err := datadir.Check(dir, top); err != nil || problem != "" {
		t.Errorf("with %d temporary names left behind, Check = %q, %v", len(left), problem, err)
	}
}
