// Package apperr defines the domain error vocabulary shared by every service.
// Services return these; the single translation point httpx.RenderError maps them
// to RFC 9457 problem+json. Keeping them here (not in each service package) lets
// httpx translate without importing the whole service tree.
package apperr

import (
	"fmt"
	"time"
)

// Sentinel errors for the common cases. Wrap with fmt.Errorf("...: %w", Err...)
// to add context; check with errors.Is.
type sentinel string

func (s sentinel) Error() string { return string(s) }

const (
	ErrNotFound        = sentinel("not found")
	ErrForbidden       = sentinel("forbidden")
	ErrConflict        = sentinel("conflict")
	ErrUnauthenticated = sentinel("unauthenticated")
	ErrBadRequest      = sentinel("bad request")
)

// Validation is a set of field-level failures rendered as a 422 with an errors map.
type Validation struct {
	Fields map[string][]string
}

func (v *Validation) Error() string { return "validation failed" }

// NewValidation builds an empty Validation error.
func NewValidation() *Validation { return &Validation{Fields: map[string][]string{}} }

// Add appends a message for a field.
func (v *Validation) Add(field, msg string) {
	v.Fields[field] = append(v.Fields[field], msg)
}

// HasErrors reports whether any field failed.
func (v *Validation) HasErrors() bool { return len(v.Fields) > 0 }

// OrNil returns nil when there are no field errors, else the receiver.
func (v *Validation) OrNil() error {
	if v.HasErrors() {
		return v
	}
	return nil
}

// Forbidden carries a specific human detail (e.g. "event is not running").
// It reports Is(ErrForbidden) so callers can check the sentinel.
type Forbidden struct{ Detail string }

func (e *Forbidden) Error() string             { return "forbidden: " + e.Detail }
func (e *Forbidden) Is(t error) bool           { return t == ErrForbidden }
func Forbiddenf(f string, a ...any) *Forbidden { return &Forbidden{Detail: fmt.Sprintf(f, a...)} }

// Conflict carries a specific human detail (e.g. "team name already taken").
type Conflict struct{ Detail string }

func (e *Conflict) Error() string            { return "conflict: " + e.Detail }
func (e *Conflict) Is(t error) bool          { return t == ErrConflict }
func Conflictf(f string, a ...any) *Conflict { return &Conflict{Detail: fmt.Sprintf(f, a...)} }

// NotFound carries a specific detail; renders 404.
type NotFound struct{ Detail string }

func (e *NotFound) Error() string   { return "not found: " + e.Detail }
func (e *NotFound) Is(t error) bool { return t == ErrNotFound }

// RateLimited signals a 429 with a Retry-After. Scope labels the metric.
type RateLimited struct {
	RetryAfter time.Duration
	Scope      string
}

func (e *RateLimited) Error() string {
	return fmt.Sprintf("rate limited (%s), retry after %s", e.Scope, e.RetryAfter)
}
