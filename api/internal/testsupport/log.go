package testsupport

import (
	"io"
	"log/slog"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// DiscardLogger returns a logger that drops everything (for integration tests).
func DiscardLogger() *slog.Logger { return discardLogger() }
