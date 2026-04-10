-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS watched_files (
    id           BIGSERIAL PRIMARY KEY,
    path_id      BIGINT NOT NULL REFERENCES watched_paths(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    is_directory BOOLEAN NOT NULL DEFAULT FALSE,
    size         BIGINT NOT NULL DEFAULT 0,
    modified_ms  BIGINT NOT NULL DEFAULT 0,
    created_at   TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_watched_files_path_id ON watched_files(path_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS watched_files;
-- +goose StatementEnd
