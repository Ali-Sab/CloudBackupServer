-- +goose Up
-- +goose StatementBegin
-- relative_path stores the POSIX path of each entry relative to the watched root
-- directory, e.g. "photos/2024/img.jpg". Top-level entries have relative_path = name.
-- The DEFAULT '' keeps this migration safe on any pre-existing rows.
ALTER TABLE watched_files ADD COLUMN relative_path TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE watched_files DROP COLUMN relative_path;
-- +goose StatementEnd
