// Package audit records admin-relevant actions to the audit_log table.
// Meta must never contain flags or password hashes.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/db/gen"
)

// Logger writes audit entries. Failures are logged, not propagated — an audit
// write must never fail the action it records.
type Logger struct {
	q   *gen.Queries
	log *slog.Logger
}

// New builds an audit logger.
func New(q *gen.Queries, log *slog.Logger) *Logger {
	return &Logger{q: q, log: log}
}

// Log records one action. actorID may be uuid.Nil for system actions.
func (l *Logger) Log(ctx context.Context, actorID uuid.UUID, action, subjectType, subjectID string, meta map[string]any) {
	if meta == nil {
		meta = map[string]any{}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		raw = []byte("{}")
	}
	var actor *uuid.UUID
	if actorID != uuid.Nil {
		actor = &actorID
	}
	if err := l.q.InsertAuditLog(ctx, gen.InsertAuditLogParams{
		ActorID:     actor,
		Action:      action,
		SubjectType: subjectType,
		SubjectID:   subjectID,
		Meta:        raw,
	}); err != nil {
		l.log.Error("audit write failed", "action", action, "subject", subjectID, "error", err.Error())
	}
}
