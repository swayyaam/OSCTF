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
	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/metrics"
	"github.com/osctf/platform/internal/scoring"
)

// Service implements the submission flow.
type Service struct {
	pool   *pgxpool.Pool
	q      *gen.Queries
	events *events.Service
	clock  clock.Clock
	audit  *audit.Logger
}

// New builds the service.
func New(pool *pgxpool.Pool, ev *events.Service, c clock.Clock, auditLog *audit.Logger) *Service {
	return &Service{pool: pool, q: gen.New(pool), events: ev, clock: c, audit: auditLog}
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
	var sharingOwner *uuid.UUID // owning team when another team's per-instance flag was submitted
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.q.WithTx(tx)
		sharingOwner = nil

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

		// Flag comparison: static compares against the challenge flag (v0.1);
		// per_instance compares against the submitting team's own instance flag
		// and raises a sharing signal if it matches a different team's flag.
		if locked.FlagMode == "per_instance" {
			correct, sharingOwner, lerr = s.comparePerInstance(ctx, qtx, ch.ID, in)
			if lerr != nil {
				return lerr
			}
		} else {
			correct = compareFlag(in.Flag, locked.Flag, locked.FlagCaseInsensitive)
		}

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

	// A sharing signal is detection-only: log it and count it, never reveal it to
	// the submitter and never record the flag value.
	if sharingOwner != nil {
		metrics.FlagSharingSignals.Inc()
		if s.audit != nil {
			s.audit.Log(ctx, in.UserID, "flag.shared", "team", in.TeamID.String(), map[string]any{
				"challenge_id": ch.ID.String(),
				"owner_team":   sharingOwner.String(),
			})
		}
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

// comparePerInstance compares against the submitting team's own instance flag.
// It returns whether the flag is correct and, when incorrect, the owning team of
// any *other* team's instance whose flag was submitted (the sharing signal). A
// missing instance is a 403 (nothing legitimate to compare against) rather than a
// silent wrong answer.
func (s *Service) comparePerInstance(ctx context.Context, qtx *gen.Queries, challengeID uuid.UUID, in Input) (bool, *uuid.UUID, error) {
	inst, err := qtx.GetTeamInstance(ctx, gen.GetTeamInstanceParams{ChallengeID: challengeID, TeamID: &in.TeamID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil, &apperr.Forbidden{Detail: "start the challenge first"}
		}
		return false, nil, fmt.Errorf("submissions: get team instance: %w", err)
	}
	instFlag := ""
	if inst.Flag != nil {
		instFlag = *inst.Flag
	}
	if compareFlag(in.Flag, instFlag, false) {
		return true, nil, nil
	}
	// Wrong flag: did it match a different team's instance flag?
	owner, ferr := qtx.FindInstanceByFlag(ctx, gen.FindInstanceByFlagParams{ChallengeID: challengeID, Flag: &in.Flag})
	if ferr != nil {
		if errors.Is(ferr, pgx.ErrNoRows) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("submissions: sharing lookup: %w", ferr)
	}
	if owner.TeamID != nil && *owner.TeamID != in.TeamID {
		t := *owner.TeamID
		return false, &t, nil
	}
	return false, nil, nil
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
