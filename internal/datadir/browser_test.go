package datadir_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/fsperm"
)

// notPrivateProfile ends what every step refuses a browser profile with when
// Loadline did not make it private.
const notPrivateProfile = " is not private, so the dashboard's browser profile there cannot be trusted: " +
	"remove it, and Loadline makes it again"

// securedHome returns a data directory below top as install leaves it before
// the dashboard first opens: private, and holding the token.
func securedHome(t *testing.T) (home, top string) {
	t.Helper()
	top = t.TempDir()
	home = filepath.Join(top, "loadline")
	if err := datadir.Secure(home, top); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "token"), []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, top
}

// openFolder returns a folder outside the data directory that is not
// private, which a link out of it leads to.
func openFolder(t *testing.T) string {
	t.Helper()
	folder := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if private, err := fsperm.DirPrivate(folder); err != nil || private {
		t.Fatalf("%s is private (%v), so the test proves nothing", folder, err)
	}
	return folder
}

// refusedAsNotPrivate checks that each step that takes the data directory,
// or the profile, refuses profile, in home below top, as not private.
func refusedAsNotPrivate(t *testing.T, home, top, profile string) {
	t.Helper()
	profileRefusedWith(t, home, top, profile, profile+notPrivateProfile)
}

// profileRefusedWith checks that each step that takes the data directory,
// or the profile, refuses profile, in home below top, in the words want.
func profileRefusedWith(t *testing.T, home, top, profile, want string) {
	t.Helper()
	for step, err := range map[string]error{
		"Make":                  datadir.Make(home),
		"Verify":                datadir.Verify(home, top),
		"PrepareBrowserProfile": datadir.PrepareBrowserProfile(profile),
	} {
		if err == nil || err.Error() != want {
			t.Errorf("%s = %v, want it refused: %q", step, err, want)
		}
	}
	if got, err := datadir.Check(home, top); err != nil || got != want {
		t.Errorf("Check = %q, %v; want %q", got, err, want)
	}
}

func TestOwns_TheBrowserProfile(t *testing.T) {
	if !datadir.Owns("browser") {
		t.Error(`Owns("browser") = false, want true`)
	}
	for _, name := range []string{"Browser", "browsers", "browser.old", ".browser"} {
		if datadir.Owns(name) {
			t.Errorf("Owns(%q) = true, want false", name)
		}
	}
}

// Only the data directory's own browser is the dashboard's profile. A
// folder of that name further down is like anything else in the folder that
// holds it: walked into and judged.
func TestOwns_OnlyTheTopLevelBrowserFolder(t *testing.T) {
	if nested := filepath.Join("spool", "browser"); datadir.Owns(nested) {
		t.Errorf("Owns(%q) = true, want false", nested)
	}
	for _, tt := range []struct {
		name, folder string
		refused      bool
	}{
		{name: "the profile", folder: "browser"},
		{name: "a folder of that name in the spool", folder: filepath.Join("spool", "browser"), refused: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home, _ := securedHome(t)
			folder := filepath.Join(home, tt.folder)
			if err := fsperm.MkdirAll(folder); err != nil {
				t.Fatal(err)
			}
			shared := filepath.Join(t.TempDir(), "shared.txt")
			if err := os.WriteFile(shared, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			inside := filepath.Join(folder, "Local State")
			if err := os.Link(shared, inside); err != nil {
				t.Skipf("cannot make a hard link here: %v", err)
			}

			err := datadir.Make(home)

			want := inside + " has another name"
			switch {
			case tt.refused && (err == nil || !strings.HasPrefix(err.Error(), want)):
				t.Errorf("Make = %v, want it refused, starting %q", err, want)
			case !tt.refused && err != nil:
				t.Errorf("Make = %v, want what the profile holds left alone", err)
			}
		})
	}
}

