-- +goose Up

-- +goose StatementBegin
-- Deduplicate any rows that snuck in before the UNIQUE constraint existed.
-- Keep the most recently inserted row (highest id) per (user_id, relative_path).
DELETE FROM file_backups
WHERE id NOT IN (
    SELECT MAX(id) FROM file_backups GROUP BY user_id, relative_path
);
-- +goose StatementEnd

-- +goose StatementBegin
-- Add UNIQUE constraint idempotently — PostgreSQL has no ADD CONSTRAINT IF NOT EXISTS.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'file_backups_user_path_unique'
    ) THEN
        ALTER TABLE file_backups
            ADD CONSTRAINT file_backups_user_path_unique UNIQUE (user_id, relative_path);
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- Add version column; existing deduplicated rows start at 1.
ALTER TABLE file_backups ADD COLUMN IF NOT EXISTS version INTEGER NOT NULL DEFAULT 1;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
ALTER TABLE file_backups DROP COLUMN IF EXISTS version;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE file_backups DROP CONSTRAINT IF EXISTS file_backups_user_path_unique;
-- +goose StatementEnd
