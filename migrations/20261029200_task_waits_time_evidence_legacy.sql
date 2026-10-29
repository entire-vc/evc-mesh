-- +goose Up
-- 20261029100 added task_waits_time_evidence as NOT VALID so time waits
-- declared before it kept their shape. Postgres still checks a NOT VALID
-- constraint on every later UPDATE of a row, so such a wait could no longer
-- be resolved, superseded or cancelled. And evidence_status = 'unverified'
-- is NULL for a NULL status, and a NULL CHECK passes, so a time wait with
-- the evidence but no status got through.
--
-- Time waits without a status are marked 'legacy' (the old constraint goes
-- first: it would refuse this very UPDATE). The new check holds for every
-- row and accepts that marker; a trigger keeps any writer from creating a
-- legacy wait or turning a row into one.
ALTER TABLE task_waits DROP CONSTRAINT IF EXISTS task_waits_time_evidence;

UPDATE task_waits SET evidence_status = 'legacy'
WHERE kind = 'time' AND evidence_status IS NULL;

-- IS NOT DISTINCT FROM, not =: a NULL status must fail the check.
ALTER TABLE task_waits
    ADD CONSTRAINT task_waits_time_evidence CHECK (
        kind <> 'time' OR evidence_status IS NOT DISTINCT FROM 'legacy'
        OR (event_ref IS NOT NULL AND evidence_source IS NOT NULL
            AND evidence_why IS NOT NULL AND until_at_offset IS NOT NULL
            AND evidence_status IS NOT DISTINCT FROM 'unverified'));

-- +goose StatementBegin
CREATE FUNCTION task_waits_no_new_legacy() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.evidence_status = 'legacy'
       AND (TG_OP = 'INSERT' OR OLD.evidence_status IS DISTINCT FROM 'legacy') THEN
        RAISE EXCEPTION 'a time wait needs evidence; legacy is reserved for waits declared before it was required'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'task_waits_time_evidence';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER task_waits_no_new_legacy
    BEFORE INSERT OR UPDATE OF evidence_status ON task_waits
    FOR EACH ROW EXECUTE FUNCTION task_waits_no_new_legacy();

-- The same NULL hole in the start_after receipt check. Receipts are
-- append-only, so NOT VALID here only skips a scan of rows nobody can change.
ALTER TABLE start_after_receipts DROP CONSTRAINT IF EXISTS start_after_receipts_agent_evidence;
ALTER TABLE start_after_receipts
    ADD CONSTRAINT start_after_receipts_agent_evidence CHECK (
        op <> 'set' OR origin <> 'agent_evidence' OR (
            event_ref IS NOT NULL AND evidence_source IS NOT NULL AND evidence_why IS NOT NULL
            AND source_offset IS NOT NULL AND evidence_status IS NOT DISTINCT FROM 'unverified')) NOT VALID;

-- +goose Down
ALTER TABLE start_after_receipts DROP CONSTRAINT IF EXISTS start_after_receipts_agent_evidence;
ALTER TABLE start_after_receipts
    ADD CONSTRAINT start_after_receipts_agent_evidence CHECK (
        op <> 'set' OR origin <> 'agent_evidence' OR (
            event_ref IS NOT NULL AND evidence_source IS NOT NULL AND evidence_why IS NOT NULL
            AND source_offset IS NOT NULL AND evidence_status = 'unverified'));

DROP TRIGGER IF EXISTS task_waits_no_new_legacy ON task_waits;
DROP FUNCTION IF EXISTS task_waits_no_new_legacy();
ALTER TABLE task_waits DROP CONSTRAINT IF EXISTS task_waits_time_evidence;
UPDATE task_waits SET evidence_status = NULL WHERE evidence_status = 'legacy';
ALTER TABLE task_waits
    ADD CONSTRAINT task_waits_time_evidence CHECK (
        kind <> 'time' OR (event_ref IS NOT NULL AND evidence_source IS NOT NULL
                           AND evidence_why IS NOT NULL AND until_at_offset IS NOT NULL
                           AND evidence_status = 'unverified')) NOT VALID;
