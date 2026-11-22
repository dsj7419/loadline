package launch_test

// autoCommand is how macOS opens a URL in the default browser.
func autoCommand(url string) (string, []string) {
	return "open", []string{url}
}
