package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/db/gen"
)

// TokenPrefix marks an OSCTF personal access token. It makes tokens greppable — good for
// secret scanners, and the reason a leaked one must never reach a log/response/audit/metric.
const TokenPrefix = "osctf_pat_" //nolint:gosec // G101: a public prefix marker, not a secret

const (
	tokenRandBytes = 32 // 256 bits of entropy
	tokenPrefixLen = 12 // chars of the base32 body kept for display + the auth lookup probe (~60 bits)
)

// Coarse scopes (v0.3). read = GET, submit = participant mutations, admin = admin routes.
const (
	ScopeRead   = "read"
	ScopeSubmit = "submit"
	ScopeAdmin  = "admin"
)

var validScopes = map[string]bool{ScopeRead: true, ScopeSubmit: true, ScopeAdmin: true}

// ValidateScopes returns an error naming the first unknown scope. Enforced at create time
// AND at auth time: a token carrying an unknown scope is rejected (fail closed), never
// silently treated as scopeless-and-allowed.
func ValidateScopes(scopes []string) error {
	for _, s := range scopes {
		if !validScopes[s] {
			return fmt.Errorf("unknown scope %q", s)
		}
	}
	return nil
}

var (
	// ErrInvalidToken covers a malformed, unknown, or revoked token — all indistinguishable.
	ErrInvalidToken = errors.New("auth: invalid token")
	ErrTokenExpired = errors.New("auth: token expired")
	ErrUserBanned   = errors.New("auth: token owner is banned")
	// ErrTokenScope means the token carries a scope the server does not recognise — fail closed.
	ErrTokenScope = errors.New("auth: token carries an unrecognized scope")
)

// tokenEnc encodes the random body without padding, so the token is a clean alnum string.
var tokenEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// dummyTokenHash is compared against on a lookup miss so the miss path performs the same
// constant-time comparison as a wrong-secret hit — no prefix timing/enumeration oracle.
const dummyTokenHash = "0000000000000000000000000000000000000000000000000000000000000000"

// GenerateToken returns a new plaintext token (shown to the user exactly once), its sha-256
// hash (stored), and its display/lookup prefix (stored).
func GenerateToken() (plaintext, hash, prefix string, err error) {
	b := make([]byte, tokenRandBytes)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("auth: generating token: %w", err)
	}
	body := tokenEnc.EncodeToString(b)
	plaintext = TokenPrefix + body
	return plaintext, hashToken(plaintext), body[:tokenPrefixLen], nil
}

func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// parsePresented derives the (prefix, full-hash) probe from a presented token, or ok=false if
// it isn't shaped like one of ours (a garbage Authorization value fails fast and uniformly).
func parsePresented(raw string) (prefix, hash string, ok bool) {
	body, found := strings.CutPrefix(raw, TokenPrefix)
	if !found || len(body) < tokenPrefixLen {
		return "", "", false
	}
	return body[:tokenPrefixLen], hashToken(raw), true
}

// TokenService issues and resolves API tokens.
type TokenService struct {
	q *gen.Queries
}

// NewTokenService builds a token service over the given queries.
func NewTokenService(q *gen.Queries) *TokenService { return &TokenService{q: q} }

// TokenMeta is a token's non-secret metadata (never carries the plaintext or hash).
type TokenMeta struct {
	ID         uuid.UUID
	Name       string
	Prefix     string
	Scopes     []string
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	CreatedAt  time.Time
}

func metaOf(t gen.ApiToken) TokenMeta {
	return TokenMeta{
		ID: t.ID, Name: t.Name, Prefix: t.Prefix, Scopes: t.Scopes,
		LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt, CreatedAt: t.CreatedAt,
	}
}

