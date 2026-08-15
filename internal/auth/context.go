package auth

import (
	"context"

	"github.com/google/uuid"
)

// AuthMethod records how a request authenticated. Scope enforcement applies only to token
// auth; a session carries the caller's full role.
type AuthMethod string

const (
	AuthSession AuthMethod = "session"
	AuthToken   AuthMethod = "token"
)

// Identity is the authenticated caller attached to a request context by the auth
// middleware. For session auth, Role comes from the session (cheap checks) and admin
// endpoints re-read the user row. For token auth, Role is resolved LIVE per request and
// Scopes/TokenID are set.
type Identity struct {
	UserID       uuid.UUID
	Role         string
	SessionToken string     // session auth only
	Method       AuthMethod // how this request authenticated
	Scopes       []string   // token auth only — the granted scope set
	TokenID      uuid.UUID  // token auth only — for rate-limit keying and audit
}

// IsAdmin reports whether the session role is admin (session-cached; re-check
// against the DB for admin endpoints).
func (id Identity) IsAdmin() bool { return id.Role == "admin" }

type ctxKey int

const identityKey ctxKey = iota

// WithIdentity attaches the identity to the context.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// IdentityFrom returns the caller identity, if any.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey).(Identity)
	return id, ok
}
