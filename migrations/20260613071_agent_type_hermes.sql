-- +goose NO TRANSACTION
-- +goose Up
ALTER TYPE agent_type ADD VALUE IF NOT EXISTS 'hermes';
UPDATE agents SET agent_type = 'hermes' WHERE id = '05fb4ea6-29f6-40a1-8150-e370f91525c4';

-- +goose Down
UPDATE agents SET agent_type = 'custom' WHERE id = '05fb4ea6-29f6-40a1-8150-e370f91525c4';
-- Note: PostgreSQL does not support removing enum values; Down only reverts the data row.
