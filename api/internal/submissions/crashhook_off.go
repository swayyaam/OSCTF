//go:build !crashtest

package submissions

// crashAfterCommit is a no-op in every build except the crashtest one. There is no hook variable and
// no way to arm a process exit on the submission hot path in the production binary — the seam does
// not exist here beyond this empty function. See crashhook_on.go for the test-only settable hook.
func crashAfterCommit() {}
