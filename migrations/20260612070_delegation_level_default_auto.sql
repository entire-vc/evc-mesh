-- +goose Up
ALTER TABLE tasks ALTER COLUMN delegation_level SET DEFAULT 'auto';

-- +goose Down
ALTER TABLE tasks ALTER COLUMN delegation_level SET DEFAULT 'review';
