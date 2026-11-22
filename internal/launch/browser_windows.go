package launch

import (
	"os"
	"path/filepath"
)

// autoCommand opens url in the default browser through the URL protocol
// handler, which takes the rest of its command line as the URL. Unlike
// cmd /c start, no shell reads the URL, so an & in it stays part of it.
func autoCommand(url string) (string, []string) {
	return "rundll32", []string{"url.dll,FileProtocolHandler", url}
}

// installRoots are the variables that hold the directories browsers install
// under: machine-wide installs, 64- and 32-bit, and per-user ones.
var installRoots = []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"}

// installPaths are where each browser's executable sits under a root.
var installPaths = map[string]string{
	brave:   `BraveSoftware\Brave-Browser\Application\brave.exe`,
	chrome:  `Google\Chrome\Application\chrome.exe`,
	edge:    `Microsoft\Edge\Application\msedge.exe`,
	firefox: `Mozilla Firefox\firefox.exe`,
}

// candidates returns where the named browser may be installed, under each
// root that getenv sets.
func candidates(name string, getenv func(string) string) []string {
	tail, ok := installPaths[name]
	if !ok {
		return nil
	}
	var paths []string
	for _, root := range installRoots {
		if dir := getenv(root); dir != "" {
			paths = append(paths, filepath.Join(dir, tail))
		}
	}
	return paths
}

func installed(name string) (string, bool) {
	return firstInstalled(candidates(name, os.Getenv))
}
