//go:build race

package tracker_test

// raceEnabled says the race detector is on, which slows the code it
// instruments several times over, and unevenly, so that a bound in
// milliseconds cannot hold under it (see costtest).
const raceEnabled = true
