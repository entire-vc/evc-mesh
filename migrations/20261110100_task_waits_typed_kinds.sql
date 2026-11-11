-- +goose Up
-- Every end-of-turn form of the marker grammar gets a typed wait (the typed
-- command becomes the only way an agent ends a turn):
--   merge      — the runner's merge gate on a GitLab MR at the reviewed head
--                (`WAIT merge:<project>!<iid> after=<sha>`);
--   registered — a runner-owned registration the server cannot observe
--                (ops-review, gmail, receipt, lifecycle, stall, recovery,
--                queue, native), named by its sub-kind and an opaque ref;
--   human      — already a kind; it now carries the person asked and the
--                question (`❓ Blocking @<person>: <question>`).
-- mr and pipeline waits may carry an explicit baseline (`after=`), pipeline
-- waits may say `changed` (a new observation of a retried pipeline).
ALTER TABLE task_waits
    ADD COLUMN after_sha         TEXT,
    ADD COLUMN after_pipeline_id BIGINT,
    ADD COLUMN after_status      TEXT,
    ADD COLUMN registration      TEXT,
    ADD COLUMN registration_ref  TEXT,
    ADD COLUMN addressee         TEXT,
    ADD COLUMN question          TEXT;

ALTER TABLE task_waits DROP CONSTRAINT task_waits_kind_check;
ALTER TABLE task_waits ADD CONSTRAINT task_waits_kind_check CHECK (
    kind IN ('card', 'mr', 'pipeline', 'time', 'decision', 'human', 'merge', 'registered'));

ALTER TABLE task_waits DROP CONSTRAINT task_waits_pipeline_target;
ALTER TABLE task_waits ADD CONSTRAINT task_waits_pipeline_target CHECK (kind <> 'pipeline' OR (
    project_path IS NOT NULL AND pipeline_id IS NOT NULL
    AND until_state IN ('finished', 'green', 'changed')));

ALTER TABLE task_waits
    ADD CONSTRAINT task_waits_merge_target CHECK (kind <> 'merge' OR (
        project_path IS NOT NULL AND mr_iid IS NOT NULL AND after_sha IS NOT NULL)),
    ADD CONSTRAINT task_waits_registered_target CHECK ((kind = 'registered') = (registration IS NOT NULL)
        AND (kind <> 'registered' OR (registration_ref IS NOT NULL AND registration IN
            ('ops-review', 'gmail', 'receipt', 'lifecycle', 'stall', 'recovery', 'queue', 'native')))),
    ADD CONSTRAINT task_waits_after_sha_shape CHECK (
        after_sha IS NULL OR after_sha ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    ADD CONSTRAINT task_waits_after_baseline CHECK (
        (after_pipeline_id IS NULL AND after_status IS NULL) OR after_sha IS NOT NULL),
    ADD CONSTRAINT task_waits_typed_lengths CHECK (
        char_length(after_status) <= 40 AND char_length(registration_ref) <= 200
        AND char_length(addressee) <= 200 AND char_length(question) <= 4000);

-- +goose Down
-- Active waits of the new shapes are ended first: the old checks come back
-- NOT VALID (rows of the new kinds stay as history), and Postgres checks a NOT
-- VALID constraint on every later UPDATE of a row, so an active one could
-- never be resolved again.
UPDATE task_waits SET resolved_at = now(), resolution = 'invalid'
 WHERE resolved_at IS NULL
   AND (kind IN ('merge', 'registered') OR (kind = 'pipeline' AND until_state = 'changed'));

ALTER TABLE task_waits
    DROP CONSTRAINT IF EXISTS task_waits_typed_lengths,
    DROP CONSTRAINT IF EXISTS task_waits_after_baseline,
    DROP CONSTRAINT IF EXISTS task_waits_after_sha_shape,
    DROP CONSTRAINT IF EXISTS task_waits_registered_target,
    DROP CONSTRAINT IF EXISTS task_waits_merge_target;

ALTER TABLE task_waits DROP CONSTRAINT task_waits_pipeline_target;
ALTER TABLE task_waits ADD CONSTRAINT task_waits_pipeline_target CHECK (kind <> 'pipeline' OR (
    project_path IS NOT NULL AND pipeline_id IS NOT NULL
    AND until_state IN ('finished', 'green'))) NOT VALID;

ALTER TABLE task_waits DROP CONSTRAINT task_waits_kind_check;
ALTER TABLE task_waits ADD CONSTRAINT task_waits_kind_check CHECK (
    kind IN ('card', 'mr', 'pipeline', 'time', 'decision', 'human')) NOT VALID;

ALTER TABLE task_waits
    DROP COLUMN IF EXISTS question,
    DROP COLUMN IF EXISTS addressee,
    DROP COLUMN IF EXISTS registration_ref,
    DROP COLUMN IF EXISTS registration,
    DROP COLUMN IF EXISTS after_status,
    DROP COLUMN IF EXISTS after_pipeline_id,
    DROP COLUMN IF EXISTS after_sha;
