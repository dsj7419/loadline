package server_test

import (
	"testing"

	"go.uber.org/goleak"
)

// Every event stream, request and timer a test starts must be gone by the
// end of the run.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
