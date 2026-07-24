// Package clock provides an injectable time source so time-based decisions
// (event window, freeze) are testable without sleeping (docs/v0.1/03-tech-stack.md).
package clock

import "time"

// Clock returns the current time. Production uses System; tests inject a fixed one.
type Clock func() time.Time

// System is the real wall clock in UTC.
func System() Clock {
	return func() time.Time { return time.Now().UTC() }
}

// Fixed returns a clock that always reports t.
func Fixed(t time.Time) Clock {
	return func() time.Time { return t }
}
