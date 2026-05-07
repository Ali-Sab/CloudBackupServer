-- +goose Up

-- +goose StatementBegin
-- Speeds up the reference-count query used before deleting a content-addressable
-- blob object: COUNT(*) FROM file_backup_versions WHERE object_key = $1.
CREATE INDEX IF NOT EXISTS idx_fbv_object_key ON file_backup_versions(object_key);
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_fbv_object_key;
-- +goose StatementEnd
