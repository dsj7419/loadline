package datadir_test

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/fsperm"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// chromiumNetworkSandbox is the capability SID that Chromium's network
// service gives access to the folders of the profile it keeps its data in
// (lpacChromeNetworkSandbox), whether its sandbox is on or not.
const chromiumNetworkSandbox = "S-1-15-3-1024-1528657515-1944437972-2795272136-1227674495-293963776-353393192-4060142787-1908764039"

// networkSandbox returns chromiumNetworkSandbox as a SID.
func networkSandbox(t *testing.T) *windows.SID {
	t.Helper()
	sid, err := windows.StringToSid(chromiumNetworkSandbox)
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

// linkFolder makes link a junction to target, as any user may in a folder
// they can create folders in.
func linkFolder(t *testing.T, link, target string) {
	t.Helper()
	junction(t, link, target)
}

// lookPrivate gives path, a file, the protected list Loadline gives its
// folders, which fsperm.DirPrivate reads alone. It is stored as is, as
// SetFileSecurity or a backup tool stores it: set the usual way, a file's
// entries lose the inheritance flags that the folders' list holds.
func lookPrivate(t *testing.T, path string) {
	t.Helper()
	fspermtest.SetAccessListAsIs(t, path,
		"D:P(A;OICI;FA;;;"+fspermtest.UserSID(t)+")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
}

// sameLists reports each path whose access list differs between before and
// after, or that only one of them holds.
func sameLists(t *testing.T, before, after map[string]fspermtest.List) {
	t.Helper()
	for path, list := range before {
		got, ok := after[path]
		switch {
		case !ok:
			t.Errorf("%s is gone", path)
		case !got.Equal(list):
			t.Errorf("the access list of %s went from %s to %s", path, list, got)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s appeared", path)
		}
	}
}

// plantWhatABrowserLeaves puts in profile what Chromium browsers leave in a
// profile on Windows: a folder whose list gives the network service's
// capability SID access of its own, explicitly and to what it holds, a file
// there that inherits that access, and a file with an entry of its own that
// denies everyone the right to run it. It adds a junction out of the
// profile, which no browser was seen to make. It returns a check that every
// list in the profile, and where the junction leads, is as it was, and the
// junction still there.
func plantWhatABrowserLeaves(t *testing.T, profile string) (unchanged func(*testing.T)) {
	t.Helper()
	network := filepath.Join(profile, "Default", "Network")
	if err := os.MkdirAll(network, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.SetAccessList(t, network,
		"D:AI(A;;0x1301bf;;;"+chromiumNetworkSandbox+")(A;OICIIO;SDGXGWGR;;;"+chromiumNetworkSandbox+")")
	cookies := filepath.Join(network, "Cookies")
	writeFile(t, cookies)
	if list := fspermtest.AccessList(t, cookies); !list.Names(networkSandbox(t)) {
		t.Fatalf("%s inherits no access for the network service, so the test proves nothing: %s", cookies, list)
	}
	storage := filepath.Join(profile, "Default", "LocalStorage")
	writeFile(t, storage)
	fspermtest.SetAccessList(t, storage, "D:(D;;WP;;;WD)")
	if list := fspermtest.AccessList(t, storage); !list.Denies() {
		t.Fatalf("%s denies nothing, so the test proves nothing: %s", storage, list)
	}
	project, _ := elsewhere(t, t.TempDir())
	link := filepath.Join(profile, "Default", "Project")
	junction(t, link, project)

	before, beforeProject := accessLists(t, profile), accessLists(t, project)
	return func(t *testing.T) {
		t.Helper()
		sameLists(t, before, accessLists(t, profile))
		sameLists(t, beforeProject, accessLists(t, project))
		if isLink, err := fsperm.IsLink(link); err != nil || !isLink {
			t.Errorf("%s is no longer a junction (%v)", link, err)
		}
	}
}

// Loadline makes the profile before the browser first launches, with the
// private list of its own, protected from what the data directory passes
// down, so that the data directory's list never goes through it.
func TestPrepareBrowserProfile_MakesItPrivate(t *testing.T) {
	home, _ := securedHome(t)
	profile := filepath.Join(home, "browser")

	if err := datadir.PrepareBrowserProfile(profile); err != nil {
		t.Fatalf("PrepareBrowserProfile: %v", err)
	}

	private, err := fsperm.DirPrivate(profile)
	if err != nil || !private || !fspermtest.Protected(t, profile) {
		t.Errorf("%s has %s (private: %t, %v), want the private list, protected", profile, fspermtest.AccessList(t, profile), private, err)
	}
}

// When Secure makes the data directory private, the directory passes its
// new list down, and Windows stops at a folder whose list is protected. The
// profile's is from the moment Loadline makes it, so what the browser keeps
// there is never rewritten, however much it holds.
func TestSecure_PassesNothingDownIntoTheBrowserProfile(t *testing.T) {
	top := privateTop(t)
	home := filepath.Join(top, "loadline")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.Grant(t, home, fspermtest.Read, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
	profile := filepath.Join(home, "browser")
	if err := fsperm.MkdirAll(profile); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(profile, "Local State")
	writeFile(t, state)
	// Stored as it is, with an inherited entry the profile does not pass
	// down: whatever worked inheritance out again below the profile would
	// take that entry off.
	fspermtest.SetAccessListAsIs(t, state, "D:AI(A;ID;0x1301bf;;;"+chromiumNetworkSandbox+")(A;ID;FA;;;"+
		fspermtest.UserSID(t)+")(A;ID;FA;;;SY)(A;ID;FA;;;BA)")
	before := fspermtest.AccessList(t, state)
	if !before.Names(networkSandbox(t)) {
		t.Fatalf("%s has %s, with no entry for the network service, so the test proves nothing", state, before)
	}
	// Another Loadline process's file, which only the data directory's list,
	// passed down, makes private: it has a second name beside it.
	writing := filepath.Join(home, ".token-1234")
	beganUnderTwoNames(t, writing)
	if others := fspermtest.Others(t, writing, fspermtest.Read); len(others) == 0 {
		t.Fatalf("%s admits nobody else, so the test proves nothing", writing)
	}

	if err := datadir.Secure(home, top); err != nil {
		t.Fatalf("Secure: %v", err)
	}

	if others := fspermtest.Others(t, writing, fspermtest.Any); len(others) > 0 {
		t.Fatalf("%s still admits %v: the data directory passed nothing down", writing, others)
	}
	if after := fspermtest.AccessList(t, state); !after.Equal(before) {
		t.Errorf("the access list of %s, in the profile, went from %s to %s", state, before, after)
	}
	if private, err := fsperm.DirPrivate(profile); err != nil || !private || !fspermtest.Protected(t, profile) {
		t.Errorf("%s has %s (private: %t, %v), want the private list, protected", profile, fspermtest.AccessList(t, profile), private, err)
	}
}

// A browser folder Loadline did not make, or that no longer has the list it
// made it with, cannot be told from one someone else set up, and the files in
// it keep whatever they inherited. Nothing takes it, and nothing changes it:
// the profile is the browser's cache, and the browser makes a new one.
func TestMake_RefusesABrowserProfileItDidNotMakePrivate(t *testing.T) {
	user := fspermtest.UserSID(t)
	for _, tt := range []struct {
		name string
		// open has the data directory let every signed-in user read what it
		// holds, as one that is not private yet may.
		open bool
		// list is the profile's own list, or "" for one that takes what the
		// data directory passes down.
		list string
	}{
		{name: "inherits from a data directory open to Authenticated Users", open: true},
		{name: "inherits only what the private data directory passes down"},
		{
			name: "protected, but admits Authenticated Users too",
			list: "D:P(A;OICI;FA;;;" + user + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FR;;;AU)",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			top := privateTop(t)
			home := filepath.Join(top, "loadline")
			if tt.open {
				if err := os.Mkdir(home, 0o700); err != nil {
					t.Fatal(err)
				}
				fspermtest.Grant(t, home, fspermtest.Read, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
			} else if err := datadir.Secure(home, top); err != nil {
				t.Fatal(err)
			}
			profile := filepath.Join(home, "browser")
			if err := os.Mkdir(profile, 0o700); err != nil {
				t.Fatal(err)
			}
			if tt.list != "" {
				fspermtest.SetAccessList(t, profile, tt.list)
			}
			state := filepath.Join(profile, "Local State")
			writeFile(t, state)
			if others := fspermtest.Others(t, state, fspermtest.Read); tt.open && len(others) == 0 {
				t.Fatalf("%s admits nobody else, so the test proves nothing", state)
			}
			before := accessLists(t, home)

			refusedAsNotPrivate(t, home, top, profile)

			sameLists(t, before, accessLists(t, home))
		})
	}
}
