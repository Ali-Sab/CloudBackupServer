-- +goose Up

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS file_backup_versions (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    watched_path_id BIGINT NOT NULL REFERENCES watched_paths(id) ON DELETE CASCADE,
    relative_path   TEXT NOT NULL,
    version         INTEGER NOT NULL,
    size            BIGINT NOT NULL DEFAULT 0,
    checksum_sha256 TEXT NOT NULL,
    object_key      TEXT NOT NULL,
    backed_up_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (watched_path_id, relative_path, version)
);

CREATE INDEX idx_fbv_user_backed_up ON file_backup_versions(user_id, backed_up_at DESC);
CREATE INDEX idx_fbv_watched_path   ON file_backup_versions(watched_path_id, relative_path);
-- +goose StatementEnd

-- +goose StatementBegin
-- Backfill existing backups so history and version lists are populated immediately.
INSERT INTO file_backup_versions
    (user_id, watched_path_id, relative_path, version, size, checksum_sha256, object_key, backed_up_at)
SELECT user_id, watched_path_id, relative_path, version, size, checksum_sha256, object_key, backed_up_at
FROM file_backups
ON CONFLICT (watched_path_id, relative_path, version) DO NOTHING;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS file_backup_versions;
-- +goose StatementEnd
