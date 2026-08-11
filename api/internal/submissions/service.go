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
	"time"

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

// redactedFlag replaces a real per-instance flag in the stored `provided` value so a
// per-team instance secret is never persisted or echoed back through the admin views.
const redactedFlag = "[redacted per-instance flag]"

// FlagChecker is a plugin-provided correctness check. It receives the submitted guess + author
// config + per-instance metadata, and NEVER the flag. Defined here (not imported from challenges)
// so the submission path does not depend on the challenge domain — the composition root adapts
// the challenge-type registry to this.
type FlagChecker interface {
	CheckFlag(ctx context.Context, submitted string, config, instance map[string]string) (correct bool, err error)
}

// ChallengeTypes resolves a challenge type id to its checker, without the submission path
// importing the challenge-type registry. Resolve returns:
//   - (checker, true, true)  — a plugin type with a live checker → pre-tx verdict path;
//   - (nil, false, true)     — a built-in type → in-tx flag comparison;
//   - (nil, false, false)    — no checker registered (plugin down / unknown type) → reject-retry.
type ChallengeTypes interface {
	Resolve(typeID string) (checker FlagChecker, isPlugin, registered bool)
}

// Service implements the submission flow.
type Service struct {
	pool    *pgxpool.Pool
	q       *gen.Queries
	events  *events.Service
	clock   clock.Clock
	audit   *audit.Logger
	checker ChallengeTypes // challenge-type resolver; nil = no plugin types (byte-identical to v0.2)
}

// New builds the service. With no challenge-type resolver wired (see WithChallengeTypes) every
// challenge uses the platform's built-in flag comparison — byte-identical to v0.2.
func New(pool *pgxpool.Pool, ev *events.Service, c clock.Clock, auditLog *audit.Logger) *Service {
	return &Service{pool: pool, q: gen.New(pool), events: ev, clock: c, audit: auditLog}
}

// WithChallengeTypes wires the challenge-type resolver the submission path consults: a challenge
// whose type resolves to a plugin checker takes the pre-transaction verdict path; a built-in type
// (or no resolver) keeps the in-transaction flag comparison. Returns the service for chaining.
func (s *Service) WithChallengeTypes(reg ChallengeTypes) *Service {
	s.checker = reg
	return s
}

// retryUnavailable is the DISTINCT reject-retry outcome: the challenge's checker could not render
// an authoritative verdict (plugin unavailable/unregistered, or the challenge was swapped/deleted
// between the verdict and the commit). It is NOT a wrong answer — no attempt is consumed — and it
// maps to 503 so the participant is told to retry, not shown "incorrect".
func retryUnavailable() error {
	return &apperr.Unavailable{
		Detail:     "this challenge's checker is temporarily unavailable — your attempt was not counted; please try again",
		RetryAfter: 2 * time.Second,
	}
}

