package datadir_test

// A junction named like a file Loadline writes before it puts it in place.
// Only a regular file of that name is another process's to leave alone; a
// junction is a link among Loadline's own paths, which is refused.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsj7419/loadline/internal/datadir"
	"github.com/dsj7419/loadline/internal/fsperm/fspermtest"
)

// elsewhere returns a folder that has nothing to do with Loadline, a
// project in a folder every signed-in user may work in, and a file in it.
func elsewhere(t *testing.T, top string) (project, doc string) {
	t.Helper()
	shared := filepath.Join(top, "team")
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.Permissive(t, shared)
	project = filepath.Join(shared, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	doc = filepath.Join(project, "doc.txt")
	writeFile(t, doc)
	if others := fspermtest.Others(t, doc, fspermtest.Read); len(others) == 0 {
		t.Fatalf("%s is not shared, so the test proves nothing: %s", doc, fspermtest.AccessList(t, doc))
	}
	return project, doc
}

// openDataDir returns a data directory that is not private yet, in a folder
// that lets every signed-in user in, as LOADLINE_HOME on a second drive was
// before Loadline first secured it, and that folder.
func openDataDir(t *testing.T, top string) (dir, parent string) {
	t.Helper()
	parent = filepath.Join(top, "drive")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	fspermtest.Permissive(t, parent)
	dir = filepath.Join(parent, "loadline")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, parent
}

func TestSecure_RefusesAJunctionNamedLikeAFileBeingWritten(t *testing.T) {
	for _, name := range inFlight {
		t.Run(filepath.Base(name), func(t *testing.T) {
			top := privateTop(t)
			project, doc := elsewhere(t, top)
			dir, parent := openDataDir(t, top)
			link := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			junction(t, link, project)
			beforeProject, beforeDoc := fspermtest.AccessList(t, project), fspermtest.AccessList(t, doc)

			err := datadir.Secure(dir, parent)

			if err == nil || !strings.Contains(err.Error(), link+" is a link") {
				t.Errorf("Secure = %v, want it to refuse %s as a link", err, link)
			}
			if got := fspermtest.AccessList(t, project); got != beforeProject {
				t.Errorf("Secure changed the access list of %s, outside the data directory, from %s to %s", project, beforeProject, got)
			}
			if got := fspermtest.AccessList(t, doc); got != beforeDoc {
				t.Errorf("Secure changed the access list of %s, outside the data directory, from %s to %s", doc, beforeDoc, got)
			}
		})
	}
}

// The same on the path every hook takes when there is no token yet.
func TestMake_RefusesAJunctionNamedLikeAFileBeingWritten(t *testing.T) {
	top := privateTop(t)
	project, doc := elsewhere(t, top)
	dir, _ := openDataDir(t, top)
	link := filepath.Join(dir, ".token-1234")
	junction(t, link, project)
	before := fspermtest.AccessList(t, doc)

	err := datadir.Make(dir)

	if err == nil || !strings.Contains(err.Error(), link+" is a link") {
		t.Errorf("Make = %v, want it to refuse %s as a link", err, link)
	}
	if got := fspermtest.AccessList(t, doc); got != before {
		t.Errorf("Make changed the access list of %s, outside the data directory, from %s to %s", doc, before, got)
	}
}

// Doctor reports it as it reports any link among Loadline's paths.
func TestCheck_ReportsAJunctionNamedLikeAFileBeingWritten(t *testing.T) {
	home, top := loadlineHome(t)
	project, _ := elsewhere(t, top)
	link := filepath.Join(home, ".token-1234")
	junction(t, link, project)

	got, err := datadir.Check(home, top)

	if err != nil || !strings.Contains(got, link+" is a link") {
		t.Errorf("Check = %q, %v; want it to report %s as a link", got, err, link)
	}
}
