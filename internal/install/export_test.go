package install

// DetectShellOn exposes the shell detection for any operating system to the
// external tests, which run on just one.
var DetectShellOn = detectShell
