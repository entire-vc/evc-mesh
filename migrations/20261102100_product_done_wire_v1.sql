-- +goose Up

-- Wire contract board-product-done.authenticated.v1 (additive). The policy gains
-- the rollout scope name the consumer pins; a receipt that consumed an
-- acceptance intent records the scope, the acceptance snapshot hash, the
-- canonical payload digest (and its algorithm), and the request-shaped
-- evidence the consumer sent. The foundation payload_digest, the stored
-- evidence snapshot and every existing row are untouched (defaults are empty).
ALTER TABLE transition_receipt_policies
    ADD COLUMN scope_id TEXT NOT NULL DEFAULT '';

ALTER TABLE task_transition_receipts
    ADD COLUMN scope_id                 TEXT  NOT NULL DEFAULT '',
    ADD COLUMN acceptance_hash          TEXT  NOT NULL DEFAULT '',
    ADD COLUMN payload_digest_v1        TEXT  NOT NULL DEFAULT '',
    ADD COLUMN payload_digest_algorithm TEXT  NOT NULL DEFAULT '',
    ADD COLUMN wire_evidence            JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN wire_evidence_hash       TEXT  NOT NULL DEFAULT '';

-- +goose Down
-- DROP COLUMN does not fire the append-only row trigger; legacy receipt data stays.
ALTER TABLE task_transition_receipts
    DROP COLUMN IF EXISTS wire_evidence_hash,
    DROP COLUMN IF EXISTS wire_evidence,
    DROP COLUMN IF EXISTS payload_digest_algorithm,
    DROP COLUMN IF EXISTS payload_digest_v1,
    DROP COLUMN IF EXISTS acceptance_hash,
    DROP COLUMN IF EXISTS scope_id;
ALTER TABLE transition_receipt_policies DROP COLUMN IF EXISTS scope_id;
