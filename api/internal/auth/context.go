package auth

import (
	"context"

	"github.com/google/uuid"
)

// Identity is the authenticated caller attached to a request context by the
// session middleware. Role comes from the session (cheap checks); admin
// endpoints must re-read the user row (docs/v0.1/06-auth.md).
type Identity struct {
	UserID       uuid.UUID
	Role         string
	SessionToken string
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
