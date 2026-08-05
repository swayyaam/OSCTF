// Package teams is the team domain service: creation, membership, captain
// transfer, and the public team pages. It owns its transaction boundaries and
// never imports HTTP.
package teams

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db"
	"github.com/osctf/platform/internal/db/gen"
)

// Service implements team operations.
type Service struct {
	pool    *pgxpool.Pool
	q       *gen.Queries
	maxSize int
}

// New builds the team service. maxSize is the configured team cap.
func New(pool *pgxpool.Pool, maxSize int) *Service {
	return &Service{pool: pool, q: gen.New(pool), maxSize: maxSize}
}

// Team is the domain view of a team plus its members and the caller's role.
type Team struct {
	Row     gen.Team
	Members []gen.ListTeamMembersRow
}

func validateName(name string) error {
	if len(name) < 3 || len(name) > 48 {
		v := apperr.NewValidation()
		v.Add("name", "must be between 3 and 48 characters")
		return v
	}
	return nil
}

// Create makes a team with the caller as captain and first member.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, name string) (Team, error) {
	if err := validateName(name); err != nil {
		return Team{}, err
	}
	code, err := GenerateInviteCode()
	if err != nil {
		return Team{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Team{}, fmt.Errorf("teams: generating id: %w", err)
	}

	var team gen.Team
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.q.WithTx(tx)
		t, cerr := qtx.CreateTeam(ctx, gen.CreateTeamParams{
			ID: id, Name: name, InviteCode: code, CaptainID: userID,
		})
		if cerr != nil {
			return cerr
		}
		if aerr := qtx.AddTeamMember(ctx, gen.AddTeamMemberParams{TeamID: id, UserID: userID}); aerr != nil {
			return aerr
		}
		team = t
		return nil
	})
	if err != nil {
		return Team{}, s.mapConflict(err, "team name")
	}
	return s.load(ctx, team)
}

// Join adds the caller to the team identified by an invite code.
func (s *Service) Join(ctx context.Context, userID uuid.UUID, code string) (Team, error) {
	t, err := s.q.GetTeamByInviteCode(ctx, code)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Team{}, &apperr.NotFound{Detail: "no team with that invite code"}
		}
		return Team{}, fmt.Errorf("teams: looking up invite code: %w", err)
	}
	if t.Banned {
		return Team{}, apperr.Conflictf("that team is banned")
	}
	// Capacity check and insert must be atomic: CountTeamMembers-then-AddTeamMember
	// is a check-then-act race, so simultaneous joins to the same team each read a
	// stale count and all insert, overrunning max team size. Lock the team row
	// (FOR UPDATE) as the serialization anchor — concurrent joins to THIS team block
	// until the prior one commits and then see the updated count. Mirrors the
	// GetChallengeForUpdate anchor in submissions.Submit.
	if err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.q.WithTx(tx)
		locked, lerr := qtx.LockTeam(ctx, t.ID)
		if lerr != nil {
			if errors.Is(lerr, pgx.ErrNoRows) {
				return &apperr.NotFound{Detail: "no team with that invite code"}
			}
			return fmt.Errorf("teams: locking team: %w", lerr)
		}
		if locked.Banned {
			return apperr.Conflictf("that team is banned")
		}
		count, lerr := qtx.CountTeamMembers(ctx, t.ID)
		if lerr != nil {
			return fmt.Errorf("teams: counting members: %w", lerr)
		}
		if int(count) >= s.maxSize {
			return apperr.Conflictf("that team is full")
		}
		if lerr := qtx.AddTeamMember(ctx, gen.AddTeamMemberParams{TeamID: t.ID, UserID: userID}); lerr != nil {
			// uq_team_members_user → already on a team.
			return s.mapConflict(lerr, "membership")
		}
		return nil
	}); err != nil {
		return Team{}, err
	}
	return s.load(ctx, t)
}

// Leave removes the caller from their team, transferring captaincy or deleting
// an empty, submission-free team (docs/v0.1/04-database.md).
func (s *Service) Leave(ctx context.Context, userID uuid.UUID) error {
	row, err := s.q.GetUserTeam(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &apperr.NotFound{Detail: "you are not on a team"}
		}
		return fmt.Errorf("teams: getting team: %w", err)
	}
	team := row.Team

	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.q.WithTx(tx)
		// Lock the team and re-read it INSIDE the tx: row.Team above is read outside
		// the transaction, so its captain_id is stale. Two members leaving at once
		// would otherwise each decide captaincy from that stale value — the second
		// leave, seeing the pre-transfer captain, removes the member the first leave
		// just promoted without re-transferring, stranding the team with a captain who
		// is no longer a member. The lock also serializes Leave against Join on the
		// same team.
		locked, err := qtx.LockTeam(ctx, team.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // team already gone (a concurrent leave emptied+deleted it)
			}
			return fmt.Errorf("teams: locking team: %w", err)
		}
		if err := qtx.RemoveTeamMember(ctx, gen.RemoveTeamMemberParams{TeamID: team.ID, UserID: userID}); err != nil {
			return err
		}
		members, err := qtx.ListTeamMembers(ctx, team.ID)
		if err != nil {
			return err
		}
		if len(members) == 0 {
			// Empty team: delete it only if it has no submissions (history).
			subs, err := qtx.CountTeamSubmissions(ctx, team.ID)
			if err != nil {
				return err
			}
			if subs == 0 {
				return qtx.DeleteTeam(ctx, team.ID)
			}
			return nil // keep as historical record
		}
		if locked.CaptainID == userID {
			// Transfer captaincy to the earliest joiner.
			return qtx.UpdateTeamCaptain(ctx, gen.UpdateTeamCaptainParams{ID: team.ID, CaptainID: members[0].ID})
		}
		return nil
	})
}

