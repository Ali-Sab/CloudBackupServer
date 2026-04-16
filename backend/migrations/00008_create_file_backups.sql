-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS file_backups (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    relative_path   TEXT NOT NULL,
    size            BIGINT NOT NULL DEFAULT 0,
    checksum_sha256 TEXT NOT NULL,
    object_key      TEXT NOT NULL,
    backed_up_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    CONSTRAINT file_backups_user_path_unique UNIQUE (user_id, relative_path)
);

CREATE INDEX idx_file_backups_user_id ON file_backups(user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS file_backups;
-- +goose StatementEnd
