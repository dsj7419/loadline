package launch

// autoCommand opens url in the default browser.
func autoCommand(url string) (string, []string) {
	return "open", []string{url}
}

// appExecutables are each browser's executable inside /Applications.
var appExecutables = map[string]string{
	brave:   "Brave Browser.app/Contents/MacOS/Brave Browser",
	chrome:  "Google Chrome.app/Contents/MacOS/Google Chrome",
	edge:    "Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	firefox: "Firefox.app/Contents/MacOS/firefox",
}

// candidates returns where the named browser is installed, if anywhere.
func candidates(name string) []string {
	executable, ok := appExecutables[name]
	if !ok {
		return nil
	}
	return []string{"/Applications/" + executable}
}

func installed(name string) (string, bool) {
	return firstInstalled(candidates(name))
}
