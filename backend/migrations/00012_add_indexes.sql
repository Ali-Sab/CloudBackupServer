-- +goose Up
-- +goose StatementBegin
-- watched_paths.user_id had a UNIQUE constraint in migration 00005 that was
-- dropped in migration 00010 (multi-folder support). Without it there is no
-- index on this column, making GetFolderStats and GetWatchedPathsByUserID
-- perform sequential scans as the table grows.
CREATE INDEX IF NOT EXISTS idx_watched_paths_user_id ON watched_paths(user_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- file_backups.user_id exists (idx_file_backups_user_id from migration 00008)
-- but there is no composite index to accelerate backed_up_at aggregations
-- in GetFolderStats (MAX(fb.backed_up_at) across a folder's backups).
CREATE INDEX IF NOT EXISTS idx_file_backups_path_backed_up
    ON file_backups(watched_path_id, backed_up_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_watched_paths_user_id;
DROP INDEX IF EXISTS idx_file_backups_path_backed_up;
-- +goose StatementEnd
