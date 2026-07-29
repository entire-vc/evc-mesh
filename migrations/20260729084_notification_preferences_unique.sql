-- +goose Up
-- +goose StatementBegin

-- notification_preferences had no uniqueness key, and the upsert that writes it
-- conflicts on the primary key. The handler never looked an existing row up, so
-- every PUT arrived with a fresh id, the ON CONFLICT could not fire, and each
-- update inserted another row instead of changing the one already there.
-- Preferences did not update; they accumulated, and the newest row was not
-- necessarily the one a reader found first.
--
-- Deduplicate before adding the index, because CREATE UNIQUE INDEX fails on a
-- table that already has duplicates and every instance that has been running
-- this endpoint has them. Our own table happens to be empty, which is exactly
-- why this has to be written for the instance that is not.
--
-- The row kept is the OLDEST per key: it is the one whose id any other row could
-- be referencing, and the newer duplicates only ever carried the same
-- (workspace, subscriber, channel) subscription. The events/is_enabled/config of
-- the most recent duplicate are folded onto it first, so the surviving row
-- reflects the last update the user actually made rather than their first.

-- +goose StatementEnd

-- +goose StatementBegin
WITH ranked AS (
    SELECT
        id,
        first_value(id) OVER w  AS keep_id,
        last_value(id)  OVER w2 AS newest_id
    FROM notification_preferences
    WINDOW
        w AS (
            PARTITION BY workspace_id, user_id, agent_id, channel
            ORDER BY created_at ASC, id ASC
        ),
        w2 AS (
            PARTITION BY workspace_id, user_id, agent_id, channel
            ORDER BY created_at ASC, id ASC
            ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING
        )
)
UPDATE notification_preferences p
SET events     = newest.events,
    is_enabled = newest.is_enabled,
    config     = newest.config,
    updated_at = now()
FROM ranked r
JOIN notification_preferences newest ON newest.id = r.newest_id
WHERE p.id = r.keep_id
  AND r.keep_id <> r.newest_id;
-- +goose StatementEnd

-- +goose StatementBegin
DELETE FROM notification_preferences p
USING (
    SELECT
        id,
        row_number() OVER (
            PARTITION BY workspace_id, user_id, agent_id, channel
            ORDER BY created_at ASC, id ASC
        ) AS rn
    FROM notification_preferences
) dup
WHERE p.id = dup.id
  AND dup.rn > 1;
-- +goose StatementEnd

-- +goose StatementBegin
-- Two partial indexes rather than one composite: user_id and agent_id are
-- nullable and chk_single_actor guarantees exactly one of them is set, so a
-- single index over both columns would never collide (NULL is distinct from
-- NULL in a unique index).
CREATE UNIQUE INDEX IF NOT EXISTS uq_notif_prefs_user_channel
    ON notification_preferences (workspace_id, user_id, channel)
    WHERE user_id IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX IF NOT EXISTS uq_notif_prefs_agent_channel
    ON notification_preferences (workspace_id, agent_id, channel)
    WHERE agent_id IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
-- dispatch() reads every enabled preference in a workspace and then has to
-- establish that each row's owner is actually in it. This index is what keeps
-- that join off a sequential scan on the workspace's rows.
CREATE INDEX IF NOT EXISTS idx_notif_prefs_ws_enabled
    ON notification_preferences (workspace_id)
    WHERE is_enabled = true;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_notif_prefs_ws_enabled;
DROP INDEX IF EXISTS uq_notif_prefs_agent_channel;
DROP INDEX IF EXISTS uq_notif_prefs_user_channel;
-- +goose StatementEnd
