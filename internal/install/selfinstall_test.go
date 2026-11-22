package install_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/install"
)

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "loadline.exe"
	}
	return "loadline"
}

func writeBinary(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "source-binary")
	writeFile(t, path, []byte(content))
	return path
}

func selfInstall(t *testing.T, src, dataDir string) string {
	t.Helper()

	got, err := install.SelfInstall(src, dataDir)
	if err != nil {
		t.Fatalf("SelfInstall: %v", err)
	}
	return got
}

func TestSelfInstall_CopiesTheBinary(t *testing.T) {
	dataDir := t.TempDir()
	src := writeBinary(t, "loadline v1")

	got := selfInstall(t, src, dataDir)

	target := filepath.Join(dataDir, "bin", binaryName())
	if want := filepath.ToSlash(target); got != want {
		t.Errorf("SelfInstall = %s, want %s", got, want)
	}
	if strings.Contains(got, `\`) {
		t.Errorf("SelfInstall = %s, want forward slashes", got)
	}
	if content := readFile(t, target); string(content) != "loadline v1" {
		t.Errorf("installed binary holds %q", content)
	}
	if entries := dirEntries(t, filepath.Dir(target)); !slices.Equal(entries, []string{binaryName()}) {
		t.Errorf("bin dir holds %q, want only the binary", entries)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Errorf("installed binary has mode %o, want it executable", info.Mode().Perm())
		}
	}
}

func TestSelfInstall_InstalledPathNamesTheTarget(t *testing.T) {
	dataDir := t.TempDir()

	planned := install.InstalledPath(dataDir)
	got := selfInstall(t, writeBinary(t, "loadline"), dataDir)

	if planned != got {
		t.Errorf("InstalledPath = %s, SelfInstall = %s, want them equal", planned, got)
	}
}

func TestSelfInstall_SkipsAnIdenticalTarget(t *testing.T) {
	dataDir := t.TempDir()
	src := writeBinary(t, "loadline v1")
	target := filepath.Join(dataDir, "bin", binaryName())
	selfInstall(t, src, dataDir)
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatal(err)
	}

	selfInstall(t, src, dataDir)

	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("target was rewritten at %s although it was identical", info.ModTime())
	}
}

func TestSelfInstall_ReplacesAnOlderBinary(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "bin", binaryName())
	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)

	selfInstall(t, writeBinary(t, "loadline v2, which is longer"), dataDir)

	if content := readFile(t, target); string(content) != "loadline v2, which is longer" {
		t.Errorf("installed binary holds %q, want v2", content)
	}
	if entries := dirEntries(t, filepath.Dir(target)); !slices.Equal(entries, []string{binaryName()}) {
		t.Errorf("bin dir holds %q, want only the binary", entries)
	}
}

func TestSelfInstall_ReplacesABinaryOfTheSameSize(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "bin", binaryName())
	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)

	selfInstall(t, writeBinary(t, "loadline v2"), dataDir)

	if content := readFile(t, target); !bytes.Equal(content, []byte("loadline v2")) {
		t.Errorf("installed binary holds %q, want v2", content)
	}
}

func TestPlanSelfInstall(t *testing.T) {
	dataDir := t.TempDir()
	src := writeBinary(t, "loadline v2")
	target := filepath.Join(dataDir, "bin", binaryName())
	path := "bin/" + binaryName()

	missing, err := install.PlanSelfInstall(src, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0].Path != path || missing[0].Action != install.ActionAdd || !strings.Contains(missing[0].Detail, src) {
		t.Errorf("without a binary: %+v, want add %s naming the source", missing, path)
	}

	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)
	older, err := install.PlanSelfInstall(src, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(older) != 1 || older[0].Path != path || older[0].Action != install.ActionUpdate {
		t.Errorf("with an older binary: %+v, want update %s", older, path)
	}

	selfInstall(t, src, dataDir)
	current, err := install.PlanSelfInstall(src, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 0 {
		t.Errorf("with the same binary: %+v, want no change", current)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("planning removed the binary: %v", err)
	}

	if _, err := install.PlanSelfInstall(filepath.Join(t.TempDir(), "missing"), dataDir); err == nil {
		t.Error("PlanSelfInstall succeeded without a source binary")
	}
}

func TestSelfInstall_MissingSourceFails(t *testing.T) {
	dataDir := t.TempDir()

	if _, err := install.SelfInstall(filepath.Join(t.TempDir(), "missing"), dataDir); err == nil {
		t.Fatal("SelfInstall succeeded without a source binary")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "bin", binaryName())); err == nil {
		t.Error("SelfInstall left a target behind")
	}
}

// The binary's folder must be what the data directory's check takes for
// Loadline's own.
func TestInstalledPath_TheDataDirectoryOwnsItsFolder(t *testing.T) {
	bin := filepath.Base(filepath.Dir(filepath.FromSlash(install.InstalledPath(t.TempDir()))))

	if !datadir.Owns(bin) {
		t.Errorf("datadir.Owns(%q) = false, want true", bin)
	}
}

// The copy of the binary belongs to its install until it is moved in, and
// the preparation of the data directory, which other installs and the
// server run meanwhile, must know it for one.
func TestSelfInstall_WritesFirstToAFileTheDataDirectoryLeavesToItsWriter(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(dataDir, "bin", binaryName())
	var temps []string
	install.SetRename(t, func(from, to string) error {
		if to == target {
			temps = append(temps, from)
		}
		return os.Rename(from, to)
	})

	selfInstall(t, writeBinary(t, "loadline v1"), dataDir)

	if len(temps) != 1 || filepath.Dir(temps[0]) != filepath.Dir(target) || !datadir.Temporary(filepath.Base(temps[0])) {
		t.Errorf("SelfInstall wrote the binary first to %q, want one file beside %s that datadir.Temporary takes", temps, target)
	}
}
