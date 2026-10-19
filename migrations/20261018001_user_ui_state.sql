-- +goose Up
-- Per-user interface state that should follow the person across devices
-- (which Docs tree branches are open, later board filters, collapsed columns).
-- One generic table keyed by (user, scope) instead of one table per screen; the
-- shape of `state` belongs to the scope, the API only enforces a size cap and a
-- scope whitelist. Purely additive: nothing reads it until the new endpoints do.
CREATE TABLE user_ui_state (
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope      text        NOT NULL,
    state      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, scope)
);

-- +goose Down
DROP TABLE IF EXISTS user_ui_state;
