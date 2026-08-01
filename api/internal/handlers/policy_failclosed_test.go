package handlers_test

import (
	"context"
	"testing"

	"github.com/osctf/platform/internal/handlers"
)

// TestFreezeFailsClosedWithoutEvents (3a-iv): a server with no Events dependency must
// HIDE solves, not leak them — a security control cannot depend on wiring nothing
// enforces. The nil-Events path returns hide=true (a zero cutoff hides every solve).
func TestFreezeFailsClosedWithoutEvents(t *testing.T) {
	s := handlers.New(handlers.Deps{}) // Events deliberately unwired
	hide, _ := s.FreezeHidesForTest(context.Background(), nil)
	if !hide {
		t.Fatal("missing Events must fail CLOSED (hide solves), but freeze hiding was off — silent leak")
	}
}
