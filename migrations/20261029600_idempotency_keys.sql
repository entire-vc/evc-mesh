-- +goose Up

-- Idempotency-Key records for retried writes (POST comments, artifacts, finish).
-- A key is scoped to the actor and the request (method + path): the same key
-- from another actor or on another task is a different record. The first
-- request claims the row in state in_flight; when it answers, the response is
-- stored and a repeat with the same body gets it back instead of writing again.
--
-- A row expires at expires_at (24h after the claim); an in_flight row whose
-- request died with its process is taken over after claimed_at + 5 minutes.
-- Expired rows are purged by later claims.
CREATE TABLE idempotency_keys (
    actor_type        TEXT        NOT NULL,
    actor_id          UUID        NOT NULL,
    method            TEXT        NOT NULL,
    path              TEXT        NOT NULL,
    idem_key          TEXT        NOT NULL,
    request_hash      TEXT        NOT NULL,
    -- claim_token fences a claim: only the request that took it may complete or
    -- release it, so a request whose claim was taken over writes nothing here.
    claim_token       UUID        NOT NULL,
    state             TEXT        NOT NULL CHECK (state IN ('in_flight', 'done')),
    response_status   INT,
    response_type     TEXT,
    response_body     BYTEA,
    claimed_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at        TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (actor_type, actor_id, method, path, idem_key)
);

CREATE INDEX idx_idempotency_keys_expires ON idempotency_keys (expires_at);

-- +goose Down
DROP TABLE IF EXISTS idempotency_keys;
