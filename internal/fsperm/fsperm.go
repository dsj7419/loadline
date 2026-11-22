// Package fsperm keeps the directories Loadline writes its secrets to
// private to the user who runs it, and tells when one is not. The data
// directory holds the ingest token and the binary every hook runs, and
// LOADLINE_HOME can put it in a folder that lets other users in.
package fsperm

import "strings"

// andList joins names as a sentence lists them: "a", "a and b", "a, b and c".
func andList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	last := len(names) - 1
	return strings.Join(names[:last], ", ") + " and " + names[last]
}
