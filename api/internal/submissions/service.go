// Package submissions owns the flag-submission hot path: the single-transaction
// flow that locks the challenge, enforces solve/attempt rules, compares the flag
// in constant time, and always logs the attempt (docs/v0.1/01-architecture.md).
package submissions

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/scoring"
)

// Service implements the submission flow.
type Service struct {
	pool   *pgxpool.Pool
	q      *gen.Queries
	events *events.Service
	clock  clock.Clock
}

// New builds the service.
func New(pool *pgxpool.Pool, ev *events.Service, c clock.Clock) *Service {
	return &Service{pool: pool, q: gen.New(pool), events: ev, clock: c}
}

// Input is a validated submission request.
type Input struct {
	UserID       uuid.UUID
	TeamID       uuid.UUID
	Slug         string
	Flag         string
	IP           *string
	TeamBanned   bool // caller's team banned → reject (handler-supplied)
	BypassWindow bool // admin submissions ignore the event window
}

// Result is the submission verdict.
type Result struct {
	Correct bool
	Points  *int // current challenge value when correct; nil otherwise
}

// Submit runs the full flow and returns the verdict. Every attempt is logged.
func (s *Service) Submit(ctx context.Context, in Input) (Result, error) {
	if in.TeamBanned {
		return Result{}, &apperr.Forbidden{Detail: "your team is banned"}
	}

	// Event must be running unless the caller is an admin bypassing the window.
	if !in.BypassWindow {
		e, err := s.events.Get(ctx)
		if err != nil {
			return Result{}, err
		}
		if s.events.Phase(e) != events.PhaseRunning {
			return Result{}, &apperr.Forbidden{Detail: "the event is not running"}
		}
	}

	// Resolve the challenge and enforce visibility (invisible → 404).
	ch, err := s.q.GetChallengeBySlug(ctx, in.Slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, apperr.ErrNotFound
		}
		return Result{}, fmt.Errorf("submissions: get challenge: %w", err)
	}
	if !ch.Visible && !in.BypassWindow {
		return Result{}, apperr.ErrNotFound
	}

	var correct bool
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.q.WithTx(tx)

		// Lock the challenge as the solve-count anchor.
		locked, lerr := qtx.GetChallengeForUpdate(ctx, ch.ID)
		if lerr != nil {
			return fmt.Errorf("submissions: lock challenge: %w", lerr)
		}

		solved, lerr := qtx.HasTeamSolved(ctx, gen.HasTeamSolvedParams{ChallengeID: ch.ID, TeamID: in.TeamID})
		if lerr != nil {
			return fmt.Errorf("submissions: solved check: %w", lerr)
		}
		if solved {
			return apperr.Conflictf("your team has already solved this challenge")
		}

		attempts, lerr := qtx.CountTeamAttempts(ctx, gen.CountTeamAttemptsParams{ChallengeID: ch.ID, TeamID: in.TeamID})
		if lerr != nil {
			return fmt.Errorf("submissions: count attempts: %w", lerr)
		}
		if locked.MaxAttempts != nil && attempts >= int64(*locked.MaxAttempts) {
			return &apperr.Forbidden{Detail: "no attempts remaining for this challenge"}
		}

		correct = compareFlag(in.Flag, locked.Flag, locked.FlagCaseInsensitive)

		id, lerr := uuid.NewV7()
		if lerr != nil {
			return fmt.Errorf("submissions: generating id: %w", lerr)
		}
		_, lerr = qtx.CreateSubmission(ctx, gen.CreateSubmissionParams{
			ID: id, ChallengeID: ch.ID, TeamID: in.TeamID, UserID: in.UserID,
			Provided: in.Flag, Correct: correct, Ip: parseIP(in.IP),
		})
		if lerr != nil {
			// The partial unique index makes a concurrent double-solve a 23505.
			var pgErr *pgconn.PgError
			if errors.As(lerr, &pgErr) && pgErr.Code == "23505" {
				return apperr.Conflictf("your team has already solved this challenge")
			}
			return fmt.Errorf("submissions: insert: %w", lerr)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	res := Result{Correct: correct}
	if correct {
		// Value reflects the solve count including this solve (already committed).
		count, cerr := s.q.CountChallengeSolves(ctx, ch.ID)
		if cerr != nil {
			return Result{}, fmt.Errorf("submissions: count solves: %w", cerr)
		}
		pts := currentPoints(ch, int(count))
		res.Points = &pts
	}
	return res, nil
}

// parseIP converts a client IP string into the netip.Addr pgx stores for inet.
// An unparseable or empty IP is stored NULL.
func parseIP(ip *string) *netip.Addr {
	if ip == nil || *ip == "" {
		return nil
	}
	addr, err := netip.ParseAddr(*ip)
	if err != nil {
		return nil
	}
	return &addr
}

func compareFlag(provided, actual string, caseInsensitive bool) bool {
	a, b := provided, actual
	if caseInsensitive {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func currentPoints(c gen.Challenge, solves int) int {
	params := scoring.ChallengeScoring{Initial: int(c.PointsInitial)}
	if c.PointsMin != nil {
		params.Min = int(*c.PointsMin)
	}
	if c.Decay != nil {
		params.Decay = int(*c.Decay)
	}
	return scoring.Value(c.Scoring, params, solves)
}
