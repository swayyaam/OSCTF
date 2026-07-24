package challenges

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
)

// BoardEntry is a participant-facing challenge with its current value and the
// caller team's solve state. It never carries the flag or description.
type BoardEntry struct {
	Challenge  gen.Challenge
	Points     int
	Solves     int
	SolvedByMe bool
}

// ListVisible returns visible challenges with current point values and solve
// counts. teamID may be uuid.Nil for a teamless caller (SolvedByMe always false).
func (s *Service) ListVisible(ctx context.Context, teamID uuid.UUID) ([]BoardEntry, error) {
	rows, err := s.q.ListVisibleChallenges(ctx)
	if err != nil {
		return nil, fmt.Errorf("challenges: listing visible: %w", err)
	}
	out := make([]BoardEntry, 0, len(rows))
	for _, c := range rows {
		solves, err := s.q.CountChallengeSolves(ctx, c.ID)
		if err != nil {
			return nil, fmt.Errorf("challenges: counting solves: %w", err)
		}
		entry := BoardEntry{
			Challenge: c,
			Points:    CurrentPoints(c, int(solves)),
			Solves:    int(solves),
		}
		if teamID != uuid.Nil {
			solved, err := s.q.HasTeamSolved(ctx, gen.HasTeamSolvedParams{ChallengeID: c.ID, TeamID: teamID})
			if err != nil {
				return nil, fmt.Errorf("challenges: solved check: %w", err)
			}
			entry.SolvedByMe = solved
		}
		out = append(out, entry)
	}
	return out, nil
}

// Detail is the full participant view of one challenge.
type Detail struct {
	BoardEntry
	Attachments  []gen.ChallengeAttachment
	AttemptsUsed int
}

// GetVisibleDetail returns a visible challenge's full participant detail by slug.
// Invisible or missing challenges are ErrNotFound (never leak existence).
func (s *Service) GetVisibleDetail(ctx context.Context, slug string, teamID uuid.UUID) (Detail, error) {
	c, err := s.q.GetChallengeBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Detail{}, apperr.ErrNotFound
		}
		return Detail{}, fmt.Errorf("challenges: get by slug: %w", err)
	}
	if !c.Visible {
		return Detail{}, apperr.ErrNotFound
	}
	solves, err := s.q.CountChallengeSolves(ctx, c.ID)
	if err != nil {
		return Detail{}, fmt.Errorf("challenges: counting solves: %w", err)
	}
	atts, err := s.q.ListAttachments(ctx, c.ID)
	if err != nil {
		return Detail{}, fmt.Errorf("challenges: listing attachments: %w", err)
	}
	d := Detail{
		BoardEntry:  BoardEntry{Challenge: c, Points: CurrentPoints(c, int(solves)), Solves: int(solves)},
		Attachments: atts,
	}
	if teamID != uuid.Nil {
		solved, err := s.q.HasTeamSolved(ctx, gen.HasTeamSolvedParams{ChallengeID: c.ID, TeamID: teamID})
		if err != nil {
			return Detail{}, fmt.Errorf("challenges: solved check: %w", err)
		}
		d.SolvedByMe = solved
		attempts, err := s.q.CountTeamAttempts(ctx, gen.CountTeamAttemptsParams{ChallengeID: c.ID, TeamID: teamID})
		if err != nil {
			return Detail{}, fmt.Errorf("challenges: counting attempts: %w", err)
		}
		d.AttemptsUsed = int(attempts)
	}
	return d, nil
}

// GetBySlug returns a raw challenge by slug (used by the submit path in M5).
func (s *Service) GetBySlug(ctx context.Context, slug string) (gen.Challenge, error) {
	c, err := s.q.GetChallengeBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.Challenge{}, apperr.ErrNotFound
		}
		return gen.Challenge{}, fmt.Errorf("challenges: get by slug: %w", err)
	}
	return c, nil
}

// CurrentValue returns a challenge's current point value by loading it and its
// solve count. Used by profile solve lists.
func (s *Service) CurrentValue(ctx context.Context, challengeID uuid.UUID) (int, error) {
	c, err := s.q.GetChallengeByID(ctx, challengeID)
	if err != nil {
		return 0, fmt.Errorf("challenges: current value: %w", err)
	}
	solves, err := s.q.CountChallengeSolves(ctx, challengeID)
	if err != nil {
		return 0, fmt.Errorf("challenges: current value solve count: %w", err)
	}
	return CurrentPoints(c, int(solves)), nil
}
