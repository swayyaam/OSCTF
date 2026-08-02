package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
)

// ErrInvalidCredentials is the single failure mode of Authenticate — callers
// must not learn whether the account exists, is banned, or had a wrong password.
var ErrInvalidCredentials = errors.New("auth: invalid credentials")

// AuthProvider authenticates credentials and yields a platform user identity.
// v0.1 implementation: EmailPasswordProvider. Future: OAuth, LDAP, SAML plugins.
type AuthProvider interface {
	// Name returns a stable identifier, e.g. "email".
	Name() string
	// Authenticate verifies credentials and returns the user ID.
	// Returns ErrInvalidCredentials on any failure (no enumeration).
	Authenticate(ctx context.Context, identifier, secret string) (userID uuid.UUID, err error)
}

// EmailPasswordProvider verifies an email + password against the users table.
type EmailPasswordProvider struct {
	q *gen.Queries
	// rehash is called after a successful login whose stored hash uses outdated
	// parameters; nil disables transparent re-hashing.
	rehash func(ctx context.Context, userID uuid.UUID, newHash string)
}

// NewEmailPasswordProvider builds the v0.1 provider.
func NewEmailPasswordProvider(q *gen.Queries, rehash func(ctx context.Context, userID uuid.UUID, newHash string)) *EmailPasswordProvider {
	return &EmailPasswordProvider{q: q, rehash: rehash}
}

// Name implements AuthProvider.
func (p *EmailPasswordProvider) Name() string { return "email" }

// Authenticate implements AuthProvider with timing uniformity: an unknown email
// still burns one argon2 verification before returning the generic error.
func (p *EmailPasswordProvider) Authenticate(ctx context.Context, email, password string) (uuid.UUID, error) {
	u, err := p.q.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Burn a hash for timing uniformity. If the gate sheds it, return the
			// same 503 the known-email path would, so overload doesn't turn into a
			// known/unknown-email status oracle.
			if berr := BurnHash(ctx, password); errors.Is(berr, apperr.ErrUnavailable) {
				return uuid.Nil, berr
			}
			return uuid.Nil, ErrInvalidCredentials
		}
		return uuid.Nil, fmt.Errorf("auth: looking up user: %w", err)
	}
	ok, err := VerifyPassword(ctx, password, u.PasswordHash)
	if errors.Is(err, apperr.ErrUnavailable) {
		return uuid.Nil, err // shed under load: propagate the 503, not a generic 401
	}
	if err != nil || !ok {
		return uuid.Nil, ErrInvalidCredentials
	}
	if u.Banned {
		// Same generic error — bans are not advertised at login.
		return uuid.Nil, ErrInvalidCredentials
	}
	if p.rehash != nil && NeedsRehash(u.PasswordHash) {
		if newHash, herr := HashPassword(ctx, password); herr == nil {
			p.rehash(ctx, u.ID, newHash)
		}
	}
	return u.ID, nil
}
