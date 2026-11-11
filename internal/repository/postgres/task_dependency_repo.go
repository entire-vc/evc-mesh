package postgres

import (
	"context"
	"database/sql"
	"errors"

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
var taskDependencySelectCols = `d.id, d.task_id, d.depends_on_task_id, d.dependency_type, d.created_at, d.condition, d.evidence_url, d.condition_met_at, d.criterion, d.generation, d.evidence_status, d.evidence_reason, d.evidence_head_sha, d.evidence_environment, d.evidence_link_id, d.evidence_actor_id, ` +
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
		INSERT INTO task_dependencies (id, task_id, depends_on_task_id, dependency_type, created_at, condition, evidence_url, condition_met_at, criterion, evidence_status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	if dep.Condition == "" {
		dep.Condition = domain.DependencyConditionTaskDone
	}
	if dep.EvidenceStatus == "" {
		dep.EvidenceStatus = domain.EvidenceNone
		if dep.EvidenceURL != nil && dep.Condition != domain.DependencyConditionTaskDone {
			dep.EvidenceStatus = domain.EvidenceUnverified
		}
	}
	_, err = tx.ExecContext(ctx, q,
		dep.ID, dep.TaskID, dep.DependsOnTaskID, dep.DependencyType, dep.CreatedAt,
		dep.Condition, dep.EvidenceURL, dep.ConditionMetAt, dep.Criterion, dep.EvidenceStatus,
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

// DeleteIfGeneration is the compare-and-delete: the source task and generation
// are predicates of the DELETE itself, under the same workspace graph lock that
// condition updates take, so a concurrent condition change cannot slip between
// the check and the delete.
func (r *TaskDependencyRepo) DeleteIfGeneration(ctx context.Context, taskID, id uuid.UUID, gen int64) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT completion_graph_lock(p.workspace_id) FROM task_dependencies d JOIN tasks t ON t.id=d.task_id JOIN projects p ON p.id=t.project_id WHERE d.id=$1 AND d.task_id=$2`, id, taskID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM task_dependencies WHERE id = $1 AND task_id = $2 AND generation = $3`, id, taskID, gen)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var cur int64
		qerr := tx.GetContext(ctx, &cur, `SELECT generation FROM task_dependencies WHERE id = $1 AND task_id = $2`, id, taskID)
		if errors.Is(qerr, sql.ErrNoRows) {
			return apierror.NotFound("TaskDependency")
		}
		if qerr != nil {
			return qerr
		}
		return domain.ErrStaleDependencyGeneration
	}
	return tx.Commit()
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

// UpdateCondition implements repository.TaskDependencyRepository. A change of
// condition or criterion bumps the generation and drops the old evidence;
// evidence is stored only while the generation is still expectedGen (CAS).
func (r *TaskDependencyRepo) UpdateCondition(ctx context.Context, id uuid.UUID, upd domain.DependencyConditionUpdate) (*domain.TaskDependency, error) {
	if upd.ExpectedGeneration == nil {
		return nil, errors.New("task dependency update requires expected generation")
	}
	var ev domain.DependencyEvidence
	if upd.Evidence != nil {
		ev = *upd.Evidence
	}
	status := domain.EvidenceNone
	if upd.Evidence != nil {
		status = ev.Status
	}
	nz := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	q := `WITH cur AS (SELECT id, (condition <> $2 OR criterion <> $3) AS changed FROM task_dependencies WHERE id = $1 FOR UPDATE),
		d AS (UPDATE task_dependencies t SET condition = $2, criterion = $3,
			generation = t.generation + CASE WHEN cur.changed THEN 1 ELSE 0 END,
			evidence_url = CASE WHEN $11 THEN $4 WHEN cur.changed THEN NULL ELSE t.evidence_url END,
			condition_met_at = CASE WHEN $11 THEN $5 WHEN cur.changed THEN NULL ELSE t.condition_met_at END,
			evidence_status = CASE WHEN $11 THEN $6 WHEN cur.changed THEN 'none' ELSE t.evidence_status END,
			evidence_reason = CASE WHEN $11 THEN $7 WHEN cur.changed THEN NULL ELSE t.evidence_reason END,
			evidence_head_sha = CASE WHEN $11 THEN $8 WHEN cur.changed THEN NULL ELSE t.evidence_head_sha END,
			evidence_environment = CASE WHEN $11 THEN $9 WHEN cur.changed THEN NULL ELSE t.evidence_environment END,
			evidence_link_id = CASE WHEN $11 THEN $10 WHEN cur.changed THEN NULL ELSE t.evidence_link_id END,
			evidence_actor_id = CASE WHEN $11 THEN $12 WHEN cur.changed THEN NULL ELSE t.evidence_actor_id END
			FROM cur WHERE t.id = cur.id AND t.generation = $13 RETURNING t.*)
		SELECT ` + taskDependencySelectCols + ` FROM d ` + taskDependencyJoins
	var dep domain.TaskDependency
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// The evidence was verified against a link read earlier. Lock that link row
	// (a concurrent receipt change waits for this commit, and its drift trigger then
	// sees the bound edge) and re-check the receipt below, so a link that moved in
	// between cannot leave stale evidence trusted.
	if upd.Evidence != nil && ev.LinkID != nil {
		if _, lerr := tx.ExecContext(ctx, `SELECT 1 FROM vcs_links WHERE id = $1 FOR SHARE`, *ev.LinkID); lerr != nil {
			return nil, lerr
		}
	}
	err = tx.GetContext(ctx, &dep, q, id, upd.Condition, upd.Criterion,
		nz(ev.URL), ev.MetAt, status, nz(ev.Reason), nz(ev.HeadSHA), nz(ev.Environment), ev.LinkID,
		upd.Evidence != nil, ev.ActorID, upd.ExpectedGeneration)
	if err == nil && upd.Evidence != nil && ev.LinkID != nil && status == domain.EvidenceTrusted {
		var revoked int64
		res, rerr := tx.ExecContext(ctx, `UPDATE task_dependencies d
			SET evidence_status = 'revoked',
			    evidence_reason = 'bound pr link moved to another HEAD/deploy or its SHIP/acceptance changed before the evidence was stored'
			FROM vcs_links l
			WHERE d.id = $1 AND l.id = d.evidence_link_id AND d.evidence_status = 'trusted'
			  AND d.evidence_head_sha IS NOT NULL
			  AND (COALESCE(l.metadata->>'head_sha', '') <> d.evidence_head_sha
			    OR (d.condition = 'shipped' AND COALESCE(l.metadata->>'ship_sha', '') <> d.evidence_head_sha)
			    OR (d.condition = 'deployed' AND (COALESCE(l.metadata->'deployed'->>'sha', '') <> d.evidence_head_sha
			         OR COALESCE(l.metadata->'deployed'->>'environment', '') <> COALESCE(d.evidence_environment, '')))
			    OR (d.condition = 'accepted' AND (COALESCE(l.metadata->'acceptance'->>'sha', '') <> d.evidence_head_sha
			         OR COALESCE(l.metadata->'acceptance'->>'result', '') <> 'passed')))`, id)
		if rerr != nil {
			return nil, rerr
		}
		revoked, _ = res.RowsAffected()
		if revoked > 0 {
			if gerr := tx.GetContext(ctx, &dep, `SELECT `+taskDependencySelectCols+` FROM `+taskDependencyFrom+` WHERE d.id = $1`, id); gerr != nil {
				return nil, gerr
			}
		}
	}
	if err == nil {
		if cerr := tx.Commit(); cerr != nil {
			return nil, cerr
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if qerr := r.db.GetContext(ctx, &exists, `SELECT EXISTS(SELECT 1 FROM task_dependencies WHERE id = $1)`, id); qerr == nil && exists {
			return nil, domain.ErrStaleDependencyGeneration
		}
		return nil, nil
	}
	if err != nil {
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

// ListBlockingByTasks returns, per task, its `blocks` edges that still hold it
// (condition not satisfied), oldest first, in one query. Tasks without such an
// edge are absent from the map. The board reads it for a whole page.
func (r *TaskDependencyRepo) ListBlockingByTasks(ctx context.Context, taskIDs []uuid.UUID) (map[uuid.UUID][]domain.TaskDependency, error) {
	out := make(map[uuid.UUID][]domain.TaskDependency)
	if len(taskIDs) == 0 {
		return out, nil
	}
	q := `SELECT ` + taskDependencySelectCols + ` FROM ` + taskDependencyFrom +
		` WHERE d.task_id = ANY($1) AND d.dependency_type = 'blocks' ORDER BY d.created_at ASC`
	var deps []domain.TaskDependency
	if err := r.db.SelectContext(ctx, &deps, q, pq.Array(taskIDs)); err != nil {
		return nil, err
	}
	for _, d := range deps {
		if !d.ConditionSatisfied {
			out[d.TaskID] = append(out[d.TaskID], d)
		}
	}
	return out, nil
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
	q := `SELECT DISTINCT dv.task_id FROM vcs_links dv
		WHERE dv.task_id = ANY($1::uuid[])
		  AND dv.link_type = 'pr'
		  AND COALESCE(dv.metadata->'deployed'->>'sha', '') <> ''
		  AND dv.metadata->'deployed'->>'at' IS NOT NULL
		  AND dv.metadata->'deployed'->>'environment' = '` + domain.VCSEnvProd + `'`
	var ids []uuid.UUID
	if err := r.db.SelectContext(ctx, &ids, q, pq.Array(taskIDs)); err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
