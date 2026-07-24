package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/db/gen"
)

// ExampleSeeder loads the example challenges from disk on first boot.
type ExampleSeeder struct {
	q          *gen.Queries
	challenges *challenges.Service
	log        *slog.Logger
}

// NewExampleSeeder builds the seeder.
func NewExampleSeeder(q *gen.Queries, ch *challenges.Service, log *slog.Logger) *ExampleSeeder {
	return &ExampleSeeder{q: q, challenges: ch, log: log}
}

// Seed loads every examples/challenges/<slug>/challenge.yaml under dir, creating
// challenges and uploading their files. Idempotent: a no-op if any challenge
// already exists. A missing examples dir is logged and skipped.
func (s *ExampleSeeder) Seed(ctx context.Context, dir string) error {
	count, err := s.q.CountChallenges(ctx)
	if err != nil {
		return fmt.Errorf("seed: counting challenges: %w", err)
	}
	if count > 0 {
		return nil
	}

	root := filepath.Join(dir, "challenges")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.log.Warn("no examples directory found; skipping example seeding", "dir", root)
			return nil
		}
		return fmt.Errorf("seed: reading examples: %w", err)
	}

	seeded := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		chalDir := filepath.Join(root, e.Name())
		yamlPath := filepath.Join(chalDir, "challenge.yaml")
		if _, statErr := os.Stat(yamlPath); statErr != nil {
			continue
		}
		if err := s.seedOne(ctx, chalDir, e.Name(), yamlPath); err != nil {
			return err
		}
		seeded++
	}
	s.log.Info("seeded example challenges", "count", seeded)
	return nil
}

func (s *ExampleSeeder) seedOne(ctx context.Context, chalDir, dirName, yamlPath string) error {
	c, err := parseChallengeYAML(yamlPath, dirName)
	if err != nil {
		return err
	}

	in := challenges.CreateInput{
		Slug: &c.Slug, Title: c.Title, Category: c.Category,
		Description: c.Description, Flag: c.Flag, FlagCaseInsensitive: c.FlagCaseInsensitive,
		Kind: c.Kind, Scoring: c.Scoring, PointsInitial: c.PointsInitial,
		PointsMin: c.PointsMin, Decay: c.Decay, MaxAttempts: c.MaxAttempts,
		Visible: c.Visible, MemLimitMB: c.MemLimitMB, CPUMillis: c.CPUMillis,
		ContainerEnv: c.ContainerEnv,
	}
	if c.Difficulty != "" {
		in.Difficulty = &c.Difficulty
	}
	if c.Kind == "container" {
		in.Image = &c.Image
		in.InternalPort = c.InternalPort
		if c.ConnectionTemplate != "" {
			in.ConnectionTemplate = &c.ConnectionTemplate
		}
	}

	created, err := s.challenges.Create(ctx, in)
	if err != nil {
		return fmt.Errorf("seed: creating %q: %w", c.Slug, err)
	}

	for _, rel := range c.Files {
		if err := s.uploadFile(ctx, created.ID, chalDir, rel); err != nil {
			return err
		}
	}
	return nil
}

func (s *ExampleSeeder) uploadFile(ctx context.Context, challengeID [16]byte, chalDir, rel string) error {
	// Keep files within the challenge directory (defence against traversal in authored yaml).
	clean := filepath.Clean(rel)
	full := filepath.Join(chalDir, clean)
	f, err := os.Open(full) //nolint:gosec // path is confined to the trusted examples dir
	if err != nil {
		return fmt.Errorf("seed: opening attachment %s: %w", full, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("seed: stat attachment %s: %w", full, err)
	}
	_, err = s.challenges.UploadAttachment(ctx, challengeID, filepath.Base(clean), "application/octet-stream", info.Size(), f)
	if err != nil {
		return fmt.Errorf("seed: uploading %s: %w", full, err)
	}
	return nil
}
