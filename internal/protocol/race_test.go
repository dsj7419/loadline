//go:build race

package protocol_test

// raceEnabled says the race detector is on, which makes the heaviest
// tests run fewer cases to stay inside CI's time.
const raceEnabled = true
