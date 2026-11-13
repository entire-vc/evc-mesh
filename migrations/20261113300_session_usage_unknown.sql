-- +goose Up
-- "unknown" usage is not "zero". Per measure (tokens in, tokens out, cost) a
-- session keeps two facts: whether any report gave it a known figure (*_known) and whether any
-- report said the figure is unknown (*_unknown, sticky: a later known report never clears it).
-- A total over sessions is incomplete when any session has *_unknown or no known figure, and
-- is NULL when no session has a known figure.
-- Rows that exist now were read as plain numbers, so they stay known; new rows start unknown
-- until a report gives a figure.
ALTER TABLE agent_sessions
    ADD COLUMN tokens_in_known    boolean NOT NULL DEFAULT true,
    ADD COLUMN tokens_out_known   boolean NOT NULL DEFAULT true,
    ADD COLUMN cost_known         boolean NOT NULL DEFAULT true,
    ADD COLUMN tokens_in_unknown  boolean NOT NULL DEFAULT false,
    ADD COLUMN tokens_out_unknown boolean NOT NULL DEFAULT false,
    ADD COLUMN cost_unknown       boolean NOT NULL DEFAULT false;
ALTER TABLE agent_sessions
    ALTER COLUMN tokens_in_known  SET DEFAULT false,
    ALTER COLUMN tokens_out_known SET DEFAULT false,
    ALTER COLUMN cost_known       SET DEFAULT false;

-- +goose Down
ALTER TABLE agent_sessions
    DROP COLUMN cost_unknown,
    DROP COLUMN tokens_out_unknown,
    DROP COLUMN tokens_in_unknown,
    DROP COLUMN cost_known,
    DROP COLUMN tokens_out_known,
    DROP COLUMN tokens_in_known;
