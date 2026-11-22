//go:build !race

package domain_test

// raceEnabled says the race detector is on, which slows the code it
// instruments several times over, and unevenly, so that the cost checks
// run at a smaller scale under it.
const raceEnabled = false
