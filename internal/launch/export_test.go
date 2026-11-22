package launch

// OpenBrowser lets the tests wait until a browser they started has been
// reaped, so that none ends with Open's reaper still waiting for it.
func OpenBrowser(url string, b Browser) (reaped <-chan struct{}, err error) {
	return openBrowser(url, b)
}
