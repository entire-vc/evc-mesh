-- +goose NO TRANSACTION
-- +goose Up
-- An extension actor uploads artifacts under its own identity (like comments and
-- cards, which use actor_type 'extension' since 20261014004). Without the value
-- the insert failed with 22P02, surfaced as 400 "invalid value for field".
-- ADD VALUE cannot run inside a transaction block, hence NO TRANSACTION.
ALTER TYPE uploader_type ADD VALUE IF NOT EXISTS 'extension';

-- +goose Down
-- Postgres cannot drop an enum value; rows signed 'extension' stay valid.
SELECT 1;
