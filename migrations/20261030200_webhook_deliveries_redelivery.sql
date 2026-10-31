-- +goose NO TRANSACTION
-- +goose Up
-- Redelivery of an extension event (GET/POST /ext/me/events). A redelivery is
-- a new webhook_deliveries row pointing at the original delivery, whose id
-- stays the event's X-Mesh-Delivery on every resend. Additive and nullable:
-- existing rows are originals. No foreign key: the column only ever points at
-- a row of the same subscription, and both go together on the subscription's
-- ON DELETE CASCADE.
ALTER TABLE webhook_deliveries ADD COLUMN IF NOT EXISTS redelivery_of UUID;

-- The event's own id (the X-Mesh-Event-Id of the first send). A payload that is
-- not a JSON object cannot carry it, so a resend reads it from here.
ALTER TABLE webhook_deliveries ADD COLUMN IF NOT EXISTS event_id UUID;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_deliveries_redelivery_of
    ON webhook_deliveries (redelivery_of)
    WHERE redelivery_of IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_webhook_deliveries_redelivery_of;
ALTER TABLE webhook_deliveries DROP COLUMN IF EXISTS event_id;
ALTER TABLE webhook_deliveries DROP COLUMN IF EXISTS redelivery_of;
