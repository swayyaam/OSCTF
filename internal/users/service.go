// Package users is the user-account domain service: registration validation,
// profile reads, and password changes. Admin user management also lives here.
// It never imports HTTP; handlers translate its errors.
package users

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/db/gen"
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// Service implements user-account operations over the store.
type Service struct {
	q                *gen.Queries
	sessions         *auth.SessionStore
	registrationOpen bool
}

// New builds the service.
func New(q *gen.Queries, sessions *auth.SessionStore, registrationOpen bool) *Service {
	return &Service{q: q, sessions: sessions, registrationOpen: registrationOpen}
}

// RegisterInput is the validated registration payload.
type RegisterInput struct {
	Username string
	Email    string
	Password string
}

// Register validates and creates a user account.
func (s *Service) Register(ctx context.Context, in RegisterInput) (gen.User, error) {
	if !s.registrationOpen {
		return gen.User{}, apperr.Forbiddenf("registration is closed")
	}

	v := apperr.NewValidation()
	if len(in.Username) < 3 || len(in.Username) > 32 {
		v.Add("username", "must be between 3 and 32 characters")
	} else if !usernameRe.MatchString(in.Username) {
		v.Add("username", "may only contain letters, digits, and _ . -")
	}
	if _, err := mail.ParseAddress(in.Email); err != nil || strings.ContainsAny(in.Email, " <>") {
		v.Add("email", "must be a valid email address")
	}
	if len(in.Password) < 8 || len(in.Password) > 128 {
		v.Add("password", "must be between 8 and 128 characters")
	} else if auth.IsCommonPassword(in.Password) {
		v.Add("password", "is too common; pick something less guessable")
	}
	if err := v.OrNil(); err != nil {
		return gen.User{}, err
	}

	hash, err := auth.HashPassword(ctx, in.Password)
	if err != nil {
		if errors.Is(err, apperr.ErrUnavailable) {
			return gen.User{}, err // hashing gate shed this request: 503, not 500
		}
		return gen.User{}, fmt.Errorf("users: hashing password: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return gen.User{}, fmt.Errorf("users: generating id: %w", err)
	}
	u, err := s.q.CreateUser(ctx, gen.CreateUserParams{
		ID:           id,
		Username:     in.Username,
		Email:        strings.ToLower(in.Email),
		PasswordHash: hash,
		Role:         "user",
		Hidden:       false,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return gen.User{}, apperr.Conflictf("username or email is already taken")
		}
		return gen.User{}, fmt.Errorf("users: creating user: %w", err)
	}
	return u, nil
}

// Get returns a user by ID.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (gen.User, error) {
	u, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.User{}, apperr.ErrNotFound
		}
		return gen.User{}, fmt.Errorf("users: getting user: %w", err)
	}
	return u, nil
}

// TeamRef is a compact team reference with the caller's role in it.
type TeamRef struct {
	ID   uuid.UUID
	Name string
	Role string // captain | member
}

// TeamOf returns the user's team reference, or nil when teamless.
func (s *Service) TeamOf(ctx context.Context, userID uuid.UUID) (*TeamRef, error) {
	row, err := s.q.GetUserTeam(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("users: getting team: %w", err)
	}
	role := "member"
	if row.Team.CaptainID == userID {
		role = "captain"
	}
	return &TeamRef{ID: row.Team.ID, Name: row.Team.Name, Role: role}, nil
}

// ChangePassword verifies the current password and swaps in the new one,
// revoking every other session of the user.
func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, current, newPassword, keepSessionToken string) error {
	u, err := s.Get(ctx, userID)
	if err != nil {
		return err
	}
	ok, err := auth.VerifyPassword(ctx, current, u.PasswordHash)
	if errors.Is(err, apperr.ErrUnavailable) {
		return err // hashing gate shed this request: 503, not a misleading 403
	}
	if err != nil || !ok {
		return apperr.Forbiddenf("current password is incorrect")
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
	hash, err := auth.HashPassword(ctx, newPassword)
	if err != nil {
		if errors.Is(err, apperr.ErrUnavailable) {
			return err // hashing gate shed this request: 503, not 500
		}
		return fmt.Errorf("users: hashing password: %w", err)
	}
	if err := s.q.UpdateUserPassword(ctx, gen.UpdateUserPasswordParams{ID: userID, PasswordHash: hash}); err != nil {
		return fmt.Errorf("users: updating password: %w", err)
	}
	if err := s.sessions.DeleteAllForUser(ctx, userID, keepSessionToken); err != nil {
		return fmt.Errorf("users: revoking sessions: %w", err)
	}
	return nil
}

// RehashPassword persists a transparently upgraded hash (login-time rehash).
func (s *Service) RehashPassword(ctx context.Context, userID uuid.UUID, newHash string) error {
	return s.q.UpdateUserPassword(ctx, gen.UpdateUserPasswordParams{ID: userID, PasswordHash: newHash})
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
