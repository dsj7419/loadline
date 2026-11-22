//go:build !race

package forward_test

// raceEnabled says the race detector is on, which slows the code it
// instruments several times over (see assertGaveUpAt).
const raceEnabled = false
