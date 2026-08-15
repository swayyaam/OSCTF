// Package version exposes the build version, set via -ldflags at build time.
package version

// Version is overridden at build time with `-ldflags "-X ...version.Version=..."`.
// The main package also sets its own `version` var; this package is the importable one.
var Version = "dev"
