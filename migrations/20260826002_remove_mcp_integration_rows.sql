-- +goose Up

-- The "mcp" integration_configs provider row has never governed any code
-- path (grep confirms: only the create/update allow-list referenced it, no
-- handler or service ever read a row back for behavior — #4a3195a5). It is
-- a pure connection-instruction card on the frontend, not a channel, so
-- is_active is a switch that switches nothing. As of this migration
-- Configure/Update reject provider="mcp" outright (400), so these rows can
-- never be recreated through the API — deleting them here is the one-time
-- cleanup, not an ongoing invariant that needs re-enforcing elsewhere.
DELETE FROM integration_configs WHERE provider = 'mcp';

-- +goose Down

-- Down is a structural no-op: the rows this Up deleted carried no
-- information (config='{}' on every row observed in prod, #4a3195a5) and
-- provider="mcp" cannot be recreated through the API after this change
-- ships, so there is nothing meaningful to restore on rollback.
