//go:build crashtest

package submissions

// This file exists ONLY in the crashtest build (a build tag no production target sets — not
// `go build`, not the Dockerfile). It carries the settable crash seam the subprocess crash test
// arms; the production binary compiles crashhook_off.go instead, where the hook does not exist at
// all. That is stronger than a nil check: there is nothing to reach, so nothing can arm a process
// exit on the submission hot path in a shipped binary.

// afterCommitCrashHook, when set, fires at the commit→async seam in Submit.
var afterCommitCrashHook func()

func crashAfterCommit() {
	if afterCommitCrashHook != nil {
		afterCommitCrashHook()
	}
}

// SetAfterCommitCrashHookForTest installs the seam. Only TestCrashBetweenCommitAndAsyncRecovers
// calls it, and only in the crashtest build.
func SetAfterCommitCrashHookForTest(fn func()) { afterCommitCrashHook = fn }
