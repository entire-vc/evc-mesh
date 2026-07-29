-- +goose Up

-- notification_preferences had no uniqueness on the thing it is actually keyed
-- by: one workspace, one actor, one channel. UpsertPreference now finds the
-- existing row before writing, but that is a rule the application follows
-- rather than one the table enforces, and it does nothing about rows already
-- stored by the version that did not.
--
-- The duplicates have to go before the index can be built, so this migration
-- folds them first. Our own instance has an empty table and will pass either
-- way; the fold is written for a self-hoster whose table is not empty, where
-- getting it wrong silently discards whichever settings it drops.

-- 1. Fold the newest values onto the surviving row.
--
-- Survivor is the OLDEST row of each group, so that ids handed out earlier —
-- the ones a client may have stored, or a DELETE .../preferences/:pref_id may
-- name — keep resolving. The VALUES folded onto it come from the NEWEST row, so
-- what survives is the last setting the user saved rather than the first. Those
-- are two different rows, which is the whole reason this is an UPDATE and not
-- just a DELETE.
WITH keyed AS (
    SELECT
        id,
        count(*) OVER grp AS group_size,
        row_number() OVER (PARTITION BY workspace_id, user_id, agent_id, channel
                           ORDER BY created_at ASC, id ASC) AS oldest_rank,
        first_value(events)     OVER newest AS newest_events,
        first_value(is_enabled) OVER newest AS newest_is_enabled,
        first_value(config)     OVER newest AS newest_config
    FROM notification_preferences
    WINDOW
        grp AS (PARTITION BY workspace_id, user_id, agent_id, channel),
        newest AS (PARTITION BY workspace_id, user_id, agent_id, channel
                   ORDER BY updated_at DESC, created_at DESC, id DESC)
)
UPDATE notification_preferences p
SET events     = k.newest_events,
    is_enabled = k.newest_is_enabled,
    config     = k.newest_config,
    updated_at = now()
FROM keyed k
WHERE p.id = k.id
  AND k.oldest_rank = 1
  AND k.group_size > 1;

-- 2. Remove the rows that were just folded away.
WITH keyed AS (
    SELECT
        id,
        row_number() OVER (PARTITION BY workspace_id, user_id, agent_id, channel
                           ORDER BY created_at ASC, id ASC) AS oldest_rank
    FROM notification_preferences
)
DELETE FROM notification_preferences p
USING keyed k
WHERE p.id = k.id
  AND k.oldest_rank > 1;

-- 3. Make it the table's rule rather than the application's.
--
-- Two partial indexes, not one three-column index: chk_single_actor leaves the
-- other actor column NULL, and before PostgreSQL 15 every NULL is distinct in a
-- unique index, so a plain UNIQUE (workspace_id, user_id, agent_id, channel)
-- would permit exactly the duplicates this is meant to stop.
CREATE UNIQUE INDEX IF NOT EXISTS uq_notif_prefs_user_channel
    ON notification_preferences (workspace_id, user_id, channel)
    WHERE user_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_notif_prefs_agent_channel
    ON notification_preferences (workspace_id, agent_id, channel)
    WHERE agent_id IS NOT NULL;

-- +goose Down

-- Only the constraint comes back off. The folded rows are not restored: their
-- values were merged into the survivor, so there is nothing left to restore
-- them from, and re-splitting them would recreate the bug.
DROP INDEX IF EXISTS uq_notif_prefs_user_channel;
DROP INDEX IF EXISTS uq_notif_prefs_agent_channel;
