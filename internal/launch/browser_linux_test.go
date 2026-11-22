package launch_test

// autoCommand is how a Linux desktop opens a URL in the default browser.
func autoCommand(url string) (string, []string) {
	return "xdg-open", []string{url}
}
