package launch

// autoCommand opens url in the desktop's default browser.
func autoCommand(url string) (string, []string) {
	return "xdg-open", []string{url}
}

// pathCommands are the commands each browser's packages put on PATH.
var pathCommands = map[string]string{
	brave:   "brave-browser",
	chrome:  "google-chrome",
	edge:    "microsoft-edge",
	firefox: "firefox",
}

// candidates returns the command that runs the named browser, if any.
func candidates(name string) []string {
	command, ok := pathCommands[name]
	if !ok {
		return nil
	}
	return []string{command}
}

func installed(name string) (string, bool) {
	return firstInstalled(candidates(name))
}
