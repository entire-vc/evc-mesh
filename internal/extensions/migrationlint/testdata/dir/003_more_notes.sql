-- +goose Up
ALTER TABLE ext_hello_notes ADD COLUMN body text;
CREATE INDEX hello_by_title ON tasks (title);