// pluginVerdict decides whether ch's type is a plugin checker and, if so, computes the verdict
// PRE-transaction (outside the row lock — never a plugin network call inside an open tx). Returns
// (nil, nil) for a built-in type — the in-tx flag comparison handles it; (verdict, nil) for a
// plugin type; or (nil, reject-retry) when the checker is unavailable/unregistered, or the submit
// is a guaranteed reject (already solved), so no plugin call is paid for it.
func (s *Service) pluginVerdict(ctx context.Context, ch gen.Challenge, in Input) (*bool, error) {
	if s.checker == nil {
		return nil, nil // no plugin support wired → built-in path (v0.2)
	}
	fc, isPlugin, registered := s.checker.Resolve(ch.Type)
	if !registered {
		// A plugin type whose plugin is down/unregistered, or an unknown type: cannot judge.
		return nil, retryUnavailable()
	}
	if !isPlugin {
		return nil, nil // built-in type → in-tx flag comparison
	}
	// Early, NON-authoritative already-solved read: skip the plugin call for a duplicate submit
	// that cannot succeed (a slow/shed-prone plugin should not take load from guaranteed rejects).
	// The in-tx check re-decides authoritatively. (Phase was already enforced above.)
	if solved, serr := s.q.HasTeamSolved(ctx, gen.HasTeamSolvedParams{ChallengeID: ch.ID, TeamID: in.TeamID}); serr == nil && solved {
		return nil, apperr.Conflictf("your team has already solved this challenge")
	}
	// Per-challenge type config is a later feature; the plugin judges from the guess + its own
	// (env/manifest) config. Never send the flag.
	verdict, verr := fc.CheckFlag(ctx, in.Flag, map[string]string{}, map[string]string{})
	if verr != nil {
		return nil, retryUnavailable()
	}
	return &verdict, nil
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

	// For a plugin challenge-type, the verdict is computed here — PRE-transaction, outside the row
	// lock — so a slow/untrusted plugin never holds a database lock. nil for a built-in type (the
	// in-tx comparison decides); a reject-retry error short-circuits without opening the tx.
	pluginVerdict, rerr := s.pluginVerdict(ctx, ch, in)
	if rerr != nil {
		return Result{}, rerr
	}

	var correct bool
	var sharingOwner *uuid.UUID // owning team when another team's per-instance flag was submitted
	err = db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		qtx := s.q.WithTx(tx)
		sharingOwner = nil

		// Lock the challenge as the solve-count anchor.
		locked, lerr := qtx.GetChallengeForUpdate(ctx, ch.ID)
		if lerr != nil {
			if pluginVerdict != nil && errors.Is(lerr, pgx.ErrNoRows) {
				// DELETED between the pre-tx verdict and the lock — a missing row, not a changed
				// updated_at. Fail closed here, before any attempt/solve check.
				return retryUnavailable()
			}
			return fmt.Errorf("submissions: lock challenge: %w", lerr)
		}

		// Plugin path: the verdict was computed against a specific version of THIS row. If the
		// challenge was edited or its type swapped between the verdict and the lock, the verdict is
		// void. Checked BEFORE the solved/attempt checks so a swapped-or-deleted challenge fails
		// closed WITHOUT consuming an attempt — the ordering here is load-bearing (see the ordering
		// test, which fails if this moves below the solve/attempt checks).
		if pluginVerdict != nil && (locked.Type != ch.Type || !locked.UpdatedAt.Equal(ch.UpdatedAt)) {
			return retryUnavailable()
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

		// Correctness. A plugin challenge-type's verdict was decided pre-tx (above); a built-in
		// type compares here — static against the challenge flag (v0.1); per_instance against the
		// submitting team's own instance flag, raising a sharing signal on another team's flag.
		switch {
		case pluginVerdict != nil:
			correct = *pluginVerdict
		case locked.FlagMode == "per_instance":
			correct, sharingOwner, lerr = s.comparePerInstance(ctx, qtx, ch.ID, in)
			if lerr != nil {
				return lerr
			}
		default:
			correct = compareFlag(in.Flag, locked.Flag, locked.FlagCaseInsensitive)
		}

		id, lerr := uuid.NewV7()
		if lerr != nil {
			return fmt.Errorf("submissions: generating id: %w", lerr)
		}
		// A per-instance flag is a per-team secret. When the submitted value IS a real
		// per-instance flag — the team's own (correct) or, via sharing, another team's —
		// do not persist it verbatim: the admin submissions view would otherwise echo one
		// team's instance flag to whoever reads it, the very value the sharing signal above
		// deliberately refuses to record. Genuinely wrong guesses are kept for triage.
		provided := in.Flag
		if locked.FlagMode == "per_instance" && (correct || sharingOwner != nil) {
			provided = redactedFlag
		}
		_, lerr = qtx.CreateSubmission(ctx, gen.CreateSubmissionParams{
			ID: id, ChallengeID: ch.ID, TeamID: in.TeamID, UserID: in.UserID,
			Provided: provided, Correct: correct, Ip: parseIP(in.IP),
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
