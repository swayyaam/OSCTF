//go:build linux

package plugin

import "syscall"

// procAttr sets kernel-level parent-death on Linux (the deploy target): if the host process
// dies, the kernel sends SIGKILL to the plugin child, so a host crash cannot orphan a plugin.
// go-plugin v1.8.0 provides NO parent-death of its own (verified in source: it neither sets a
// death signal nor a process group, and the plugin server watches neither stdin nor the
// connection), so the loader sets it here. Setpgid puts the child in its own group so the
// whole plugin subtree can be signalled together.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
