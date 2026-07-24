package users

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/pagination"
)

// AdminListFilters narrows the admin user list.
type AdminListFilters struct {
	Query  *string
	Banned *bool
	Hidden *bool
	Role   *string
}

// AdminListResult is a page of users with their team refs.
type AdminListResult struct {
	Items []gen.ListUsersAdminRow
	Total int64
}

// ListAdmin returns a page of users for the admin panel.
func (s *Service) ListAdmin(ctx context.Context, p pagination.Params, f AdminListFilters) (AdminListResult, error) {
	items, err := s.q.ListUsersAdmin(ctx, gen.ListUsersAdminParams{
		Limit: p.Limit(), Offset: p.Offset(),
		Q: f.Query, Banned: f.Banned, Hidden: f.Hidden, Role: f.Role,
	})
	if err != nil {
		return AdminListResult{}, fmt.Errorf("users: listing: %w", err)
	}
	total, err := s.q.CountUsersAdmin(ctx, gen.CountUsersAdminParams{
		Q: f.Query, Banned: f.Banned, Hidden: f.Hidden, Role: f.Role,
	})
	if err != nil {
		return AdminListResult{}, fmt.Errorf("users: counting: %w", err)
	}
	return AdminListResult{Items: items, Total: total}, nil
}

// AdminUpdate toggles banned/hidden or changes role. Banning revokes the user's
// sessions. Admins cannot ban or demote themselves.
func (s *Service) AdminUpdate(ctx context.Context, actorID, targetID uuid.UUID, banned, hidden *bool, role *string) (gen.User, error) {
	if actorID == targetID {
		if (banned != nil && *banned) || (role != nil && *role != "admin") {
			return gen.User{}, apperr.Conflictf("you cannot ban or demote yourself")
		}
	}
	if role != nil && *role != "user" && *role != "admin" {
		v := apperr.NewValidation()
		v.Add("role", "must be 'user' or 'admin'")
		return gen.User{}, v
	}
	u, err := s.q.UpdateUserAdminFields(ctx, gen.UpdateUserAdminFieldsParams{
		ID: targetID, Banned: banned, Hidden: hidden, Role: role,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.User{}, apperr.ErrNotFound
		}
		return gen.User{}, fmt.Errorf("users: admin update: %w", err)
	}
	if banned != nil && *banned {
		if err := s.sessions.DeleteAllForUser(ctx, targetID, ""); err != nil {
			return gen.User{}, fmt.Errorf("users: revoking sessions on ban: %w", err)
		}
	}
	return u, nil
}

// AdminResetPassword sets a new password and revokes the user's sessions.
func (s *Service) AdminResetPassword(ctx context.Context, targetID uuid.UUID, newPassword string) error {
	if _, err := s.Get(ctx, targetID); err != nil {
		return err
	}
	v := apperr.NewValidation()
	if len(newPassword) < 8 || len(newPassword) > 128 {
		v.Add("new_password", "must be between 8 and 128 characters")
	} else if auth.IsCommonPassword(newPassword) {
		v.Add("new_password", "is too common; pick something less guessable")
	}
	if err := v.OrNil(); err != nil {
		return err
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("users: hashing password: %w", err)
	}
	if err := s.q.UpdateUserPassword(ctx, gen.UpdateUserPasswordParams{ID: targetID, PasswordHash: hash}); err != nil {
		return fmt.Errorf("users: resetting password: %w", err)
	}
	if err := s.sessions.DeleteAllForUser(ctx, targetID, ""); err != nil {
		return fmt.Errorf("users: revoking sessions on reset: %w", err)
	}
	return nil
}
