//go:build !linux

package plugin

import "syscall"

// procAttr on non-Linux platforms (macOS/BSD — the Docker-Desktop developer path) sets only
// the process group: there is no kernel parent-death signal outside Linux, so if the host
// crashes the plugin child is orphaned until the next boot sweep reclaims it (invariant #1).
// The sweep is therefore load-bearing on this path, not a rare backstop. This file compiles on
// any Unix; the platform does not target Windows (where SysProcAttr has no Setpgid).
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
