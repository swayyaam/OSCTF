//go:build embed_spa

package webdist

import (
	"embed"
	"io/fs"
)

// staticFiles holds the built SPA, copied into static/ at image-build time.
//
//go:embed all:static
var staticFiles embed.FS

// Embedded reports whether the SPA is compiled into the binary.
const Embedded = true

// FS is the SPA rooted at the static/ subdirectory.
var FS, _ = fs.Sub(staticFiles, "static")
