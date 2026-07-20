//go:build !embed_spa

package webdist

import "io/fs"

// Embedded reports whether the SPA is compiled into the binary.
const Embedded = false

// FS is nil in dev builds; Handler serves a placeholder instead.
var FS fs.FS
