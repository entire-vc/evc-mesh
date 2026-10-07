-- +goose Up
-- Explicit registrations are durable; historical comments are not registrations.
CREATE TABLE parked_waits (
    id uuid PRIMARY KEY,
    task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    wait_comment_id uuid NOT NULL,
    plan jsonb NOT NULL,
    registered_by uuid NOT NULL,
    registered_by_type actor_type NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    release_id uuid,
    release_request jsonb,
    released_by uuid,
    released_by_type actor_type,
    result jsonb NOT NULL,
    UNIQUE (task_id, wait_comment_id)
);

-- Edges must invalidate the parked snapshot just like comments and gates.
CREATE TRIGGER dependency_task_snapshot AFTER INSERT OR UPDATE OR DELETE ON task_dependencies
    FOR EACH ROW EXECUTE FUNCTION invalidate_task_snapshot_on_activity();

-- +goose Down
DROP TRIGGER dependency_task_snapshot ON task_dependencies;
DROP TABLE parked_waits;
