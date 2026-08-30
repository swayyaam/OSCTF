package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/swayyaam/OSCTF/internal/apigen"
	"github.com/swayyaam/OSCTF/internal/apperr"
	"github.com/swayyaam/OSCTF/internal/auth"
	"github.com/swayyaam/OSCTF/internal/db/gen"
	"github.com/swayyaam/OSCTF/internal/httpx"
	"github.com/swayyaam/OSCTF/internal/metrics"
	"github.com/swayyaam/OSCTF/internal/users"
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
		// Redis is down: FAIL CLOSED. Credential and mutation paths must not run without a working
		// limiter — an outage silently removing the login/register/submit throttle (incl. the
		// credential-stuffing backstop) is exactly when it is most wanted. Return 503, not 500: 500
		// tells a client its request was wrong; 503 + Retry-After tells it to come back, which
		// automation handles differently. Logged + counted DISTINCTLY so an operator can tell
		// "Redis is down" from "you are being throttled".
		metrics.RateLimiterUnavailable.WithLabelValues(scope).Inc()
		s.logger().Warn("rate limiter unavailable (Redis) — failing the request closed with 503",
			"scope", scope, "error", err.Error())
		return &apperr.Unavailable{Detail: "the rate limiter is temporarily unavailable; please retry shortly", RetryAfter: limiterUnavailableRetry}
	}
	if !allowed {
		metrics.RateLimitRejections.WithLabelValues(scope).Inc()
		return &apperr.RateLimited{RetryAfter: retryAfter, Scope: scope}
	}
	return nil
}

// limiterUnavailableRetry is the Retry-After surfaced when the limiter backend is down — short, so a
// client comes back quickly once Redis recovers, not the (possibly long) rate-limit window.
const limiterUnavailableRetry = 3 * time.Second

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
	// Scopes belong to the CREDENTIAL, not the account, so they appear only when a token was
	// presented. A session has none — it carries the account's full role — and reporting an
	// empty list there would read as "no permissions" rather than "not applicable".
	if id, ok := auth.IdentityFrom(ctx); ok && len(id.Scopes) > 0 {
		scopes := make([]apigen.TokenScope, 0, len(id.Scopes))
		for _, s := range id.Scopes {
			scopes = append(scopes, apigen.TokenScope(s))
		}
		me.Scopes = &scopes
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
	// Email/password login can be disabled for an SSO-only deployment. This is a global
	// config, not per-account, so a clear error is fine (no enumeration concern).
	if s.d.EmailLoginDisabled {
		return nil, apperr.Forbiddenf("email/password login is disabled on this deployment; use your configured identity provider")
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

	userID, err := s.d.Auth.Default().Authenticate(ctx, email, request.Body.Password)
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
