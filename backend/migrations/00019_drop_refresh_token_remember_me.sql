-- +goose Up
-- +goose StatementBegin
ALTER TABLE refresh_tokens DROP COLUMN IF EXISTS remember_me;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS remember_me BOOLEAN NOT NULL DEFAULT FALSE;
-- +goose StatementEnd
