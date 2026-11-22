package launch_test

// autoCommand is how Windows opens a URL in the default browser: through the
// URL protocol handler, which takes the URL as one argument, with no shell
// in between to read the & in it.
func autoCommand(url string) (string, []string) {
	return "rundll32", []string{"url.dll,FileProtocolHandler", url}
}
