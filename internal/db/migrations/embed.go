// Package migrations embeds the goose SQL migrations so they ship inside the
// binary and run on boot without a migrations directory on disk.
package migrations

import "embed"

// FS holds every *.sql migration in this directory.
//
//go:embed *.sql
var FS embed.FS
