package challenges

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/pagination"
)

// CreateInput is the validated challenge creation payload.
type CreateInput struct {
	Slug                *string
	Title               string
	Category            string
	Description         string
	Difficulty          *string
	Kind                string
	Flag                string
	FlagCaseInsensitive bool
	Scoring             string
	PointsInitial       int
	PointsMin           *int
	Decay               *int
	MaxAttempts         *int
	Visible             bool
	Image               *string
	InternalPort        *int
	MemLimitMB          *int
	CPUMillis           *int
	ContainerEnv        map[string]string
	ConnectionTemplate  *string
	Instancing          string // "" -> "shared"
	FlagMode            string // "" -> "static"
	InstanceTTLSeconds  *int   // nil -> default; 0 -> no TTL
	Egress              *bool  // nil -> true
	WritablePaths       []string
}

// Full is a challenge plus its attachments (instance summary is joined in the handler/M7).
type Full struct {
	Challenge   gen.Challenge
	Attachments []gen.ChallengeAttachment
	Solves      int
}

// Create validates and inserts a challenge.
func (s *Service) Create(ctx context.Context, in CreateInput) (gen.Challenge, error) {
	if in.Kind == "" {
		in.Kind = "standard"
	}
	if in.Scoring == "" {
		in.Scoring = "dynamic"
	}
	if in.Instancing == "" {
		in.Instancing = "shared"
	}
	if in.FlagMode == "" {
		in.FlagMode = "static"
	}
	slug := ""
	if in.Slug != nil {
		slug = *in.Slug
	} else {
		slug = slugify(in.Title)
	}

	if err := validateCreate(in, slug); err != nil {
		return gen.Challenge{}, err
	}

	envJSON := []byte("{}")
	if len(in.ContainerEnv) > 0 {
		b, err := json.Marshal(in.ContainerEnv)
		if err != nil {
			return gen.Challenge{}, fmt.Errorf("challenges: marshaling env: %w", err)
		}
		envJSON = b
	}
	id, err := uuid.NewV7()
	if err != nil {
		return gen.Challenge{}, fmt.Errorf("challenges: generating id: %w", err)
	}

	egress := true
	if in.Egress != nil {
		egress = *in.Egress
	}
	wpJSON := []byte("[]")
	if len(in.WritablePaths) > 0 {
		b, merr := json.Marshal(in.WritablePaths)
		if merr != nil {
			return gen.Challenge{}, fmt.Errorf("challenges: marshaling writable_paths: %w", merr)
		}
		wpJSON = b
	}

	params := gen.CreateChallengeParams{
		ID: id, Slug: slug, Title: in.Title, Category: in.Category,
		Description: in.Description, Difficulty: in.Difficulty, Kind: in.Kind,
		Flag: in.Flag, FlagCaseInsensitive: in.FlagCaseInsensitive, Scoring: in.Scoring,
		PointsInitial: clampI32(in.PointsInitial), Visible: in.Visible,
		MemLimitMb: 256, CpuMillis: 500, ContainerEnv: envJSON,
		PointsMin: i32p(in.PointsMin), Decay: i32p(in.Decay), MaxAttempts: i32p(in.MaxAttempts),
		Image: in.Image, InternalPort: i32p(in.InternalPort), ConnectionTemplate: in.ConnectionTemplate,
		Instancing: &in.Instancing, FlagMode: &in.FlagMode,
		InstanceTtlSeconds: i32p(in.InstanceTTLSeconds), Egress: &egress, WritablePaths: wpJSON,
	}
	if in.MemLimitMB != nil {
		params.MemLimitMb = clampI32(*in.MemLimitMB)
	}
	if in.CPUMillis != nil {
		params.CpuMillis = clampI32(*in.CPUMillis)
	}

	c, err := s.q.CreateChallenge(ctx, params)
	if err != nil {
		return gen.Challenge{}, mapConflict(err)
	}
	return c, nil
}

// GetAdmin returns the full challenge with attachments and solve count.
func (s *Service) GetAdmin(ctx context.Context, id uuid.UUID) (Full, error) {
	c, err := s.q.GetChallengeByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Full{}, apperr.ErrNotFound
		}
		return Full{}, fmt.Errorf("challenges: get: %w", err)
	}
	return s.loadFull(ctx, c)
}

func (s *Service) loadFull(ctx context.Context, c gen.Challenge) (Full, error) {
	atts, err := s.q.ListAttachments(ctx, c.ID)
	if err != nil {
		return Full{}, fmt.Errorf("challenges: listing attachments: %w", err)
	}
	solves, err := s.q.CountChallengeSolves(ctx, c.ID)
	if err != nil {
		return Full{}, fmt.Errorf("challenges: counting solves: %w", err)
	}
	return Full{Challenge: c, Attachments: atts, Solves: int(solves)}, nil
}

// AdminListFilters narrows the admin challenge list.
type AdminListFilters struct {
	Category *string
	Visible  *bool
	Kind     *string
}

// AdminListResult is a page of challenges with attachments and solve counts.
type AdminListResult struct {
	Items []Full
	Total int64
}

