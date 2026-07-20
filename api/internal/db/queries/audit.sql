-- name: InsertAuditLog :exec
INSERT INTO audit_log (actor_id, action, subject_type, subject_id, meta)
VALUES ($1, $2, $3, $4, $5);
