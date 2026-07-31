-- +goose Up

-- One account per instance means one display_name per instance: the same row
-- is read by every workspace the person belongs to. So "let a workspace admin
-- fix a member's name" is, mechanically, a cross-tenant write — an admin of
-- workspace A renaming somebody changes what workspace B sees, and B never
-- agreed to that.
--
-- The distinction that makes the useful half safe is provenance, not role.
-- A name nobody ever chose (accounts provisioned with a password, or invites
-- accepted without filling the name in — both land display_name = email) is
-- unowned: filling it in takes nothing away from anyone. A name the person set
-- on themselves is theirs, and no admin of any workspace gets to overwrite it.
--
-- This column records that provenance. It is set to TRUE only by
-- PATCH /api/v1/auth/me, which is the one path where the subject of the change
-- is also its author. The admin path (PATCH /workspaces/:ws_id/members/:user_id
-- with a name) refuses when it is TRUE.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS display_name_self_set BOOLEAN NOT NULL DEFAULT FALSE;

-- Deliberately NOT backfilled to TRUE for rows whose display_name differs from
-- their email. That heuristic reads "has a real name" as "chose it themselves",
-- which is wrong for exactly the population this feature exists for: names
-- seeded by an operator, by config import, or by an invite the inviter typed a
-- name into. Leaving every existing row FALSE means an admin can still correct
-- them, and the first time each person edits their own profile the row flips
-- and locks. The lock is earned by an action, not guessed from a string.

-- +goose Down

ALTER TABLE users DROP COLUMN IF EXISTS display_name_self_set;
