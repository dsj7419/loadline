//go:build race

package launch_test

// raceEnabled says the race detector is on, which slows the test binary's
// exit several times over (see reapLimit).
const raceEnabled = true
