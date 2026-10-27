-- +goose Up

-- When Mesh last handed the actor's current token to the extension's own
-- service (manifest external.token_path). NULL means the service has not got it
-- yet: the install handshake delivers it then and only then, so a repeated
-- handshake (health, restart) does not rotate. The stored instant is also the
-- signature timestamp of that delivery: the next one is signed strictly later,
-- because the service refuses a timestamp not newer than the last it accepted.
-- Extensions without token_path never write it.
ALTER TABLE extension_actors ADD COLUMN IF NOT EXISTS token_delivered_at TIMESTAMPTZ;

-- +goose Down

ALTER TABLE extension_actors DROP COLUMN IF EXISTS token_delivered_at;
