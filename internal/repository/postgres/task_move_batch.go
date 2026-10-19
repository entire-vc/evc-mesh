package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// moveBatchRow is one locked task of the batch.
type moveBatchRow struct {
	ID           uuid.UUID
	ProjectID    uuid.UUID
	StatusID     uuid.UUID
	TaskNumber   int
	ParentTaskID *uuid.UUID
	CheckedOut   bool
	WorkspaceID  uuid.UUID
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

func joinIDs(ids []uuid.UUID) string {
	s := uuidStrings(ids)
	sort.Strings(s)
	return strings.Join(s, ", ")
}

// mapMoveBatchPGError turns a guard refusal (23514) into a 422 the caller can
// act on; anything else is returned untouched.
func mapMoveBatchPGError(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23514" {
		return apierror.Unprocessable("move refused by completion graph guard", strings.TrimSpace(pqErr.Message+" "+pqErr.Detail))
	}
	return err
}

// MoveBatchToProject moves the tasks of in.TaskIDs into in.ProjectID in one
// transaction. Lock order matches task creation (target project number lock,
// then the workspace graph lock), after which the rows are locked FOR UPDATE.
// The guard's mesh.atomic_move flag (this transaction's id) is set only for the project UPDATE; the
// resulting graph is fully re-validated before COMMIT, and any violation rolls
// everything back.
func (r *TaskRepo) MoveBatchToProject(ctx context.Context, in domain.MoveBatchInput) (res *domain.MoveBatchResult, err error) {
	if len(in.TaskIDs) == 0 {
		return nil, apierror.BadRequest("task_ids must not be empty")
	}
	seen := make(map[uuid.UUID]bool, len(in.TaskIDs))
	for _, id := range in.TaskIDs {
		if seen[id] {
			return nil, apierror.BadRequest("task_ids contains duplicates: " + id.String())
		}
		seen[id] = true
	}
	ids := pq.Array(uuidStrings(in.TaskIDs))

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	var targetWS uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT workspace_id FROM projects WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, in.ProjectID).Scan(&targetWS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apierror.NotFound("Project")
		}
		return nil, err
	}
	if targetWS != in.WorkspaceID {
		return nil, apierror.Forbidden("target project belongs to a different workspace")
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1::text))`, in.ProjectID.String()); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT completion_graph_lock($1)`, in.WorkspaceID); err != nil {
		return nil, err
	}

	// Lock and load every task. Missing, deleted and foreign ones are refused
	// by name so a typo cannot turn into a partial move.
	rows, err := tx.QueryContext(ctx, `
		SELECT t.id, t.project_id, t.status_id, t.task_number, t.parent_task_id,
		       t.checked_out_by IS NOT NULL, p.workspace_id
		FROM tasks t JOIN projects p ON p.id=t.project_id
		WHERE t.id = ANY($1::uuid[]) AND t.deleted_at IS NULL
		ORDER BY t.id FOR UPDATE OF t`, ids)
	if err != nil {
		return nil, err
	}
	loaded := map[uuid.UUID]*moveBatchRow{}
	for rows.Next() {
		var m moveBatchRow
		if err = rows.Scan(&m.ID, &m.ProjectID, &m.StatusID, &m.TaskNumber, &m.ParentTaskID, &m.CheckedOut, &m.WorkspaceID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		loaded[m.ID] = &m
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, rerr
	}
	_ = rows.Close()

	var missing, foreign, already, checkedOut []uuid.UUID
	for _, id := range in.TaskIDs {
		m, ok := loaded[id]
		switch {
		case !ok:
			missing = append(missing, id)
		case m.WorkspaceID != in.WorkspaceID:
			foreign = append(foreign, id)
		case m.ProjectID == in.ProjectID:
			already = append(already, id)
		case m.CheckedOut:
			checkedOut = append(checkedOut, id)
		}
	}
	if len(missing) > 0 {
		return nil, apierror.NotFoundWithDetails("Task", "missing or deleted: "+joinIDs(missing))
	}
	if len(foreign) > 0 {
		return nil, apierror.Forbidden("task belongs to a different workspace: " + joinIDs(foreign))
	}
	if len(already) > 0 {
		return nil, apierror.Unprocessable("task is already in the target project", joinIDs(already))
	}
	if len(checkedOut) > 0 {
		return nil, apierror.Conflict("active checkout, nothing was moved: " + joinIDs(checkedOut))
	}
	reserved, err := scanUUIDs(ctx, tx, `SELECT task_id FROM runtime_reservations
		WHERE task_id = ANY($1::uuid[]) AND state IN ('reserved','consumed')`, ids)
	if err != nil {
		return nil, err
	}
	if len(reserved) > 0 {
		return nil, apierror.Conflict("active runtime reservation, nothing was moved: " + joinIDs(reserved))
	}

	// Status mapping: every source status must be mapped to a status of the
	// target project, otherwise the whole batch is refused.
	var unmapped []string
	var srcArr, dstArr []string
	for src, dst := range in.StatusMapping {
		srcArr = append(srcArr, src.String())
		dstArr = append(dstArr, dst.String())
	}
	dstOK, err := scanUUIDs(ctx, tx, `SELECT id FROM task_statuses WHERE project_id=$1 AND id = ANY($2::uuid[])`, in.ProjectID, pq.Array(dstArr))
	if err != nil {
		return nil, err
	}
	okSet := map[uuid.UUID]bool{}
	for _, id := range dstOK {
		okSet[id] = true
	}
	for _, id := range in.TaskIDs {
		dst, mapped := in.StatusMapping[loaded[id].StatusID]
		if !mapped {
			unmapped = append(unmapped, fmt.Sprintf("%s: no mapping for status %s", id, loaded[id].StatusID))
		} else if !okSet[dst] {
			unmapped = append(unmapped, fmt.Sprintf("%s: status %s is not a status of the target project", id, dst))
		}
	}
	if len(unmapped) > 0 {
		return nil, apierror.Unprocessable("status mapping incomplete, nothing was moved", strings.Join(unmapped, "; "))
	}

	// Children that would stay behind (any state, deleted included) keep a
	// parent pointer into another project: refuse.
	stay, err := scanPairs(ctx, tx, `
		SELECT c.id, c.parent_task_id FROM tasks c
		WHERE c.parent_task_id = ANY($1::uuid[]) AND NOT (c.id = ANY($1::uuid[]))
		UNION
		SELECT d.task_id, d.depends_on_task_id FROM task_dependencies d
		WHERE d.dependency_type='is_child_of' AND d.depends_on_task_id = ANY($1::uuid[])
		  AND NOT (d.task_id = ANY($1::uuid[]))`, ids)
	if err != nil {
		return nil, err
	}
	if len(stay) > 0 {
		return nil, apierror.Unprocessable("moved task has children that would stay in the old project; "+
			"children never move implicitly, include them in task_ids or detach them first", pairsString(stay, "child", "root"))
	}

	// Moved tasks whose parent stays behind.
	leaves, err := scanPairs(ctx, tx, `
		SELECT t.id, t.parent_task_id FROM tasks t
		WHERE t.id = ANY($1::uuid[]) AND t.parent_task_id IS NOT NULL AND NOT (t.parent_task_id = ANY($1::uuid[]))
		UNION
		SELECT d.task_id, d.depends_on_task_id FROM task_dependencies d
		WHERE d.dependency_type='is_child_of' AND d.task_id = ANY($1::uuid[])
		  AND NOT (d.depends_on_task_id = ANY($1::uuid[]))`, ids)
	if err != nil {
		return nil, err
	}
	if len(leaves) > 0 && in.LeafMode != domain.MoveLeafDetachKeepBlocks {
		return nil, apierror.Unprocessable("moved task has a parent that stays in the old project (leaf_mode=refuse)", pairsString(leaves, "task", "parent"))
	}

	before, err := moveBatchSnapshots(ctx, tx, in.TaskIDs)
	if err != nil {
		return nil, err
	}
	beforeDeps, err := moveBatchDeps(ctx, tx, ids)
	if err != nil {
		return nil, err
	}

	// Detach leaves from their old parent; the parent then waits on the leaf.
	removed := map[uuid.UUID]bool{}
	created := map[uuid.UUID]bool{}
	var detached []domain.MoveBatchDetach
	for _, lp := range leaves {
		leaf, parent := lp[0], lp[1]
		var ids2 []uuid.UUID
		if ids2, err = scanUUIDs(ctx, tx, `DELETE FROM task_dependencies
			WHERE task_id=$1 AND depends_on_task_id=$2 AND dependency_type='is_child_of' RETURNING id`, leaf, parent); err != nil {
			return nil, mapMoveBatchPGError(err)
		}
		for _, id := range ids2 {
			removed[id] = true
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET parent_task_id=NULL, updated_at=now()
			WHERE id=$1 AND parent_task_id IS NOT NULL`, leaf); err != nil {
			return nil, mapMoveBatchPGError(err)
		}
		d := domain.MoveBatchDetach{TaskID: leaf, OldParentID: parent}
		var alive bool
		if err = tx.QueryRowContext(ctx, `SELECT deleted_at IS NULL FROM tasks WHERE id=$1`, parent).Scan(&alive); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if !alive {
			d.BlocksAction = "skipped_parent_deleted"
			detached = append(detached, d)
			continue
		}
		var depID uuid.UUID
		var depType string
		err = tx.QueryRowContext(ctx, `SELECT id, dependency_type::text FROM task_dependencies
			WHERE task_id=$1 AND depends_on_task_id=$2`, parent, leaf).Scan(&depID, &depType)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			depID = uuid.New()
			if _, err = tx.ExecContext(ctx, `INSERT INTO task_dependencies(id, task_id, depends_on_task_id, dependency_type)
				VALUES($1,$2,$3,'blocks')`, depID, parent, leaf); err != nil {
				return nil, mapMoveBatchPGError(err)
			}
			created[depID] = true
			d.BlocksAction = "created"
		case err != nil:
			return nil, err
		case depType == "blocks":
			d.BlocksAction = "already_present"
		case depType == "is_child_of":
			return nil, apierror.Unprocessable("cannot replace the parent link: the parent is itself a child of the moved task",
				fmt.Sprintf("%s -> %s", parent, leaf))
		default:
			// Rewriting another relationship into blocks would silently change its meaning.
			return nil, apierror.Unprocessable("the parent already has a "+depType+" dependency on the leaf; it is not rewritten into blocks",
				fmt.Sprintf("%s -> %s", parent, leaf))
		}
		d.BlocksDepID = &depID
		detached = append(detached, d)
	}

	// The project change itself, the only statement the flag is on for.
	if _, err = tx.ExecContext(ctx, `SELECT set_config('mesh.atomic_move', txid_current()::text, true)`); err != nil {
		return nil, err
	}
	// The guard is a row trigger that wants a task's parent in the same project at
	// the moment of the update, so a single multi-row UPDATE depends on row order.
	// Move parents before children, one statement per task.
	ordered, err := scanUUIDs(ctx, tx, `
		WITH RECURSIVE depth(id, d) AS (
			SELECT id, 0 FROM tasks WHERE id = ANY($1::uuid[])
			  AND (parent_task_id IS NULL OR NOT parent_task_id = ANY($1::uuid[]))
			UNION ALL
			SELECT c.id, depth.d+1 FROM tasks c JOIN depth ON c.parent_task_id=depth.id
			WHERE c.id = ANY($1::uuid[])
		)
		SELECT t.id FROM tasks t JOIN depth ON depth.id=t.id
		ORDER BY depth.d, t.task_number, t.id`, ids)
	if err != nil {
		return nil, err
	}
	if len(ordered) != len(in.TaskIDs) {
		return nil, apierror.Unprocessable("cannot order the batch parent-before-child", "")
	}
	var moved int64
	for _, id := range ordered {
		res, uerr := tx.ExecContext(ctx, `
			UPDATE tasks t
			SET project_id=$1, status_id=m.dst, updated_at=now(),
			    task_number=(SELECT COALESCE(MAX(task_number),0) FROM tasks WHERE project_id=$1)+1
			FROM unnest($3::uuid[], $4::uuid[]) AS m(src,dst)
			WHERE t.id=$2 AND m.src=t.status_id`, in.ProjectID, id, pq.Array(srcArr), pq.Array(dstArr))
		if uerr != nil {
			return nil, mapMoveBatchPGError(uerr)
		}
		n, _ := res.RowsAffected()
		moved += n
	}
	if _, err = tx.ExecContext(ctx, `SELECT set_config('mesh.atomic_move', '', true)`); err != nil {
		return nil, err
	}
	if int(moved) != len(in.TaskIDs) {
		return nil, apierror.Conflict(fmt.Sprintf("moved %d of %d tasks, nothing was moved", moved, len(in.TaskIDs)))
	}

	// Full post-validation before COMMIT; any violation rolls everything back.
	if verr := moveBatchValidate(ctx, tx, in, ids, beforeDeps, removed, created); verr != nil {
		return nil, verr
	}

	after, err := moveBatchSnapshots(ctx, tx, in.TaskIDs)
	if err != nil {
		return nil, err
	}
	sources := map[uuid.UUID]uuid.UUID{}
	distinct := map[uuid.UUID]bool{}
	for _, id := range in.TaskIDs {
		sources[id] = loaded[id].ProjectID
		distinct[loaded[id].ProjectID] = true
	}
	for p := range distinct {
		if _, err = tx.ExecContext(ctx, `SELECT bump_task_list_revision($1)`, p); err != nil {
			return nil, err
		}
	}
	res = &domain.MoveBatchResult{DryRun: in.DryRun, Before: before, After: after, Detached: detached, SourceProjects: sources}
	if in.DryRun {
		return res, nil // deferred Rollback discards everything
	}
	if cerr := tx.Commit(); cerr != nil {
		return nil, cerr
	}
	return res, nil
}

func moveBatchValidate(ctx context.Context, tx *sql.Tx, in domain.MoveBatchInput, ids any,
	beforeDeps []domain.MoveBatchDependency, removed, created map[uuid.UUID]bool) error {
	var n int
	// Every moved task is in the target project with unique numbers there.
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE id = ANY($1::uuid[]) AND project_id=$2`, ids, in.ProjectID).Scan(&n); err != nil {
		return err
	}
	if n != len(in.TaskIDs) {
		return apierror.Conflict("post-validation: not every task reached the target project")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT task_number FROM tasks WHERE project_id=$1 GROUP BY task_number HAVING count(*)>1) d`, in.ProjectID).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return apierror.Conflict("post-validation: duplicate task_number in target project")
	}
	// No parent pointer crosses a project boundary (deleted rows included).
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks c JOIN tasks p ON p.id=c.parent_task_id
		WHERE (c.id = ANY($1::uuid[]) OR p.id = ANY($1::uuid[])) AND c.project_id<>p.project_id`, ids).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return apierror.Unprocessable("post-validation: parent link would cross projects", fmt.Sprintf("%d link(s)", n))
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM task_dependencies d
		JOIN tasks a ON a.id=d.task_id JOIN tasks b ON b.id=d.depends_on_task_id
		WHERE d.dependency_type='is_child_of' AND (a.id = ANY($1::uuid[]) OR b.id = ANY($1::uuid[]))
		  AND a.project_id<>b.project_id`, ids).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return apierror.Unprocessable("post-validation: is_child_of dependency would cross projects", fmt.Sprintf("%d row(s)", n))
	}
	// Every dependency endpoint stays inside the workspace.
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM task_dependencies d
		JOIN tasks a ON a.id=d.task_id JOIN projects ap ON ap.id=a.project_id
		JOIN tasks b ON b.id=d.depends_on_task_id JOIN projects bp ON bp.id=b.project_id
		WHERE (a.id = ANY($1::uuid[]) OR b.id = ANY($1::uuid[]))
		  AND (ap.workspace_id<>$2 OR bp.workspace_id<>$2)`, ids, in.WorkspaceID).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return apierror.Unprocessable("post-validation: dependency leaves the workspace", fmt.Sprintf("%d row(s)", n))
	}
	// No completion cycle through any edge that touches the moved set.
	if _, err := tx.ExecContext(ctx, `SELECT completion_graph_assert_edge($2, d.task_id, d.depends_on_task_id, NULL, d.id)
		FROM task_dependencies d
		WHERE d.dependency_type='blocks' AND (d.task_id = ANY($1::uuid[]) OR d.depends_on_task_id = ANY($1::uuid[]))`, ids, in.WorkspaceID); err != nil {
		return mapMoveBatchPGError(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT completion_graph_assert_edge($2, c.parent_task_id, c.id, c.id, NULL)
		FROM tasks c
		WHERE c.deleted_at IS NULL AND c.parent_task_id IS NOT NULL
		  AND (c.id = ANY($1::uuid[]) OR c.parent_task_id = ANY($1::uuid[]))`, ids, in.WorkspaceID); err != nil {
		return mapMoveBatchPGError(err)
	}
	// Dependencies: IDs, endpoints and types preserved except the declared
	// detach conversions; nothing appears that was not declared.
	afterDeps, err := moveBatchDeps(ctx, tx, ids)
	if err != nil {
		return err
	}
	after := map[uuid.UUID]domain.MoveBatchDependency{}
	for _, d := range afterDeps {
		after[d.ID] = d
	}
	seenBefore := map[uuid.UUID]bool{}
	for _, b := range beforeDeps {
		seenBefore[b.ID] = true
		a, ok := after[b.ID]
		switch {
		case removed[b.ID]:
			if ok {
				return apierror.Conflict("post-validation: dependency expected to be removed is still present: " + b.ID.String())
			}
		case !ok:
			return apierror.Conflict("post-validation: dependency lost: " + b.ID.String())
		case a.TaskID != b.TaskID || a.DependsOnTaskID != b.DependsOnTaskID:
			return apierror.Conflict("post-validation: dependency endpoints changed: " + b.ID.String())
		case a.DependencyType != b.DependencyType:
			return apierror.Conflict("post-validation: dependency type changed: " + b.ID.String())
		}
	}
	for id := range after {
		if !seenBefore[id] && !created[id] {
			return apierror.Conflict("post-validation: unexpected dependency appeared: " + id.String())
		}
	}
	return nil
}

func scanUUIDs(ctx context.Context, tx *sql.Tx, q string, args ...any) ([]uuid.UUID, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func scanPairs(ctx context.Context, tx *sql.Tx, q string, args ...any) ([][2]uuid.UUID, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out [][2]uuid.UUID
	for rows.Next() {
		var p [2]uuid.UUID
		if err := rows.Scan(&p[0], &p[1]); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i][0].String()+out[i][1].String() < out[j][0].String()+out[j][1].String()
	})
	return out, rows.Err()
}

func pairsString(pairs [][2]uuid.UUID, a, b string) string {
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = fmt.Sprintf("%s %s of %s %s", a, p[0], b, p[1])
	}
	return strings.Join(parts, "; ")
}

func moveBatchDeps(ctx context.Context, tx *sql.Tx, ids any) ([]domain.MoveBatchDependency, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, task_id, depends_on_task_id, dependency_type::text
		FROM task_dependencies WHERE task_id = ANY($1::uuid[]) OR depends_on_task_id = ANY($1::uuid[]) ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.MoveBatchDependency
	for rows.Next() {
		var d domain.MoveBatchDependency
		if err := rows.Scan(&d.ID, &d.TaskID, &d.DependsOnTaskID, &d.DependencyType); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func moveBatchSnapshots(ctx context.Context, tx *sql.Tx, order []uuid.UUID) ([]domain.MoveBatchSnapshot, error) {
	arr := pq.Array(uuidStrings(order))
	rows, err := tx.QueryContext(ctx, `SELECT id, project_id, status_id, task_number, parent_task_id FROM tasks WHERE id = ANY($1::uuid[])`, arr)
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]*domain.MoveBatchSnapshot{}
	for rows.Next() {
		var s domain.MoveBatchSnapshot
		if serr := rows.Scan(&s.TaskID, &s.ProjectID, &s.StatusID, &s.TaskNumber, &s.ParentTaskID); serr != nil {
			_ = rows.Close()
			return nil, serr
		}
		s.Dependencies = []domain.MoveBatchDependency{}
		byID[s.TaskID] = &s
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, rerr
	}
	_ = rows.Close()
	deps, err := moveBatchDeps(ctx, tx, arr)
	if err != nil {
		return nil, err
	}
	for _, d := range deps {
		if s, ok := byID[d.TaskID]; ok {
			s.Dependencies = append(s.Dependencies, d)
		}
		if s, ok := byID[d.DependsOnTaskID]; ok && d.DependsOnTaskID != d.TaskID {
			s.Dependencies = append(s.Dependencies, d)
		}
	}
	out := make([]domain.MoveBatchSnapshot, 0, len(order))
	for _, id := range order {
		if s, ok := byID[id]; ok {
			out = append(out, *s)
		}
	}
	return out, nil
}
