-- +goose Up

-- native-review-admission/v1 (A1, Mesh backend).
--
-- One authoritative consistency boundary: this database. The canonical
-- reservation and its single synthetic marker are inserted by one transaction
-- that first takes the conflicting guard locks (enrollment, then authority, in
-- that fixed order) and only then re-reads revocation, window and bindings.
-- Everything here is additive; nothing touches task_acceptance_intents or the
-- product_done policy.

-- Trusted enrollment: who may issue, publish and consume, for which exact
-- scope/pins/product bindings. Written by a trusted operator, never by the
-- publisher or the reviewer; the issue path takes every non-body fact from here.
CREATE TABLE native_review_enrollments (
    id                       UUID        PRIMARY KEY,
    workspace_id             UUID        NOT NULL,
    project_id               UUID        NOT NULL,
    issuer_actor_id          UUID        NOT NULL,
    publisher_actor_id       UUID        NOT NULL,
    transport_source_id      UUID        NOT NULL,
    consumer_actor_id        UUID        NOT NULL,
    authority_issuer_actor_id UUID       NOT NULL,
    result_author_actor_id   UUID        NOT NULL,
    owner_actor_id           UUID        NOT NULL,
    -- identity of the future guard, reserved here by the operator BEFORE any
    -- receipt exists; it is not a grant, a reservation or a clock start.
    authority_id             UUID        NOT NULL UNIQUE,
    -- Ed25519 public key (canonical base64, 32 bytes) of the enrolled issuer
    -- signature; never taken from a packet or the publisher.
    issuer_key               TEXT        NOT NULL CHECK (issuer_key ~ '^[A-Za-z0-9+/]{43}=$'),
    -- exact receiver and native binding pinned by the operator.
    receiver_task            UUID        NOT NULL,
    receiver_attempt         UUID        NOT NULL,
    receiver_session         UUID        NOT NULL,
    receiver_generation      BIGINT      NOT NULL CHECK (receiver_generation > 0),
    native_registration      TEXT        NOT NULL CHECK (native_registration ~ '^[0-9a-f]{64}$'),
    native_stage             TEXT        NOT NULL CHECK (native_stage ~ '^[a-z][a-z0-9-]{0,63}$'),
    native_package_sha256    TEXT        NOT NULL CHECK (native_package_sha256 ~ '^[0-9a-f]{64}$'),
    review_task_id           UUID        NOT NULL,
    source_task_id           UUID        NOT NULL,
    reviewer_family          TEXT        NOT NULL CHECK (reviewer_family <> ''),
    author_family            TEXT        NOT NULL CHECK (author_family <> ''),
    scope_domain             TEXT        NOT NULL CHECK (scope_domain = 't12-successor-synthetic'),
    scope_namespace          UUID        NOT NULL,
    scope_test               BOOLEAN     NOT NULL CHECK (scope_test),
    criteria_sha256          TEXT        NOT NULL CHECK (criteria_sha256 ~ '^[0-9a-f]{64}$'),
    context_sha256           TEXT        NOT NULL CHECK (context_sha256 ~ '^[0-9a-f]{64}$'),
    source_repo              TEXT        NOT NULL CHECK (source_repo <> ''),
    source_sha               TEXT        NOT NULL CHECK (source_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    package_sha256           TEXT        NOT NULL CHECK (package_sha256 ~ '^[0-9a-f]{64}$'),
    manifest_sha256          TEXT        NOT NULL CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
    config_sha256            TEXT        NOT NULL CHECK (config_sha256 ~ '^[0-9a-f]{64}$'),
    product_task_id          UUID        NOT NULL,
    product_observation_id   TEXT        NOT NULL CHECK (product_observation_id ~ '^[0-9a-f]{32}$'),
    product_session_id       TEXT        NOT NULL CHECK (product_session_id ~ '^[0-9a-f]{32}$'),
    -- original product bytes, available to the verifier; digests are checked
    -- against them by the database itself.
    authority_bytes          BYTEA       NOT NULL,
    root_disposition_bytes   BYTEA       NOT NULL,
    authority_sha256         TEXT        NOT NULL,
    root_disposition_sha256  TEXT        NOT NULL,
    created_by               UUID        NOT NULL,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT nre_authority_key UNIQUE (id, authority_id),
    CONSTRAINT nre_independent_actor CHECK (issuer_actor_id <> result_author_actor_id),
    CONSTRAINT nre_independent_family CHECK (reviewer_family <> author_family),
    CONSTRAINT nre_publisher_not_issuer CHECK (publisher_actor_id <> issuer_actor_id),
    CONSTRAINT nre_authority_digest CHECK (authority_sha256 = encode(sha256(authority_bytes), 'hex')),
    CONSTRAINT nre_root_digest CHECK (root_disposition_sha256 = encode(sha256(root_disposition_bytes), 'hex'))
);

CREATE TABLE native_review_enrollment_revocations (
    enrollment_id UUID        PRIMARY KEY REFERENCES native_review_enrollments(id),
    revoked_by    UUID        NOT NULL,
    reason        TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Immutable reviewer receipt: the whole canonical receipt, the full verdict
-- bytes and their digest. No foreign key to comments/tasks content: editing or
-- deleting the original comment cannot reach it.
CREATE TABLE native_review_receipts (
    id                 UUID        PRIMARY KEY,
    enrollment_id      UUID        NOT NULL REFERENCES native_review_enrollments(id),
    authority_id       UUID        NOT NULL,
    issuer_actor_id    UUID        NOT NULL,
    verdict            TEXT        NOT NULL CHECK (verdict IN ('SHIP', 'DO-NOT-SHIP')),
    receiver_task      UUID        NOT NULL,
    receiver_attempt   UUID        NOT NULL,
    receiver_session   UUID        NOT NULL,
    receiver_generation BIGINT     NOT NULL CHECK (receiver_generation > 0),
    native_registration TEXT       NOT NULL CHECK (native_registration ~ '^[0-9a-f]{64}$'),
    native_binding     JSONB       NOT NULL,
    body_bytes         BYTEA       NOT NULL CHECK (octet_length(body_bytes) > 0),
    body_sha256        TEXT        NOT NULL,
    canonical          BYTEA       NOT NULL,
    receipt_sha256     TEXT        NOT NULL UNIQUE,
    -- Ed25519 over "native-review-admission/v1\n" || ascii(receipt_sha256), 64 bytes
    issuer_signature   TEXT        NOT NULL CHECK (issuer_signature ~ '^[A-Za-z0-9+/]{86}==$'),
    -- one server UTC timestamp (whole seconds), persisted at issue, unsigned
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- one receipt per enrollment, carrying the operator-reserved authority id
    CONSTRAINT nrr_one_per_enrollment UNIQUE (enrollment_id),
    CONSTRAINT nrr_authority_key UNIQUE (id, authority_id),
    CONSTRAINT nrr_enrollment_authority FOREIGN KEY (enrollment_id, authority_id)
        REFERENCES native_review_enrollments(id, authority_id),
    CONSTRAINT nrr_body_digest CHECK (body_sha256 = encode(sha256(body_bytes), 'hex')),
    CONSTRAINT nrr_receipt_digest CHECK (receipt_sha256 = encode(sha256(canonical), 'hex'))
);

-- Mutable guard row: the lock target for reserve, revoke and receiver turnover.
-- Carries no budget decision of its own; the consumed state is the reservation
-- row below.
CREATE TABLE native_review_authorities (
    id                  UUID        PRIMARY KEY,
    enrollment_id       UUID        NOT NULL REFERENCES native_review_enrollments(id),
    receipt_id          UUID        NOT NULL UNIQUE REFERENCES native_review_receipts(id),
    created_by          UUID        NOT NULL,
    budget_id           UUID        NOT NULL UNIQUE,
    budget_ms           INT         NOT NULL CHECK (budget_ms = 1000),
    max_reservations    INT         NOT NULL CHECK (max_reservations = 1),
    -- unsigned decimal strings, canonical, exact nanoseconds
    not_before_ns       TEXT        NOT NULL CHECK (not_before_ns ~ '^[1-9][0-9]{0,18}$'),
    expires_ns          TEXT        NOT NULL CHECK (expires_ns ~ '^[1-9][0-9]{0,18}$'),
    clock_binding_sha256 TEXT       NOT NULL CHECK (clock_binding_sha256 ~ '^[0-9a-f]{64}$'),
    receiver_task       UUID        NOT NULL,
    receiver_attempt    UUID        NOT NULL,
    receiver_session    UUID        NOT NULL,
    receiver_generation BIGINT      NOT NULL CHECK (receiver_generation > 0),
    revision            BIGINT      NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- the guard materialises ONLY the operator-reserved id and exactly the
    -- receipt that carries it: no rebind, no other id.
    CONSTRAINT nra_receipt_authority FOREIGN KEY (receipt_id, id)
        REFERENCES native_review_receipts(id, authority_id)
);

CREATE TABLE native_review_authority_revocations (
    authority_id UUID        PRIMARY KEY REFERENCES native_review_authorities(id),
    revoked_by   UUID        NOT NULL,
    reason       TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Append-only link between an immutable receipt and the persisted native event.
CREATE TABLE native_review_delivery_links (
    registration TEXT        NOT NULL CHECK (registration ~ '^[0-9a-f]{64}$'),
    event_id     TEXT        NOT NULL CHECK (event_id <> ''),
    receipt_id   UUID        NOT NULL REFERENCES native_review_receipts(id),
    recorded_by  UUID        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (registration, event_id)
);

-- The single canonical reservation. UNIQUE(authority_id) and UNIQUE(budget_id)
-- are the once-budget: different request ids cannot create a second one.
CREATE TABLE native_review_reservations (
    id                  UUID        PRIMARY KEY,
    authority_id        UUID        NOT NULL UNIQUE REFERENCES native_review_authorities(id),
    budget_id           UUID        NOT NULL UNIQUE,
    receipt_id          UUID        NOT NULL REFERENCES native_review_receipts(id),
    receipt_sha256      TEXT        NOT NULL,
    request_id          UUID        NOT NULL UNIQUE,
    payload_digest      TEXT        NOT NULL,
    native_registration TEXT        NOT NULL,
    native_event_id     TEXT        NOT NULL,
    consumer_actor_id   UUID        NOT NULL,
    receiver_task       UUID        NOT NULL,
    receiver_attempt    UUID        NOT NULL,
    receiver_session    UUID        NOT NULL,
    receiver_generation BIGINT      NOT NULL,
    clock_evidence      JSONB       NOT NULL,
    committed_at        TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

-- The one canonical synthetic marker, inserted in the reservation transaction.
CREATE TABLE native_review_markers (
    marker_id      UUID PRIMARY KEY,
    reservation_id UUID NOT NULL UNIQUE REFERENCES native_review_reservations(id),
    authority_id   UUID NOT NULL UNIQUE REFERENCES native_review_authorities(id)
);

-- +goose StatementBegin
CREATE FUNCTION native_review_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER nre_append_only BEFORE UPDATE OR DELETE ON native_review_enrollments
    FOR EACH ROW EXECUTE FUNCTION native_review_append_only();
CREATE TRIGGER nrer_append_only BEFORE UPDATE OR DELETE ON native_review_enrollment_revocations
    FOR EACH ROW EXECUTE FUNCTION native_review_append_only();
CREATE TRIGGER nrr_append_only BEFORE UPDATE OR DELETE ON native_review_receipts
    FOR EACH ROW EXECUTE FUNCTION native_review_append_only();
CREATE TRIGGER nrar_append_only BEFORE UPDATE OR DELETE ON native_review_authority_revocations
    FOR EACH ROW EXECUTE FUNCTION native_review_append_only();
CREATE TRIGGER nrdl_append_only BEFORE UPDATE OR DELETE ON native_review_delivery_links
    FOR EACH ROW EXECUTE FUNCTION native_review_append_only();
CREATE TRIGGER nrres_append_only BEFORE UPDATE OR DELETE ON native_review_reservations
    FOR EACH ROW EXECUTE FUNCTION native_review_append_only();
CREATE TRIGGER nrm_append_only BEFORE UPDATE OR DELETE ON native_review_markers
    FOR EACH ROW EXECUTE FUNCTION native_review_append_only();

-- +goose Down
DROP TRIGGER IF EXISTS nrm_append_only ON native_review_markers;
DROP TRIGGER IF EXISTS nrres_append_only ON native_review_reservations;
DROP TRIGGER IF EXISTS nrdl_append_only ON native_review_delivery_links;
DROP TRIGGER IF EXISTS nrar_append_only ON native_review_authority_revocations;
DROP TRIGGER IF EXISTS nrr_append_only ON native_review_receipts;
DROP TRIGGER IF EXISTS nrer_append_only ON native_review_enrollment_revocations;
DROP TRIGGER IF EXISTS nre_append_only ON native_review_enrollments;
DROP TABLE IF EXISTS native_review_markers;
DROP TABLE IF EXISTS native_review_reservations;
DROP TABLE IF EXISTS native_review_delivery_links;
DROP TABLE IF EXISTS native_review_authority_revocations;
DROP TABLE IF EXISTS native_review_authorities;
DROP TABLE IF EXISTS native_review_receipts;
DROP TABLE IF EXISTS native_review_enrollment_revocations;
DROP TABLE IF EXISTS native_review_enrollments;
DROP FUNCTION IF EXISTS native_review_append_only();
