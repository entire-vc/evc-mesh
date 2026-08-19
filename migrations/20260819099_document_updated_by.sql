-- +goose Up

-- "Last updated by" for a document.
--
-- documents already recorded who created it (created_by/created_by_type) and
-- when it last changed (updated_at), but never who made that change — so the
-- Confluence-shaped byline the Docs UI wants, "created by X, last updated by Y
-- on DATE", could only ever be half answered.
--
-- Nullable, and deliberately NOT back-filled. Every row that exists today was
-- written before this column did, and nothing anywhere records who last edited
-- them. created_by is the tempting default and it is a guess: a document created
-- by one person and then edited by five others would claim its creator made the
-- last change, and the UI would print that as fact. NULL is the honest value —
-- it means "not recorded", which is exactly what is true of those rows, and the
-- read model renders it as absent rather than inventing a name.
--
-- From here on the pair is always written: documentService stamps it on create
-- (the create IS the row's most recent change) and on every mutation after, so
-- NULL keeps meaning "predates this column" and nothing else.
--
-- actor_type, not a new enum: the editor is a user or an agent, exactly like
-- created_by_type, and one vocabulary for "who did this" across the table is what
-- lets the name resolution be written once and applied to both columns.
ALTER TABLE documents ADD COLUMN updated_by      UUID;
ALTER TABLE documents ADD COLUMN updated_by_type actor_type;

-- Both or neither. An id with no type cannot be resolved to a name at all — the
-- resolution branches on the type to pick between the agents and the users table
-- — and a type with no id names nobody. Neither half means anything alone, so the
-- pair is constrained rather than trusted to the one service that writes it.
ALTER TABLE documents ADD CONSTRAINT chk_documents_updated_by_pair
    CHECK ((updated_by IS NULL) = (updated_by_type IS NULL));

-- No index. updated_by is a display column, read from a row already located by
-- its id or by its project; nothing filters, joins or orders on it. An index no
-- query reads is write cost with no reader.

-- +goose Down
ALTER TABLE documents DROP CONSTRAINT IF EXISTS chk_documents_updated_by_pair;
ALTER TABLE documents DROP COLUMN IF EXISTS updated_by_type;
ALTER TABLE documents DROP COLUMN IF EXISTS updated_by;
