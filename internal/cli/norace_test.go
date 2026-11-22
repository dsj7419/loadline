//go:build !race

package cli_test

// raceEnabled says the race detector is on, which slows the code it
// instruments several times over (see gaveUpBy).
const raceEnabled = false
