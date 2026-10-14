-- +goose Up
-- A delivery to an extension subscription (W1.10b) has no webhook_configs row.
-- Additive: existing rows keep webhook_id, the new column is nullable.
ALTER TABLE webhook_deliveries ALTER COLUMN webhook_id DROP NOT NULL;
ALTER TABLE webhook_deliveries
    ADD COLUMN subscription_id UUID REFERENCES extension_event_subscriptions(id) ON DELETE CASCADE;
ALTER TABLE webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_one_owner
    CHECK ((webhook_id IS NOT NULL) <> (subscription_id IS NOT NULL));
CREATE INDEX idx_webhook_deliveries_subscription
    ON webhook_deliveries (subscription_id, created_at DESC) WHERE subscription_id IS NOT NULL;

DROP POLICY IF EXISTS rls_webhook_deliveries ON webhook_deliveries;
CREATE POLICY rls_webhook_deliveries ON webhook_deliveries
    USING (
        EXISTS (
            SELECT 1 FROM webhook_configs wc
            WHERE wc.id = webhook_deliveries.webhook_id
              AND wc.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
        OR EXISTS (
            SELECT 1 FROM extension_event_subscriptions s
            JOIN integration_configs ic ON ic.id = s.connection_id
            WHERE s.id = webhook_deliveries.subscription_id
              AND ic.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM webhook_configs wc
            WHERE wc.id = webhook_deliveries.webhook_id
              AND wc.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
        OR EXISTS (
            SELECT 1 FROM extension_event_subscriptions s
            JOIN integration_configs ic ON ic.id = s.connection_id
            WHERE s.id = webhook_deliveries.subscription_id
              AND ic.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- +goose Down
DROP POLICY IF EXISTS rls_webhook_deliveries ON webhook_deliveries;
CREATE POLICY rls_webhook_deliveries ON webhook_deliveries
    USING (
        EXISTS (
            SELECT 1 FROM webhook_configs wc
            WHERE wc.id = webhook_deliveries.webhook_id
              AND wc.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM webhook_configs wc
            WHERE wc.id = webhook_deliveries.webhook_id
              AND wc.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );
DROP INDEX IF EXISTS idx_webhook_deliveries_subscription;
ALTER TABLE webhook_deliveries DROP CONSTRAINT IF EXISTS webhook_deliveries_one_owner;
DELETE FROM webhook_deliveries WHERE webhook_id IS NULL;
ALTER TABLE webhook_deliveries DROP COLUMN IF EXISTS subscription_id;
ALTER TABLE webhook_deliveries ALTER COLUMN webhook_id SET NOT NULL;
