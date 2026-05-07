-- +goose Up

-- +goose StatementBegin
ALTER TABLE file_backup_versions
    ADD COLUMN IF NOT EXISTS restored_from_version_id BIGINT
        REFERENCES file_backup_versions(id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
ALTER TABLE file_backup_versions DROP COLUMN IF EXISTS restored_from_version_id;
-- +goose StatementEnd
