-- +goose Up

-- Per-instance flags must be globally unique: two live instances sharing a flag would
-- make the sharing-detection lookup (FindInstanceByFlag) ambiguous and could silently
-- hand two teams the same flag. The generator already produces high-entropy values;
-- this enforces it at the database so a collision fails the insert instead of corrupting
-- scoring. Partial index — static/shared challenges leave flag NULL, and many NULLs are
-- allowed.
CREATE UNIQUE INDEX IF NOT EXISTS uq_instances_flag ON instances (flag) WHERE flag IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS uq_instances_flag;
