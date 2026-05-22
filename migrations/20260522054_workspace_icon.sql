-- +goose Up
ALTER TABLE workspaces ADD COLUMN icon_url TEXT;

-- +goose Down
ALTER TABLE workspaces DROP COLUMN icon_url;