// CaptainRepair records a captaincy reassignment performed by RepairStrandedCaptains.
type CaptainRepair struct {
	TeamID     uuid.UUID
	NewCaptain uuid.UUID
}

// RepairStrandedCaptains reassigns captaincy to the earliest-joining member of every
// team whose captain_id is not a current member, and returns what it changed. This
// is a self-heal for the data corruption older builds could produce when two members
// left simultaneously (now prevented in Leave): a stranded team has a captain who
// cannot act, with no in-product recovery. Idempotent — a no-op once every captain is
// a member — so it is safe to run on every startup. Run it before serving traffic.
func (s *Service) RepairStrandedCaptains(ctx context.Context) ([]CaptainRepair, error) {
	rows, err := s.q.RepairStrandedCaptains(ctx)
	if err != nil {
		return nil, fmt.Errorf("teams: repairing stranded captains: %w", err)
	}
	out := make([]CaptainRepair, len(rows))
	for i, r := range rows {
		out[i] = CaptainRepair{TeamID: r.ID, NewCaptain: r.CaptainID}
	}
	return out, nil
}

// Rename changes a team's name; captain-only (enforced by the caller/handler).
func (s *Service) Rename(ctx context.Context, teamID uuid.UUID, name string) (Team, error) {
	if err := validateName(name); err != nil {
		return Team{}, err
	}
	t, err := s.q.UpdateTeamName(ctx, gen.UpdateTeamNameParams{ID: teamID, Name: name})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Team{}, apperr.ErrNotFound
		}
		return Team{}, s.mapConflict(err, "team name")
	}
	return s.load(ctx, t)
}

// RegenerateInviteCode issues a fresh invite code.
func (s *Service) RegenerateInviteCode(ctx context.Context, teamID uuid.UUID) (string, error) {
	code, err := GenerateInviteCode()
	if err != nil {
		return "", err
	}
	if _, err := s.q.UpdateTeamInviteCode(ctx, gen.UpdateTeamInviteCodeParams{ID: teamID, InviteCode: code}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", apperr.ErrNotFound
		}
		return "", fmt.Errorf("teams: regenerating invite code: %w", err)
	}
	return code, nil
}

// Get loads a team by ID with its members.
func (s *Service) Get(ctx context.Context, teamID uuid.UUID) (Team, error) {
	t, err := s.q.GetTeamByID(ctx, teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Team{}, apperr.ErrNotFound
		}
		return Team{}, fmt.Errorf("teams: getting team: %w", err)
	}
	return s.load(ctx, t)
}

// GetByCaptain fetches a team and verifies the caller is its captain.
func (s *Service) RequireCaptain(ctx context.Context, teamID, userID uuid.UUID) (gen.Team, error) {
	t, err := s.q.GetTeamByID(ctx, teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.Team{}, apperr.ErrNotFound
		}
		return gen.Team{}, fmt.Errorf("teams: getting team: %w", err)
	}
	if t.CaptainID != userID {
		return gen.Team{}, apperr.Forbiddenf("only the captain can do that")
	}
	return t, nil
}

// IsMember reports whether the user belongs to the team.
func (s *Service) IsMember(ctx context.Context, teamID, userID uuid.UUID) (bool, error) {
	row, err := s.q.GetUserTeam(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("teams: membership check: %w", err)
	}
	return row.Team.ID == teamID, nil
}

func (s *Service) load(ctx context.Context, t gen.Team) (Team, error) {
	members, err := s.q.ListTeamMembers(ctx, t.ID)
	if err != nil {
		return Team{}, fmt.Errorf("teams: listing members: %w", err)
	}
	return Team{Row: t, Members: members}, nil
}

func (s *Service) mapConflict(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "uq_team_members_user":
			return apperr.Conflictf("you are already on a team")
		case "uq_teams_name":
			return apperr.Conflictf("that team name is taken")
		case "uq_teams_invite_code":
			return apperr.Conflictf("invite code collision, please retry")
		}
		return apperr.Conflictf("%s already exists", what)
	}
	return fmt.Errorf("teams: %s: %w", what, err)
}
