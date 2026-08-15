package ws

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package's test run if any goroutine outlives the tests — the
// hub and its per-connection pumps must all exit when their context is cancelled.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
