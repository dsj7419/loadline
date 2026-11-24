package fsperm

// InProfile tells whether a folder is in the user's profile, the only place
// Loadline offers to take back a folder that an account this computer no
// longer has owns. It judges where each path really is, so that neither a
// short 8.3 name nor a junction, leading either way, can mislead it.

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// profileAt makes InProfile take profile for the folder of the user's
// profile, for the rest of the test.
func profileAt(t *testing.T, profile string) {
	t.Helper()
	previous := userProfile
	userProfile = func() (string, error) { return profile, nil }
	t.Cleanup(func() { userProfile = previous })
}

// shortName returns the 8.3 short name of long, an existing folder, joined
// to the folder that holds it, or long itself on a volume that makes no
// short names.
func shortName(t *testing.T, long string) string {
	t.Helper()
	name, err := windows.UTF16PtrFromString(long)
	if err != nil {
		t.Fatal(err)
	}
	var buf [windows.MAX_LONG_PATH]uint16
	n, err := windows.GetShortPathName(name, &buf[0], windows.MAX_LONG_PATH)
	if err != nil || n >= windows.MAX_LONG_PATH {
		t.Fatalf("GetShortPathName(%s): %d, %v", long, n, err)
	}
	return filepath.Join(filepath.Dir(long), filepath.Base(windows.UTF16ToString(buf[:n])))
}

func TestInProfile(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "a profile with a long name")
	local := filepath.Join(profile, "AppData", "Local")
	outside := filepath.Join(root, "elsewhere")
	beside := profile + " too"
	for _, dir := range []string{local, outside, beside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	short := shortName(t, profile)
	junction(t, filepath.Join(profile, "leads out"), outside)
	junction(t, filepath.Join(outside, "leads in"), local)

	for _, tt := range []struct {
		name, profile, path string
		short               bool
		want                bool
	}{
		{name: "the profile itself", profile: profile, path: profile, want: true},
		{name: "a folder in it", profile: profile, path: local, want: true},
		{name: "a folder in it, by the profile's short name", profile: profile, path: filepath.Join(short, "AppData", "Local"), short: true, want: true},
		{name: "a folder in it, the profile given by its short name", profile: short, path: local, short: true, want: true},
		{name: "a path in it that does not exist yet", profile: profile, path: filepath.Join(local, "loadline", "bin"), want: true},
		{name: "a path outside it that does not exist yet", profile: profile, path: filepath.Join(outside, "loadline"), want: false},
		{name: "the folder that holds it", profile: profile, path: root, want: false},
		{name: "a folder beside it whose name starts with its name", profile: profile, path: beside, want: false},
		{name: "a junction in it that leads out", profile: profile, path: filepath.Join(profile, "leads out", "loadline"), want: false},
		{name: "a junction outside it that leads in", profile: profile, path: filepath.Join(outside, "leads in", "loadline"), want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.short && short == profile {
				t.Skipf("the volume of %s makes no 8.3 short names (fsutil 8dot3name query shows it)", root)
			}
			profileAt(t, tt.profile)

			got, err := InProfile(tt.path)

			if err != nil || got != tt.want {
				t.Errorf("InProfile(%s), with the profile at %s = %t, %v; want %t", tt.path, tt.profile, got, err, tt.want)
			}
		})
	}
}

// foldedNames are pairs of folder names that Unicode's case folding takes for
// one, and NTFS, which compares names by its own table of upper case, keeps
// apart. The characters are given by their code points, so that none of them
// hides in this file.
var foldedNames = map[string][2]string{
	"the micro sign and the Greek mu":         {"\u00b5-user", "\u03bc-user"},
	"Georgian Mkhedruli and Mtavruli letters": {"\u10d2\u10d8\u10dd", "\u1c92\u1c98\u1c9d"},
	"two Cyrillic letters uk":                 {"\ua64b-user", "\u1c88-user"},
	"the Kelvin sign and the letter K":        {"Kevin", "\u212aevin"},
}

// foldedFolders makes a folder by each name of pair, side by side in root,
// and skips the test where the file system takes the two for one name.
func foldedFolders(t *testing.T, root string, pair [2]string) (first, second string) {
	t.Helper()
	first, second = filepath.Join(root, pair[0]), filepath.Join(root, pair[1])
	if err := os.Mkdir(first, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0o700); err != nil {
		t.Skipf("the file system of %s takes %q and %q for one name: %v", root, pair[0], pair[1], err)
	}
	return first, second
}

// A folder beside the profile whose name differs from the profile's only in
// letters that Unicode folds together, and NTFS keeps apart, is another
// folder: neither it nor anything in it is in the profile, and it is not the
// profile's folder.
func TestInProfile_TellsTheProfileFromAFolderBesideItWhoseNameFoldsToItsName(t *testing.T) {
	for name, pair := range foldedNames {
		t.Run(name, func(t *testing.T) {
			profile, beside := foldedFolders(t, t.TempDir(), pair)
			local := filepath.Join(beside, "AppData", "Local")
			if err := os.MkdirAll(local, 0o700); err != nil {
				t.Fatal(err)
			}
			profileAt(t, profile)

			for _, path := range []string{beside, filepath.Join(local, "loadline")} {
				if in, err := InProfile(path); err != nil || in {
					t.Errorf("InProfile(%q), with the profile at %q = %t, %v; want false: it is beside the profile", path, profile, in, err)
				}
			}
			if samePath(profile, beside) {
				t.Errorf("samePath(%q, %q) = true; want false: they are two folders", profile, beside)
			}
		})
	}
}

// Resolved gives a path as InProfile judges it: each link followed, each
// short name in full, and the part that does not exist yet as written.
func TestResolved(t *testing.T) {
	root := t.TempDir()
	long := filepath.Join(root, "a folder with a long name")
	target := filepath.Join(root, "target")
	for _, dir := range []string{long, target} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	short := shortName(t, long)
	junction(t, filepath.Join(root, "link"), target)
	heldRoot := mustFinalPath(t, root)

	for _, tt := range []struct {
		name, path, want string
		short            bool
	}{
		{name: "a folder by its own names", path: long, want: filepath.Join(heldRoot, "a folder with a long name")},
		{
			name: "a path not made yet, by a short name", path: filepath.Join(short, "loadline"), short: true,
			want: filepath.Join(heldRoot, "a folder with a long name", "loadline"),
		},
		{name: "a path not made yet, through a junction", path: filepath.Join(root, "link", "loadline"), want: filepath.Join(heldRoot, "target", "loadline")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.short && short == long {
				t.Skipf("the volume of %s makes no 8.3 short names (fsutil 8dot3name query shows it)", root)
			}

			got, err := Resolved(tt.path)

			if err != nil || got != tt.want {
				t.Errorf("Resolved(%s) = %q, %v; want %q", tt.path, got, err, tt.want)
			}
		})
	}
}

// Without a stand-in, InProfile asks Windows where the user's profile is.
func TestInProfile_AsksWindowsWhereTheProfileIs(t *testing.T) {
	profile, err := windows.GetCurrentProcessToken().GetUserProfileDirectory()
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		filepath.Join(profile, "AppData", "Local", "loadline-not-made-yet"): true,
		filepath.Dir(profile): false,
	} {
		if got, err := InProfile(path); err != nil || got != want {
			t.Errorf("InProfile(%s), with the profile at %s = %t, %v; want %t", path, profile, got, err, want)
		}
	}
}
