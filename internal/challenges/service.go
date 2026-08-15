// Package challenges is the challenge domain service: admin CRUD (with the
// admin/participant split enforced at the handler layer), attachment storage,
// and the participant-facing board with visibility and phase gating. Point values
// come from the pure scoring package.
package challenges

import (
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/scoring"
	"github.com/osctf/platform/internal/storage"
)

var (
	slugRe     = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	nonSlugRe  = regexp.MustCompile(`[^a-z0-9]+`)
	validCats  = map[string]bool{"web": true, "pwn": true, "crypto": true, "rev": true, "forensics": true, "misc": true}
	validDiffs = map[string]bool{"intro": true, "easy": true, "medium": true, "hard": true, "insane": true}
)

// Service implements challenge operations.
type Service struct {
	q     *gen.Queries
	store storage.ObjectStore
}

// New builds the service.
func New(q *gen.Queries, store storage.ObjectStore) *Service {
	return &Service{q: q, store: store}
}

// CurrentPoints returns the challenge's current value given its solve count.
func CurrentPoints(c gen.Challenge, solves int) int {
	params := scoring.ChallengeScoring{Initial: int(c.PointsInitial)}
	if c.PointsMin != nil {
		params.Min = int(*c.PointsMin)
	}
	if c.Decay != nil {
		params.Decay = int(*c.Decay)
	}
	return scoring.Value(c.Scoring, params, solves)
}

// slugify derives a URL-safe slug from a title.
func slugify(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	s = nonSlugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func mapConflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return apperr.Conflictf("a challenge with that slug already exists")
	}
	return err
}
