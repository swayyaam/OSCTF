package handlers

import (
	"context"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/db/gen"
)

// recompute triggers a scoreboard recompute + broadcast when wired (M5/M6).
func (s *Server) recompute(ctx context.Context) {
	if s.d.Recompute != nil {
		s.d.Recompute(ctx)
	}
}

// requireUser returns the caller identity or ErrUnauthenticated.
func requireUser(ctx context.Context) (auth.Identity, error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return auth.Identity{}, apperr.ErrUnauthenticated
	}
	return id, nil
}

// requireAdmin resolves the caller and re-reads the user row so promotion,
// demotion, and bans take effect immediately (not on session expiry).
func (s *Server) requireAdmin(ctx context.Context) (gen.User, error) {
	id, err := requireUser(ctx)
	if err != nil {
		return gen.User{}, err
	}
	u, err := s.d.Users.Get(ctx, id.UserID)
	if err != nil {
		return gen.User{}, apperr.ErrUnauthenticated
	}
	if u.Banned || u.Role != "admin" {
		return gen.User{}, apperr.Forbiddenf("admin access required")
	}
	return u, nil
}
