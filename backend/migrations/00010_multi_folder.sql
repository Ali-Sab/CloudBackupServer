-- +goose Up

-- +goose StatementBegin
ALTER TABLE watched_paths DROP CONSTRAINT IF EXISTS watched_paths_user_unique;
ALTER TABLE watched_paths ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE file_backups
    ADD COLUMN IF NOT EXISTS watched_path_id BIGINT REFERENCES watched_paths(id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE file_backups
SET watched_path_id = (
    SELECT id FROM watched_paths WHERE user_id = file_backups.user_id LIMIT 1
)
WHERE watched_path_id IS NULL;

ALTER TABLE file_backups ALTER COLUMN watched_path_id SET NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'file_backups_user_path_unique') THEN
    ALTER TABLE file_backups DROP CONSTRAINT file_backups_user_path_unique;
  END IF;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'file_backups_path_relpath_unique') THEN
    ALTER TABLE file_backups
        ADD CONSTRAINT file_backups_path_relpath_unique UNIQUE (watched_path_id, relative_path);
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
ALTER TABLE file_backups DROP CONSTRAINT IF EXISTS file_backups_path_relpath_unique;
ALTER TABLE file_backups DROP COLUMN IF EXISTS watched_path_id;
ALTER TABLE watched_paths DROP COLUMN IF EXISTS name;
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'watched_paths_user_unique') THEN
    ALTER TABLE watched_paths ADD CONSTRAINT watched_paths_user_unique UNIQUE (user_id);
  END IF;
END $$;
-- +goose StatementEnd
