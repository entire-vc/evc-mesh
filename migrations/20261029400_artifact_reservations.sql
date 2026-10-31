-- +goose Up

-- Room held on a task for an artifact upload that is still in flight. The
-- per-task size limit counts artifacts plus live reservations, and both the
-- check and the reservation run under the per-task advisory lock, so racing
-- uploads cannot jointly pass the limit, and a refused upload never reaches
-- object storage. The artifact row itself is written only after the object is
-- stored, so listings and downloads never see an artifact without its object.
--
-- A reservation is removed when its upload lands or fails. One left behind by
-- a crashed process stops counting at expires_at and is purged by the next
-- reservation on the same task.
CREATE TABLE artifact_reservations (
    id         UUID PRIMARY KEY,
    task_id    UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    size_bytes BIGINT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_artifact_reservations_task ON artifact_reservations (task_id);

-- +goose Down
DROP TABLE IF EXISTS artifact_reservations;