// Create issues a token for userID and returns the plaintext (once) plus its metadata.
func (s *TokenService) Create(ctx context.Context, userID uuid.UUID, name string, scopes []string, expiresAt *time.Time) (plaintext string, meta TokenMeta, err error) {
	if err := ValidateScopes(scopes); err != nil {
		return "", TokenMeta{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", TokenMeta{}, fmt.Errorf("auth: token id: %w", err)
	}
	plaintext, hash, prefix, err := GenerateToken()
	if err != nil {
		return "", TokenMeta{}, err
	}
	row, err := s.q.CreateAPIToken(ctx, gen.CreateAPITokenParams{
		ID: id, UserID: userID, Name: name, TokenHash: hash, Prefix: prefix,
		Scopes: scopes, ExpiresAt: expiresAt,
	})
	if err != nil {
		return "", TokenMeta{}, fmt.Errorf("auth: creating token: %w", err)
	}
	return plaintext, metaOf(row), nil
}

// Authenticate resolves a presented bearer token to an Identity with the owner's LIVE role.
// Lookup is by prefix + constant-time hash comparison; role and ban are read per request
// (there is NO cache), so revocation, expiry, ban, and demotion all take effect on the next
// request. An unknown scope on the token fails closed.
func (s *TokenService) Authenticate(ctx context.Context, raw string, now time.Time) (Identity, error) {
	prefix, hash, ok := parsePresented(raw)
	if !ok {
		subtle.ConstantTimeCompare([]byte(dummyTokenHash), []byte(dummyTokenHash)) // equalize with the miss path
		return Identity{}, ErrInvalidToken
	}
	cands, err := s.q.GetAPITokensByPrefix(ctx, prefix)
	if err != nil {
		return Identity{}, err
	}
	matchIdx := -1
	for i := range cands {
		if subtle.ConstantTimeCompare([]byte(hash), []byte(cands[i].TokenHash)) == 1 {
			matchIdx = i
		}
	}
	if matchIdx < 0 {
		if len(cands) == 0 {
			subtle.ConstantTimeCompare([]byte(hash), []byte(dummyTokenHash)) // one compare on the no-candidate miss too
		}
		return Identity{}, ErrInvalidToken
	}
	tok := cands[matchIdx]
	if tok.ExpiresAt != nil && !tok.ExpiresAt.After(now) {
		return Identity{}, ErrTokenExpired
	}
	if err := ValidateScopes(tok.Scopes); err != nil {
		return Identity{}, ErrTokenScope // fail closed on version skew / manual edit
	}
	// Resolve the owner's role + ban LIVE, every request. This check is LOAD-BEARING: it is
	// what makes a ban/demotion/revocation take effect on the next request. Banning also
	// deletes token rows (defense-in-depth), but that deletion is NOT the guarantee — do not
	// add a token cache assuming "banned tokens are deleted anyway"; that reintroduces a
	// window equal to the cache TTL. The live read is the guarantee.
	u, err := s.q.GetUserByID(ctx, tok.UserID)
	if err != nil {
		return Identity{}, ErrInvalidToken
	}
	if u.Banned {
		return Identity{}, ErrUserBanned
	}
	return Identity{
		UserID:  u.ID,
		Role:    u.Role,
		Method:  AuthToken,
		Scopes:  append([]string(nil), tok.Scopes...),
		TokenID: tok.ID,
	}, nil
}

// Touch records last-used, best-effort (the caller ignores errors — it must not fail a request).
func (s *TokenService) Touch(ctx context.Context, id uuid.UUID) error {
	return s.q.TouchAPIToken(ctx, id)
}

// List returns the caller's tokens (metadata only).
func (s *TokenService) List(ctx context.Context, userID uuid.UUID) ([]TokenMeta, error) {
	rows, err := s.q.ListAPITokensByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("auth: listing tokens: %w", err)
	}
	out := make([]TokenMeta, 0, len(rows))
	for _, r := range rows {
		out = append(out, metaOf(r))
	}
	return out, nil
}

// Revoke deletes one of userID's tokens. Returns false if it wasn't theirs / doesn't exist.
func (s *TokenService) Revoke(ctx context.Context, userID, tokenID uuid.UUID) (bool, error) {
	n, err := s.q.DeleteAPIToken(ctx, gen.DeleteAPITokenParams{ID: tokenID, UserID: userID})
	if err != nil {
		return false, fmt.Errorf("auth: revoking token: %w", err)
	}
	return n > 0, nil
}

// DeleteForUser disables all of a user's tokens (the ban hook; token equivalent of
// DeleteAllForUser for sessions).
func (s *TokenService) DeleteForUser(ctx context.Context, userID uuid.UUID) error {
	return s.q.DeleteAPITokensForUser(ctx, userID)
}

// TokenAdminMeta is a token's metadata plus its owner (for the admin cross-user view).
type TokenAdminMeta struct {
	TokenMeta
	UserID   uuid.UUID
	Username string
}

// ListAll returns a page of every token with its owner (admin view; metadata only).
func (s *TokenService) ListAll(ctx context.Context, limit, offset int32) ([]TokenAdminMeta, int64, error) {
	rows, err := s.q.ListAllAPITokens(ctx, gen.ListAllAPITokensParams{Limit: limit, Offset: offset})
	if err != nil {
		return nil, 0, fmt.Errorf("auth: listing all tokens: %w", err)
	}
	total, err := s.q.CountAllAPITokens(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("auth: counting tokens: %w", err)
	}
	out := make([]TokenAdminMeta, 0, len(rows))
	for _, r := range rows {
		out = append(out, TokenAdminMeta{TokenMeta: metaOf(r.ApiToken), UserID: r.UserID, Username: r.Username})
	}
	return out, total, nil
}

// RevokeAny deletes a token by id regardless of owner (admin revoke). Returns false if none.
func (s *TokenService) RevokeAny(ctx context.Context, tokenID uuid.UUID) (bool, error) {
	n, err := s.q.DeleteAPITokenByID(ctx, tokenID)
	if err != nil {
		return false, fmt.Errorf("auth: admin revoking token: %w", err)
	}
	return n > 0, nil
}
