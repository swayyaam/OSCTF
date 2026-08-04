package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/httpx"
	"github.com/osctf/platform/internal/metrics"
	"github.com/osctf/platform/internal/users"
)

// clientIP pulls the caller IP from the stashed request.
func (s *Server) clientIP(ctx context.Context) string {
	if p, ok := httpx.HTTPFrom(ctx); ok {
		return httpx.ClientIP(p.R, s.d.TrustProxy)
	}
	return ""
}

func (s *Server) userAgent(ctx context.Context) string {
	if p, ok := httpx.HTTPFrom(ctx); ok {
		return p.R.UserAgent()
	}
	return ""
}

// setSessionCookie writes the session cookie onto the stashed ResponseWriter.
func (s *Server) setSessionCookie(ctx context.Context, token string, maxAge time.Duration) {
	p, ok := httpx.HTTPFrom(ctx)
	if !ok {
		return
	}
	//nolint:gosec // G124: Secure tracks the deployment scheme (OSCTF_BASE_URL);
	// plain HTTP is a supported LAN/dev deployment. HttpOnly + SameSite=Lax are set.
	http.SetCookie(p.W, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.d.SecureCookies,
		MaxAge:   int(maxAge.Seconds()),
	})
}

func (s *Server) clearSessionCookie(ctx context.Context) {
	s.setSessionCookie(ctx, "", -time.Second)
}

// limit runs one rate-limit check, translating denial to the domain error.
func (s *Server) limit(ctx context.Context, scope, key string, n int, window time.Duration) error {
	if s.d.Limiter == nil {
		return nil
	}
	allowed, retryAfter, err := s.d.Limiter.Allow(ctx, scope, key, n, window)
	if err != nil {
		return fmt.Errorf("rate limit check (%s): %w", scope, err)
	}
	if !allowed {
		metrics.RateLimitRejections.WithLabelValues(scope).Inc()
		return &apperr.RateLimited{RetryAfter: retryAfter, Scope: scope}
	}
	return nil
}

// mePayload assembles the /auth/me response for a user.
func (s *Server) mePayload(ctx context.Context, u gen.User) (apigen.Me, error) {
	team, err := s.d.Users.TeamOf(ctx, u.ID)
	if err != nil {
		return apigen.Me{}, err
	}
	me := apigen.Me{
		Id:       u.ID,
		Username: u.Username,
		Email:    u.Email,
		Role:     apigen.Role(u.Role),
	}
	if team != nil {
		me.Team = &apigen.MyTeamRef{
			Id:   team.ID,
			Name: team.Name,
			Role: apigen.MyTeamRefRole(team.Role),
		}
	}
	return me, nil
}

// Register creates an account and starts a session (auto-login).
func (s *Server) Register(ctx context.Context, request apigen.RegisterRequestObject) (apigen.RegisterResponseObject, error) {
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	ip := s.clientIP(ctx)
	if s.d.RegisterIPBurst > 0 {
		if err := s.limit(ctx, "register-ip", ip, s.d.RegisterIPBurst, s.d.RegisterIPWindow); err != nil {
			return nil, err
		}
	}
	u, err := s.d.Users.Register(ctx, users.RegisterInput{
		Username: request.Body.Username,
		Email:    string(request.Body.Email),
		Password: request.Body.Password,
	})
	if err != nil {
		return nil, err
	}
	sess, err := s.d.Sessions.Create(ctx, u.ID, u.Role, ip, s.userAgent(ctx))
	if err != nil {
		return nil, err
	}
	s.setSessionCookie(ctx, sess.Token, s.d.SessionTTL)
	me, err := s.mePayload(ctx, u)
	if err != nil {
		return nil, err
	}
	return apigen.Register201JSONResponse(me), nil
}

// Login authenticates and rotates in a fresh session.
func (s *Server) Login(ctx context.Context, request apigen.LoginRequestObject) (apigen.LoginResponseObject, error) {
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	email := strings.ToLower(string(request.Body.Email))
	ip := s.clientIP(ctx)

	// Rate limits run before any DB work (docs/v0.1/06-auth.md). The per-IP limit is
	// generous and configurable so a shared-NAT venue is not throttled at event
	// start (issue #4); the per-account limit is the credential-stuffing guard.
	if s.d.LoginIPBurst > 0 {
		if err := s.limit(ctx, "login-ip", ip, s.d.LoginIPBurst, s.d.LoginIPWindow); err != nil {
			return nil, err
		}
	}
	acctHash := sha256.Sum256([]byte(email))
	if err := s.limit(ctx, "login-acct", hex.EncodeToString(acctHash[:]), 5, 5*time.Minute); err != nil {
		return nil, err
	}

	userID, err := s.d.Auth.Authenticate(ctx, email, request.Body.Password)
	if err != nil {
		if errors.Is(err, apperr.ErrUnavailable) {
			return nil, err // hashing gate shed this login: 503 + Retry-After, not a misleading 401
		}
		// Any other failure is the same generic 401.
		return nil, apperr.ErrUnauthenticated
	}
	u, err := s.d.Users.Get(ctx, userID)
	if err != nil {
		return nil, apperr.ErrUnauthenticated
	}
	sess, err := s.d.Sessions.Create(ctx, u.ID, u.Role, ip, s.userAgent(ctx))
	if err != nil {
		return nil, err
	}
	// Close the ban-during-login race: a ban that committed after Authenticate would
	// have run DeleteAllForUser before this session existed. Re-check now that the
	// token is indexed; if banned, revoke the just-created session and reject. (Login
	// is not the hot path, so the extra read is fine.)
	if fresh, gerr := s.d.Users.Get(ctx, u.ID); gerr == nil && fresh.Banned {
		_ = s.d.Sessions.Delete(ctx, sess.Token)
		return nil, apperr.ErrUnauthenticated
	}
	s.setSessionCookie(ctx, sess.Token, s.d.SessionTTL)
	me, err := s.mePayload(ctx, u)
	if err != nil {
		return nil, err
	}
	return apigen.Login200JSONResponse(me), nil
}

// Logout destroys the current session.
func (s *Server) Logout(ctx context.Context, _ apigen.LogoutRequestObject) (apigen.LogoutResponseObject, error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return nil, apperr.ErrUnauthenticated
	}
	if err := s.d.Sessions.Delete(ctx, id.SessionToken); err != nil {
		return nil, err
	}
	s.clearSessionCookie(ctx)
	return apigen.Logout204Response{}, nil
}

// GetMe returns the current account.
func (s *Server) GetMe(ctx context.Context, _ apigen.GetMeRequestObject) (apigen.GetMeResponseObject, error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return nil, apperr.ErrUnauthenticated
	}
	u, err := s.d.Users.Get(ctx, id.UserID)
	if err != nil {
		return nil, apperr.ErrUnauthenticated
	}
	me, err := s.mePayload(ctx, u)
	if err != nil {
		return nil, err
	}
	return apigen.GetMe200JSONResponse(me), nil
}

// ChangePassword swaps the caller's password, keeping only this session alive.
func (s *Server) ChangePassword(ctx context.Context, request apigen.ChangePasswordRequestObject) (apigen.ChangePasswordResponseObject, error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return nil, apperr.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	err := s.d.Users.ChangePassword(ctx, id.UserID, request.Body.CurrentPassword, request.Body.NewPassword, id.SessionToken)
	if err != nil {
		return nil, err
	}
	return apigen.ChangePassword204Response{}, nil
}
