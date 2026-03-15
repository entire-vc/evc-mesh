-- +goose Up
-- Add embedding columns to memories table (nullable — vector search is optional).
-- We store embeddings as TEXT (JSON array) so that pgvector is not required at the DB level.
-- The application layer handles encoding/decoding and vector similarity via application code
-- when pgvector is not available, or casts to the vector type when pgvector IS installed.
ALTER TABLE memories ADD COLUMN IF NOT EXISTS embedding TEXT;
ALTER TABLE memories ADD COLUMN IF NOT EXISTS embedding_model TEXT;
ALTER TABLE memories ADD COLUMN IF NOT EXISTS embedding_dim INT;

-- +goose Down
ALTER TABLE memories DROP COLUMN IF EXISTS embedding_dim;
ALTER TABLE memories DROP COLUMN IF EXISTS embedding_model;
ALTER TABLE memories DROP COLUMN IF EXISTS embedding;
