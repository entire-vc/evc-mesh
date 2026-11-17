-- +goose Up
CREATE TABLE ext_hello_notes (id uuid PRIMARY KEY);
ALTER TABLE tasks ADD COLUMN hello_flag boolean;