// A profile that is a link would have the browser keep the dashboard's
// cookie wherever the link leads.
func TestPrepareBrowserProfile_RefusesALink(t *testing.T) {
	home, _ := securedHome(t)
	outside := openFolder(t)
	profile := filepath.Join(home, "browser")
	linkFolder(t, profile, outside)

	err := datadir.PrepareBrowserProfile(profile)

	if want := profile + " is a link"; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("PrepareBrowserProfile = %v, want it refused, starting %q", err, want)
	}
	if private, err := fsperm.DirPrivate(outside); err != nil || private {
		t.Errorf("%s, where the link leads, is private (%v), want it left as it was", outside, err)
	}
}

// The profile is one of Loadline's folders, judged itself as each of them
// is: no link, and the user's own. Its access list says nothing of its
// owner, who may always change that list, so one that belongs to someone
// else is refused even when its list is exactly what Loadline gives it, by
// the browser's launch too.
func TestMake_JudgesTheBrowserProfileItself(t *testing.T) {
	for _, tt := range []struct {
		name    string
		prepare func(t *testing.T, profile string)
		want    string
	}{
		{
			name: "another owner",
			prepare: func(t *testing.T, profile string) {
				t.Helper()
				if err := fsperm.MkdirAll(profile); err != nil {
					t.Fatal(err)
				}
				datadir.OwnedByStranger(t, profile)
			},
			want: " belongs to a stranger",
		},
		{
			name: "a link",
			prepare: func(t *testing.T, profile string) {
				t.Helper()
				linkFolder(t, profile, openFolder(t))
			},
			want: " is a link",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home, top := securedHome(t)
			profile := filepath.Join(home, "browser")
			tt.prepare(t, profile)
			want := profile + tt.want

			err := datadir.Make(home)

			if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.HasSuffix(err.Error(), "run loadline install") {
				t.Fatalf("Make = %v, want it refused, starting %q", err, want)
			}
			if got, checkErr := datadir.Check(home, top); checkErr != nil || got != err.Error() {
				t.Errorf("Check = %q, %v; want what Make refused with: %q", got, checkErr, err.Error())
			}
			if prepareErr := datadir.PrepareBrowserProfile(profile); prepareErr == nil || prepareErr.Error() != err.Error() {
				t.Errorf("PrepareBrowserProfile = %v; want what Make refused with: %q", prepareErr, err.Error())
			}
		})
	}
}

// A file by the profile's name is no profile, whatever its permissions say:
// the browser cannot keep anything in it.
func TestMake_RefusesABrowserProfileThatIsNotAFolder(t *testing.T) {
	home, top := securedHome(t)
	profile := filepath.Join(home, "browser")
	if err := os.WriteFile(profile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookPrivate(t, profile)
	if private, err := fsperm.DirPrivate(profile); err != nil || !private {
		t.Fatalf("%s does not look private (%v), so the test proves nothing", profile, err)
	}

	profileRefusedWith(t, home, top, profile,
		profile+" is not a folder, so the dashboard's browser profile cannot be kept there: remove it, and Loadline makes the profile in its place")
}

// The browser writes in the profile Loadline made private, as the user, and
// leaves there what the walk refuses anywhere else in the data directory:
// access lists of its own on Windows, symbolic links on Linux and macOS.
// Nothing that prepares or checks the data directory looks below the
// profile, so none of them refuses, reports or changes any of it.
func TestMake_LeavesWhatTheBrowserProfileHoldsAlone(t *testing.T) {
	home, top := securedHome(t)
	profile := filepath.Join(home, "browser")
	if err := fsperm.MkdirAll(profile); err != nil {
		t.Fatal(err)
	}
	unchanged := plantWhatABrowserLeaves(t, profile)

	if err := datadir.Make(home); err != nil {
		t.Errorf("Make = %v, want nil", err)
	}
	if err := datadir.Verify(home, top); err != nil {
		t.Errorf("Verify = %v, want nil", err)
	}
	if got, err := datadir.Check(home, top); err != nil || got != "" {
		t.Errorf("Check = %q, %v; want \"\"", got, err)
	}
	if err := datadir.PrepareBrowserProfile(profile); err != nil {
		t.Errorf("PrepareBrowserProfile = %v, want the profile taken for the next launch", err)
	}

	unchanged(t)
}
