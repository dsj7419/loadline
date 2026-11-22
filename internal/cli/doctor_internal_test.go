package cli

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/config"
	"github.com/dsj7419/loadline/internal/doctor"
	"github.com/dsj7419/loadline/internal/forward"
	"github.com/dsj7419/loadline/internal/install"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDoctorConfig_FallsBackOnABadFile(t *testing.T) {
	path := writeConfig(t, `{"theme": "dark"}`)
	_, loadErr := config.Load(path, "")
	if loadErr == nil {
		t.Fatal("the config loads; want it refused")
	}

	cfg, checks := doctorConfig(path, "")

	if want := config.Defaults(); !reflect.DeepEqual(cfg, want) {
		t.Errorf("config = %+v, want the defaults %+v", cfg, want)
	}
	want := []doctor.Check{{
		Name:   "config",
		Status: doctor.Fail,
		Detail: loadErr.Error() + "; the checks below use the defaults",
	}}
	if !slices.Equal(checks, want) {
		t.Errorf("checks = %+v, want %+v", checks, want)
	}
}

func TestDoctorConfig_UsesAGoodFileOrTheDefaultsWithoutOne(t *testing.T) {
	loaded := config.Defaults()
	loaded.Port = 9123
	tests := []struct {
		name string
		path string
		want config.Config
	}{
		{name: "a good file", path: writeConfig(t, `{"port": 9123}`), want: loaded},
		{name: "no file", path: filepath.Join(t.TempDir(), "config.json"), want: config.Defaults()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, checks := doctorConfig(tt.path, "")

			if !reflect.DeepEqual(cfg, tt.want) || len(checks) != 0 {
				t.Errorf("doctorConfig = %+v, %+v; want %+v and no check", cfg, checks, tt.want)
			}
		})
	}
}

// closedPort returns a loopback port that nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %v is not TCP", l.Addr())
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr.Port
}

// setDoctorFallback replaces the config the doctor falls back to for the
// rest of the test.
func setDoctorFallback(t *testing.T, cfg config.Config) {
	t.Helper()
	previous := doctorFallback
	doctorFallback = func() config.Config { return cfg }
	t.Cleanup(func() { doctorFallback = previous })
}

func TestDoctor_ABadConfigFailsAndTheOtherChecksUseTheFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOADLINE_HOME", home)
	configFile := filepath.Join(home, "config.json")
	if err := os.WriteFile(configFile, []byte(`{"theme": "dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, loadErr := config.Load(configFile, "")
	if loadErr == nil {
		t.Fatal("the config loads; want it refused")
	}
	// The defaults with a port nothing listens on and a claude that is not
	// there, so the run touches no real server and no real claude.
	fallback := config.Defaults()
	fallback.Port = closedPort(t)
	fallback.ClaudePath = filepath.Join(home, "no-claude")
	setDoctorFallback(t, fallback)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	code := Run(t.Context(), []string{"doctor", "--config-dir", dir}, IO{In: strings.NewReader(""), Out: &stdout, Err: &stderr})

	url := forward.LoopbackURL(fallback.Port)
	want := "" +
		"✖ config: " + loadErr.Error() + "; the checks below use the defaults\n" +
		"✖ claude: not found; install Claude Code, or set claudePath in the Loadline config\n" +
		"✖ token: " + filepath.Join(home, "token") + " does not exist; run loadline install\n" +
		"✖ settings " + filepath.Join(dir, "settings.json") + ": not wired: the file does not exist; run loadline install\n" +
		"✖ binary: " + filepath.FromSlash(install.InstalledPath(home)) + " does not exist; run loadline install\n" +
		"✖ server: no Loadline server answers at " + url + "; the next Claude Code session starts it, or run loadline serve\n" +
		"✖ feeds: cannot be checked while the server is down\n"
	if code != exitFailure || stdout.String() != want || stderr.Len() != 0 {
		t.Errorf("doctor = %d, stderr %q, stdout:\n%s\nwant %d and:\n%s", code, stderr.String(), stdout.String(), exitFailure, want)
	}
}
