package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/pagination"
)

// requireSession is requireUser plus a session-only gate: token-management operations reject
// a bearer credential even for a privileged owner, so a leaked token can't mint or revoke
// tokens and thereby self-perpetuate past its own revocation (see docs/v0.3/06-api-v1.md).
func requireSession(ctx context.Context) (auth.Identity, error) {
	id, err := requireUser(ctx)
	if err != nil {
		return auth.Identity{}, err
	}
	if id.Method != auth.AuthSession {
		return auth.Identity{}, apperr.Forbiddenf("token management requires a session; an API token cannot manage tokens")
	}
	return id, nil
}

func toScopes(ss []string) []apigen.TokenScope {
	out := make([]apigen.TokenScope, 0, len(ss))
	for _, s := range ss {
		out = append(out, apigen.TokenScope(s))
	}
	return out
}

func toToken(m auth.TokenMeta) apigen.Token {
	return apigen.Token{
		Id: m.ID, Prefix: m.Prefix, Name: m.Name, Scopes: toScopes(m.Scopes),
		CreatedAt: m.CreatedAt, LastUsedAt: m.LastUsedAt, ExpiresAt: m.ExpiresAt,
	}
}

// CreateToken issues an API token for the calling user (session only). The plaintext is in
// this response and nowhere else.
func (s *Server) CreateToken(ctx context.Context, request apigen.CreateTokenRequestObject) (apigen.CreateTokenResponseObject, error) {
	id, err := requireSession(ctx)
	if err != nil {
		return nil, err
	}
	if s.d.Tokens == nil {
		return nil, ErrNotImplemented
	}
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	b := request.Body
	scopes := make([]string, len(b.Scopes))
	for i, sc := range b.Scopes {
		scopes[i] = string(sc)
	}
	v := apperr.NewValidation()
	if len(b.Name) < 1 || len(b.Name) > 100 {
		v.Add("name", "must be between 1 and 100 characters")
	}
	if len(scopes) == 0 {
		v.Add("scopes", "at least one scope is required")
	} else if serr := auth.ValidateScopes(scopes); serr != nil {
		v.Add("scopes", serr.Error())
	}
	ttl := s.d.TokenDefaultTTL
	if b.ExpiresInDays != nil {
		if *b.ExpiresInDays < 1 {
			v.Add("expires_in_days", "must be a positive number of days")
		} else {
			ttl = time.Duration(*b.ExpiresInDays) * 24 * time.Hour
			if s.d.TokenMaxTTL > 0 && ttl > s.d.TokenMaxTTL {
				v.Add("expires_in_days", fmt.Sprintf("exceeds the maximum of %d days", int(s.d.TokenMaxTTL.Hours())/24))
			}
		}
	}
	if err := v.OrNil(); err != nil {
		return nil, err
	}
	expiresAt := time.Now().Add(ttl)
	plaintext, meta, err := s.d.Tokens.Create(ctx, id.UserID, b.Name, scopes, &expiresAt)
	if err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, id.UserID, "token.create", "api_token", meta.ID.String(), map[string]any{"name": b.Name, "scopes": scopes})
	t := toToken(meta)
	return apigen.CreateToken201JSONResponse(apigen.TokenCreated{
		Id: t.Id, Prefix: t.Prefix, Name: t.Name, Scopes: t.Scopes,
		CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt,
		Token: plaintext,
	}), nil
}

// ListTokens returns the caller's tokens (session only; metadata only).
func (s *Server) ListTokens(ctx context.Context, _ apigen.ListTokensRequestObject) (apigen.ListTokensResponseObject, error) {
	id, err := requireSession(ctx)
	if err != nil {
		return nil, err
	}
	if s.d.Tokens == nil {
		return nil, ErrNotImplemented
	}
	metas, err := s.d.Tokens.List(ctx, id.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.Token, 0, len(metas))
	for _, m := range metas {
		out = append(out, toToken(m))
	}
	return apigen.ListTokens200JSONResponse(out), nil
}

// RevokeToken revokes one of the caller's tokens (session only). Revoking a token that isn't
// the caller's is a 404 — indistinguishable from a nonexistent id.
func (s *Server) RevokeToken(ctx context.Context, request apigen.RevokeTokenRequestObject) (apigen.RevokeTokenResponseObject, error) {
	id, err := requireSession(ctx)
	if err != nil {
		return nil, err
	}
	if s.d.Tokens == nil {
		return nil, ErrNotImplemented
	}
	ok, err := s.d.Tokens.Revoke(ctx, id.UserID, request.Id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.ErrNotFound
	}
	s.d.Audit.Log(ctx, id.UserID, "token.revoke", "api_token", request.Id.String(), nil)
	return apigen.RevokeToken204Response{}, nil
}

// AdminListTokens returns a page of every token with its owner (admin; metadata only).
func (s *Server) AdminListTokens(ctx context.Context, request apigen.AdminListTokensRequestObject) (apigen.AdminListTokensResponseObject, error) {
	if _, err := s.requireSessionAdmin(ctx); err != nil {
		return nil, err
	}
	if s.d.Tokens == nil {
		return nil, ErrNotImplemented
	}
	p := pagination.Normalize(request.Params.Page, request.Params.PerPage)
	metas, total, err := s.d.Tokens.ListAll(ctx, p.Limit(), p.Offset())
	if err != nil {
		return nil, err
	}
	items := make([]apigen.TokenAdmin, 0, len(metas))
	for _, m := range metas {
		items = append(items, apigen.TokenAdmin{
			Id: m.ID, Prefix: m.Prefix, Name: m.Name, Scopes: toScopes(m.Scopes),
			CreatedAt: m.CreatedAt, LastUsedAt: m.LastUsedAt, ExpiresAt: m.ExpiresAt,
			User: apigen.Member{Id: m.UserID, Username: m.Username},
		})
	}
	return apigen.AdminListTokens200JSONResponse(apigen.TokenAdminPage{
		Items: items, Total: int(total), Page: p.Page, PerPage: p.PerPage,
	}), nil
}

// AdminRevokeToken revokes any user's token (admin).
func (s *Server) AdminRevokeToken(ctx context.Context, request apigen.AdminRevokeTokenRequestObject) (apigen.AdminRevokeTokenResponseObject, error) {
	actor, err := s.requireSessionAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if s.d.Tokens == nil {
		return nil, ErrNotImplemented
	}
	ok, err := s.d.Tokens.RevokeAny(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.ErrNotFound
	}
	s.d.Audit.Log(ctx, actor.ID, "token.admin_revoke", "api_token", request.Id.String(), nil)
	return apigen.AdminRevokeToken204Response{}, nil
}

// requireSessionAdmin is requireAdmin plus the session-only gate for token management.
func (s *Server) requireSessionAdmin(ctx context.Context) (gen.User, error) {
	if _, err := requireSession(ctx); err != nil {
		return gen.User{}, err
	}
	return s.requireAdmin(ctx)
}
