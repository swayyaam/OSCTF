// Package handlers implements the generated strict-server interface. It maps
// HTTP requests to service calls and domain errors back to problem+json; it holds
// no business logic. Methods below are replaced milestone by milestone; anything
// still unimplemented returns ErrNotImplemented, rendered as a 501.
package handlers

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/redisx"
	"github.com/osctf/platform/internal/runtime"
	"github.com/osctf/platform/internal/scheduler"
	"github.com/osctf/platform/internal/scoreboard"
	"github.com/osctf/platform/internal/submissions"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/users"
)

// ErrNotImplemented marks an endpoint whose milestone has not landed yet.
// The HTTP error handler renders it as a 501 problem.
var ErrNotImplemented = errors.New("not implemented")

// Deps are the services and helpers the handlers dispatch to. Fields are added
// as milestones land; nil fields leave their endpoints on the 501 stub.
type Deps struct {
	Users       *users.Service
	Teams       *teams.Service
	Events      *events.Service
	Challenges  *challenges.Service
	Submissions *submissions.Service
	Scoreboard  *scoreboard.Service
	Runtime     *runtime.Manager
	Scheduler   *scheduler.Scheduler
	Auth        *auth.Registry
	Sessions    *auth.SessionStore
	Limiter     *redisx.Limiter
	Audit       *audit.Logger

	// Recompute triggers a scoreboard recompute + broadcast; wired in M5/M6.
	// Nil is a no-op, so earlier milestones compile and run.
	Recompute func(context.Context)

	// Log is used for safety-net logging (e.g. serving cached freeze state when the
	// events read fails). Nil falls back to slog.Default().
	Log *slog.Logger

	// SecureCookies mirrors OSCTF_BASE_URL's scheme; TrustProxy gates
	// X-Forwarded-For handling; SessionTTL sizes the cookie Max-Age.
	SecureCookies bool
	TrustProxy    bool
	// EmailLoginDisabled gates POST /auth/login (the built-in email/password path). Stored
	// in the "disabled" sense so the zero value is the safe, common default (enabled) — a
	// Deps built without setting it keeps email login on. True only for an SSO-only
	// deployment; see config.AuthEmailLogin.
	EmailLoginDisabled bool
	SessionTTL         time.Duration
	MaxAttachmentMB    int

	// RegisterIPBurst / RegisterIPWindow bound anonymous sign-ups per client IP. The
	// default is generous because a venue registers many players from one NAT at once;
	// a zero burst disables the limit. See internal/config.
	RegisterIPBurst  int
	RegisterIPWindow time.Duration

	// LoginIPBurst / LoginIPWindow bound login attempts per client IP. Generous by
	// default so a shared-NAT venue is not throttled at event start (issue #4); the
	// per-account limit is the credential-stuffing guard. A zero burst disables it.
	LoginIPBurst  int
	LoginIPWindow time.Duration
}

// Server implements apigen.StrictServerInterface.
type Server struct {
	d Deps
	// lastFreeze caches the most recent successful freeze decision so a transient
	// events-read failure can hold the last known state instead of flipping the
	// visibility of every team's post-freeze solves at once (see guards.go).
	lastFreeze atomic.Pointer[freezeSnapshot]
}

// New builds the handler set.
func New(d Deps) *Server { return &Server{d: d} }

// logger returns the configured logger, or the process default when unset.
func (s *Server) logger() *slog.Logger {
	if s.d.Log != nil {
		return s.d.Log
	}
	return slog.Default()
}

var _ apigen.StrictServerInterface = (*Server)(nil)
