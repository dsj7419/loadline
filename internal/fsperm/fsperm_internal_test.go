package fsperm

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// When the folders that really hold the data directory cannot be found, as
// on a file system that cannot say where a path leads, install and the
// server refuse it, and say what to do.
func TestMovableAncestor_SaysWhatToDoWhenThePathCannotBeResolved(t *testing.T) {
	previous := finalPath
	finalPath = func(string) (string, error) { return "", errors.New("the file system cannot say") }
	t.Cleanup(func() { finalPath = previous })
	top := t.TempDir()

	_, err := MovableAncestor(filepath.Join(top, "loadline"), top)

	if err == nil || !strings.Contains(err.Error(), "the file system cannot say") ||
		!strings.HasSuffix(err.Error(), "set LOADLINE_HOME to a folder in your user profile") {
		t.Errorf("MovableAncestor = %v; want the reason and the advice", err)
	}
}
