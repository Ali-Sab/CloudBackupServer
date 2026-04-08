-- +goose Up
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN username;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users ADD COLUMN username VARCHAR(255);
UPDATE users SET username = email;
ALTER TABLE users ALTER COLUMN username SET NOT NULL;
CREATE UNIQUE INDEX users_username_key ON users (username);
-- +goose StatementEnd
