-- +goose Up

-- +goose StatementBegin
-- When non-NULL, the version's object_key points to a binary delta (bsdiff format)
-- rather than a full copy of the file. Reconstructing the file requires fetching
-- the base version's bytes and applying the patch.
-- ON DELETE SET NULL: if the base version is somehow removed, the version row is
-- preserved but marked as un-reconstructable (delta_base_version_id becomes NULL
-- while object_key still points to the now-orphaned delta object — handled in code).
ALTER TABLE file_backup_versions
    ADD COLUMN IF NOT EXISTS delta_base_version_id BIGINT
        REFERENCES file_backup_versions(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_fbv_delta_base ON file_backup_versions(delta_base_version_id)
    WHERE delta_base_version_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_fbv_delta_base;
ALTER TABLE file_backup_versions DROP COLUMN IF EXISTS delta_base_version_id;
-- +goose StatementEnd
