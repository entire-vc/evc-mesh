-- +goose Up
-- The last marker-only end of turn gets a typed wait: `manual` is a passive
-- wait a lead allowed with the card label wake:manual (`WAIT manual:#<8hex>`).
-- It has no target, observer or timer; a new comment or a lead's action ends it.
ALTER TABLE task_waits DROP CONSTRAINT task_waits_kind_check;
ALTER TABLE task_waits ADD CONSTRAINT task_waits_kind_check CHECK (
    kind IN ('card', 'mr', 'pipeline', 'time', 'decision', 'human', 'merge', 'registered', 'manual'));

-- +goose Down
-- Active manual waits are ended first: the old check comes back NOT VALID
-- (manual rows stay as history) and Postgres checks a NOT VALID constraint on
-- every later UPDATE of a row, so an active one could never be resolved again.
UPDATE task_waits SET resolved_at = now(), resolution = 'invalid'
 WHERE resolved_at IS NULL AND kind = 'manual';

ALTER TABLE task_waits DROP CONSTRAINT task_waits_kind_check;
ALTER TABLE task_waits ADD CONSTRAINT task_waits_kind_check CHECK (
    kind IN ('card', 'mr', 'pipeline', 'time', 'decision', 'human', 'merge', 'registered')) NOT VALID;
