package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TaskDependencyRepo implements repository.TaskDependencyRepository with PostgreSQL.
type TaskDependencyRepo struct {
	db *sqlx.DB
}

// NewTaskDependencyRepo creates a new TaskDependencyRepo.
func NewTaskDependencyRepo(db *sqlx.DB) *TaskDependencyRepo {
	return &TaskDependencyRepo{db: db}
}

// AtomicHierarchy reports that the completion graph triggers persist an
// is_child_of edge and its parent side effect in the same transaction.
func (r *TaskDependencyRepo) AtomicHierarchy() bool { return true }

// taskDependencySelectCols is every column domain.TaskDependency scans,
// listed explicitly — see agentSelectCols in agent_repo.go for why
// `SELECT *` is unsafe: sqlx refuses to scan a column with no matching
// struct field, so an additive migration to this table breaks every read
// until redeployed.
//
// condition_satisfied is the full verdict (evidence OR the blocker's own state
// for the edge's condition), so every read joins the blocker; the join is LEFT
// so an edge whose blocker row is gone is still returned.
var taskDependencySelectCols = `d.id, d.task_id, d.depends_on_task_id, d.dependency_type, d.created_at, d.condition, d.evidence_url, d.condition_met_at, ` +
	domain.DependencyReleasedSQL("d", "bt", "bts.category") + ` AS condition_satisfied`

// taskDependencyJoins pairs with taskDependencySelectCols: the edge is d, its
// blocker bt and the blocker's status bts.
const taskDependencyJoins = `LEFT JOIN tasks bt ON bt.id = d.depends_on_task_id
	LEFT JOIN task_statuses bts ON bts.id = bt.status_id`

const taskDependencyFrom = `task_dependencies d ` + taskDependencyJoins

func (r *TaskDependencyRepo) Create(ctx context.Context, dep *domain.TaskDependency) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Separate lock statement: all following reads see the preceding writer's
	// commit. Calling the function also fails closed if the migration is absent.
	if _, err = tx.ExecContext(ctx, `SELECT completion_graph_lock(p.workspace_id) FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=$1`, dep.TaskID); err != nil {
		return err
	}
	const q = `
		INSERT INTO task_dependencies (id, task_id, depends_on_task_id, dependency_type, created_at, condition, evidence_url, condition_met_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	if dep.Condition == "" {
		dep.Condition = domain.DependencyConditionTaskDone
	}
	_, err = tx.ExecContext(ctx, q,
		dep.ID, dep.TaskID, dep.DependsOnTaskID, dep.DependencyType, dep.CreatedAt,
		dep.Condition, dep.EvidenceURL, dep.ConditionMetAt,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// GetByID returns the dependency, or (nil, nil) when no row matches.
func (r *TaskDependencyRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.TaskDependency, error) {
	q := `SELECT ` + taskDependencySelectCols + ` FROM ` + taskDependencyFrom + ` WHERE d.id = $1`
	var dep domain.TaskDependency
	if err := r.db.GetContext(ctx, &dep, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &dep, nil
}

func (r *TaskDependencyRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT completion_graph_lock(p.workspace_id) FROM task_dependencies d JOIN tasks t ON t.id=d.task_id JOIN projects p ON p.id=t.project_id WHERE d.id=$1`, id); err != nil {
		return err
	}
	const q = `DELETE FROM task_dependencies WHERE id = $1`
	res, err := tx.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("TaskDependency")
	}
	return tx.Commit()
}

// UpdateCondition implements repository.TaskDependencyRepository.
func (r *TaskDependencyRepo) UpdateCondition(ctx context.Context, id uuid.UUID, condition domain.DependencyCondition, evidenceURL *string, metAt *time.Time) (*domain.TaskDependency, error) {
	q := `WITH d AS (UPDATE task_dependencies SET condition = $2, evidence_url = $3, condition_met_at = $4 WHERE id = $1 RETURNING *)
		SELECT ` + taskDependencySelectCols + ` FROM d ` + taskDependencyJoins
	var dep domain.TaskDependency
	if err := r.db.GetContext(ctx, &dep, q, id, condition, evidenceURL, metAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &dep, nil
}

// ListByTask returns all dependencies where the given task depends on another.
func (r *TaskDependencyRepo) ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error) {
	q := `SELECT ` + taskDependencySelectCols + ` FROM ` + taskDependencyFrom + ` WHERE d.task_id = $1 ORDER BY d.created_at ASC`
	var deps []domain.TaskDependency
	if err := r.db.SelectContext(ctx, &deps, q, taskID); err != nil {
		return nil, err
	}
	if deps == nil {
		deps = []domain.TaskDependency{}
	}
	return deps, nil
}

