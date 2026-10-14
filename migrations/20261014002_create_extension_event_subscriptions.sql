-- +goose Up
CREATE TABLE extension_event_subscriptions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id UUID NOT NULL REFERENCES integration_configs(id) ON DELETE CASCADE,
    event         VARCHAR(64) NOT NULL,
    delivery      VARCHAR(16) NOT NULL CHECK (delivery IN ('webhook', 'nats')),
    target        TEXT NOT NULL DEFAULT '',
    secret_ref    VARCHAR(128) NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT extension_event_subscriptions_unique UNIQUE (connection_id, event, delivery, target)
);

CREATE INDEX idx_extension_event_subscriptions_event ON extension_event_subscriptions (event);

-- +goose Down
DROP TABLE IF EXISTS extension_event_subscriptions;
