// Package handlers implements the generated strict-server interface. It maps
// HTTP requests to service calls and domain errors back to problem+json; it holds
// no business logic. Methods below are replaced milestone by milestone; anything
// still unimplemented returns ErrNotImplemented, rendered as a 501.
package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/redisx"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/users"
)

// ErrNotImplemented marks an endpoint whose milestone has not landed yet.
// The HTTP error handler renders it as a 501 problem.
var ErrNotImplemented = errors.New("not implemented")

// Deps are the services and helpers the handlers dispatch to. Fields are added
// as milestones land; nil fields leave their endpoints on the 501 stub.
type Deps struct {
	Users      *users.Service
	Teams      *teams.Service
	Events     *events.Service
	Challenges *challenges.Service
	Auth       auth.AuthProvider
	Sessions   *auth.SessionStore
	Limiter    *redisx.Limiter
	Audit      *audit.Logger

	// Recompute triggers a scoreboard recompute + broadcast; wired in M5/M6.
	// Nil is a no-op, so earlier milestones compile and run.
	Recompute func(context.Context)

	// SecureCookies mirrors OSCTF_BASE_URL's scheme; TrustProxy gates
	// X-Forwarded-For handling; SessionTTL sizes the cookie Max-Age.
	SecureCookies   bool
	TrustProxy      bool
	SessionTTL      time.Duration
	MaxAttachmentMB int
}

// Server implements apigen.StrictServerInterface.
type Server struct {
	d Deps
}

// New builds the handler set.
func New(d Deps) *Server { return &Server{d: d} }

var _ apigen.StrictServerInterface = (*Server)(nil)

func (s *Server) AdminDestroyInstance(ctx context.Context, request apigen.AdminDestroyInstanceRequestObject) (apigen.AdminDestroyInstanceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminGetInstance(ctx context.Context, request apigen.AdminGetInstanceRequestObject) (apigen.AdminGetInstanceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminDeployInstance(ctx context.Context, request apigen.AdminDeployInstanceRequestObject) (apigen.AdminDeployInstanceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminGetInstanceLogs(ctx context.Context, request apigen.AdminGetInstanceLogsRequestObject) (apigen.AdminGetInstanceLogsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminRestartInstance(ctx context.Context, request apigen.AdminRestartInstanceRequestObject) (apigen.AdminRestartInstanceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminGetStats(ctx context.Context, request apigen.AdminGetStatsRequestObject) (apigen.AdminGetStatsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminListSubmissions(ctx context.Context, request apigen.AdminListSubmissionsRequestObject) (apigen.AdminListSubmissionsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) SubmitFlag(ctx context.Context, request apigen.SubmitFlagRequestObject) (apigen.SubmitFlagResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) GetScoreboard(ctx context.Context, request apigen.GetScoreboardRequestObject) (apigen.GetScoreboardResponseObject, error) {
	return nil, ErrNotImplemented
}
