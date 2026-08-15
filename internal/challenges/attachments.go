package challenges

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
)

// AttachmentContent streams an attachment's bytes with its metadata.
type AttachmentContent struct {
	Filename    string
	ContentType string
	Size        int64
	Body        io.ReadCloser
}

// UploadAttachment stores the file in object storage and records it.
func (s *Service) UploadAttachment(ctx context.Context, challengeID uuid.UUID, filename, contentType string, size int64, r io.Reader) (gen.ChallengeAttachment, error) {
	if _, err := s.q.GetChallengeByID(ctx, challengeID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.ChallengeAttachment{}, apperr.ErrNotFound
		}
		return gen.ChallengeAttachment{}, fmt.Errorf("challenges: get for upload: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return gen.ChallengeAttachment{}, fmt.Errorf("challenges: generating id: %w", err)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	key := fmt.Sprintf("challenges/%s/%s/%s", challengeID, id, filename)

	if err := s.store.Put(ctx, key, r, size, contentType); err != nil {
		return gen.ChallengeAttachment{}, err
	}
	att, err := s.q.CreateAttachment(ctx, gen.CreateAttachmentParams{
		ID: id, ChallengeID: challengeID, Filename: filename,
		SizeBytes: size, ContentType: contentType, ObjectKey: key,
	})
	if err != nil {
		// Roll back the stored blob on a duplicate-filename conflict.
		_ = s.store.Delete(ctx, key)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return gen.ChallengeAttachment{}, apperr.Conflictf("an attachment named %q already exists on this challenge", filename)
		}
		return gen.ChallengeAttachment{}, fmt.Errorf("challenges: recording attachment: %w", err)
	}
	return att, nil
}

// DeleteAttachment removes an attachment from storage and the database.
func (s *Service) DeleteAttachment(ctx context.Context, challengeID, attachmentID uuid.UUID) error {
	att, err := s.q.GetAttachment(ctx, gen.GetAttachmentParams{ID: attachmentID, ChallengeID: challengeID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrNotFound
		}
		return fmt.Errorf("challenges: get attachment: %w", err)
	}
	if err := s.store.Delete(ctx, att.ObjectKey); err != nil {
		return err
	}
	if err := s.q.DeleteAttachment(ctx, attachmentID); err != nil {
		return fmt.Errorf("challenges: delete attachment: %w", err)
	}
	return nil
}

// OpenAttachment resolves an attachment by challenge slug and attachment ID and
// opens its content for streaming. Visibility is enforced by the caller.
func (s *Service) OpenAttachment(ctx context.Context, challengeID, attachmentID uuid.UUID) (AttachmentContent, error) {
	att, err := s.q.GetAttachment(ctx, gen.GetAttachmentParams{ID: attachmentID, ChallengeID: challengeID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AttachmentContent{}, apperr.ErrNotFound
		}
		return AttachmentContent{}, fmt.Errorf("challenges: get attachment: %w", err)
	}
	body, err := s.store.Get(ctx, att.ObjectKey)
	if err != nil {
		return AttachmentContent{}, err
	}
	return AttachmentContent{
		Filename: att.Filename, ContentType: att.ContentType,
		Size: att.SizeBytes, Body: body,
	}, nil
}