// ListAdmin returns a page of challenges (including invisible ones).
func (s *Service) ListAdmin(ctx context.Context, p pagination.Params, f AdminListFilters) (AdminListResult, error) {
	rows, err := s.q.ListChallengesAdmin(ctx, gen.ListChallengesAdminParams{
		Limit: p.Limit(), Offset: p.Offset(),
		Category: f.Category, Visible: f.Visible, Kind: f.Kind,
	})
	if err != nil {
		return AdminListResult{}, fmt.Errorf("challenges: listing: %w", err)
	}
	total, err := s.q.CountChallengesAdmin(ctx, gen.CountChallengesAdminParams{
		Category: f.Category, Visible: f.Visible, Kind: f.Kind,
	})
	if err != nil {
		return AdminListResult{}, fmt.Errorf("challenges: counting: %w", err)
	}
	items := make([]Full, 0, len(rows))
	for _, c := range rows {
		full, err := s.loadFull(ctx, c)
		if err != nil {
			return AdminListResult{}, err
		}
		items = append(items, full)
	}
	return AdminListResult{Items: items, Total: total}, nil
}

// UpdateInput is a partial challenge update; nil fields are unchanged and Set*
// flags distinguish "clear to null" from "leave unchanged".
type UpdateInput struct {
	Slug                *string
	Title               *string
	Category            *string
	Description         *string
	SetDifficulty       bool
	Difficulty          *string
	Flag                *string
	FlagCaseInsensitive *bool
	Scoring             *string
	PointsInitial       *int
	SetPointsMin        bool
	PointsMin           *int
	SetDecay            bool
	Decay               *int
	SetMaxAttempts      bool
	MaxAttempts         *int
	Visible             *bool
	SetImage            bool
	Image               *string
	SetInternalPort     bool
	InternalPort        *int
	MemLimitMB          *int
	CPUMillis           *int
	ContainerEnv        map[string]string
	SetConnectionTmpl   bool
	ConnectionTemplate  *string
	Instancing          *string
	FlagMode            *string
	SetInstanceTTL      bool
	InstanceTTLSeconds  *int
	Egress              *bool
	WritablePaths       []string
}

// Update applies a partial update after validating the resulting row.
func (s *Service) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (gen.Challenge, error) {
	cur, err := s.q.GetChallengeByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.Challenge{}, apperr.ErrNotFound
		}
		return gen.Challenge{}, fmt.Errorf("challenges: get for update: %w", err)
	}
	if err := validateUpdate(cur, in); err != nil {
		return gen.Challenge{}, err
	}

	params := gen.UpdateChallengeParams{
		ID:   id,
		Slug: in.Slug, Title: in.Title, Category: in.Category, Description: in.Description,
		SetDifficulty: in.SetDifficulty, Difficulty: in.Difficulty,
		Flag: in.Flag, FlagCaseInsensitive: in.FlagCaseInsensitive, Scoring: in.Scoring,
		PointsInitial: i32p(in.PointsInitial),
		SetPointsMin:  in.SetPointsMin, PointsMin: i32p(in.PointsMin),
		SetDecay: in.SetDecay, Decay: i32p(in.Decay),
		SetMaxAttempts: in.SetMaxAttempts, MaxAttempts: i32p(in.MaxAttempts),
		Visible:  in.Visible,
		SetImage: in.SetImage, Image: in.Image,
		SetInternalPort: in.SetInternalPort, InternalPort: i32p(in.InternalPort),
		MemLimitMb: i32p(in.MemLimitMB), CpuMillis: i32p(in.CPUMillis),
		SetConnectionTemplate: in.SetConnectionTmpl, ConnectionTemplate: in.ConnectionTemplate,
		Instancing: in.Instancing, FlagMode: in.FlagMode,
		SetInstanceTtlSeconds: in.SetInstanceTTL, InstanceTtlSeconds: i32p(in.InstanceTTLSeconds),
		Egress: in.Egress,
	}
	if in.ContainerEnv != nil {
		b, merr := json.Marshal(in.ContainerEnv)
		if merr != nil {
			return gen.Challenge{}, fmt.Errorf("challenges: marshaling env: %w", merr)
		}
		params.ContainerEnv = b
	}
	if in.WritablePaths != nil {
		b, merr := json.Marshal(in.WritablePaths)
		if merr != nil {
			return gen.Challenge{}, fmt.Errorf("challenges: marshaling writable_paths: %w", merr)
		}
		params.WritablePaths = b
	}

	c, err := s.q.UpdateChallenge(ctx, params)
	if err != nil {
		return gen.Challenge{}, mapConflict(err)
	}
	return c, nil
}

// Delete removes a challenge. Deleting a visible challenge during a running event
// requires confirm=true (it rewrites history). The caller passes eventRunning.
func (s *Service) Delete(ctx context.Context, id uuid.UUID, eventRunning, confirm bool) error {
	c, err := s.q.GetChallengeByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrNotFound
		}
		return fmt.Errorf("challenges: get for delete: %w", err)
	}
	if c.Visible && eventRunning && !confirm {
		return apperr.Conflictf("refusing to delete a visible challenge during a running event; pass confirm=true")
	}
	// The instance (if any) is destroyed by the handler via the runtime before this
	// call (M7); the DB cascade removes the instances row, submissions, attachments.
	if err := s.q.DeleteChallenge(ctx, id); err != nil {
		return fmt.Errorf("challenges: delete: %w", err)
	}
	return nil
}

func i32p(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := clampI32(*p)
	return &v
}

// clampI32 saturates an int into int32 range. Validated challenge fields are well
// within range; this only guards against pathological inputs before the DB write.
func clampI32(v int) int32 {
	const maxI32, minI32 = int(^uint32(0) >> 1), -int(^uint32(0)>>1) - 1
	if v > maxI32 {
		return 1<<31 - 1
	}
	if v < minI32 {
		return -1 << 31
	}
	return int32(v) //nolint:gosec // G115: bounds checked directly above.
}