// ListDependents returns all dependencies where another task depends on the given task.
func (r *TaskDependencyRepo) ListDependents(ctx context.Context, taskID uuid.UUID) ([]domain.TaskDependency, error) {
	q := `SELECT ` + taskDependencySelectCols + ` FROM ` + taskDependencyFrom + ` WHERE d.depends_on_task_id = $1 ORDER BY d.created_at ASC`
	var deps []domain.TaskDependency
	if err := r.db.SelectContext(ctx, &deps, q, taskID); err != nil {
		return nil, err
	}
	if deps == nil {
		deps = []domain.TaskDependency{}
	}
	return deps, nil
}

func (r *TaskDependencyRepo) Exists(ctx context.Context, taskID, dependsOnTaskID uuid.UUID) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM task_dependencies WHERE task_id = $1 AND depends_on_task_id = $2)`
	var exists bool
	if err := r.db.GetContext(ctx, &exists, q, taskID, dependsOnTaskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return exists, nil
}

// HasOpenBlockedDependents reports whether some open task is held up by
// blockerID: a `blocks` edge where blockerID is depends_on_task_id and the
// dependent (task_id) is live and not in a done/cancelled status. The lease
// reaper reads it to keep such a card out of backlog, where nothing feeds it
// and the dependent would wait on it indefinitely.
//
// "Open" deliberately includes backlog and triage dependents: a blocked card
// usually waits in backlog, and tryUnblockTask promotes exactly those once the
// blocker is done, so they are the ones a parked blocker strands.
//
// Both endpoints must sit in the same workspace and in live projects, the
// scoping the dependency graph applies to legacy cross-tenant rows
// (wsEdgesCTE); the blocker side (b, bp) is joined only for that check.
func (r *TaskDependencyRepo) HasOpenBlockedDependents(ctx context.Context, blockerID uuid.UUID) (bool, error) {
	q := `
		SELECT EXISTS (
		    SELECT 1
		    FROM task_dependencies d
		    JOIN tasks dep          ON dep.id = d.task_id
		    JOIN task_statuses s    ON s.id = dep.status_id
		    JOIN projects dp        ON dp.id = dep.project_id
		    JOIN tasks b            ON b.id = d.depends_on_task_id
		    JOIN task_statuses bs   ON bs.id = b.status_id
		    JOIN projects bp        ON bp.id = b.project_id
		    WHERE d.depends_on_task_id = $1
		      AND d.dependency_type = 'blocks'
		      AND NOT ` + domain.DependencyReleasedSQL("d", "b", "bs.category") + `
		      AND dep.deleted_at IS NULL
		      AND s.category NOT IN ('done', 'cancelled')
		      AND dp.workspace_id = bp.workspace_id
		      AND dp.deleted_at IS NULL
		      AND bp.deleted_at IS NULL
		)`
	var open bool
	if err := r.db.GetContext(ctx, &open, q, blockerID); err != nil {
		return false, err
	}
	return open, nil
}

// DeployedTasks implements repository.TaskDependencyRepository. It is the batch
// form of the vcs_links EXISTS inside domain.DependencyReleasedSQL, for the Go
// readers that decide per edge with TaskDependency.Released.
func (r *TaskDependencyRepo) DeployedTasks(ctx context.Context, taskIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := make(map[uuid.UUID]bool, len(taskIDs))
	if len(taskIDs) == 0 {
		return out, nil
	}
	const q = `SELECT DISTINCT dv.task_id FROM vcs_links dv
		WHERE dv.task_id = ANY($1::uuid[])
		  AND dv.link_type = 'pr'
		  AND COALESCE(dv.metadata->'deployed'->>'sha', '') <> ''
		  AND dv.metadata->'deployed'->>'at' IS NOT NULL`
	var ids []uuid.UUID
	if err := r.db.SelectContext(ctx, &ids, q, pq.Array(taskIDs)); err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
