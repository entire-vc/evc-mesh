package postgres

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// runtimeWorkspaceReader: a human member/owner of the workspace, or an agent
// key presented for exactly this workspace. OAuth connectors are refused like
// on every other runtime route.
func runtimeWorkspaceReader(ctx context.Context, q sqlx.QueryerContext, ws uuid.UUID, actor RuntimeActor) error {
	if actor.Connector {
		return apierror.Forbidden("runtime capacity is not available to OAuth connectors")
	}
	var allowed bool
	var err error
	switch {
	case actor.AgentID != uuid.Nil:
		if actor.AuthWorkspaceID != ws {
			return apierror.Forbidden("runtime capacity requires an agent key for this workspace")
		}
		err = sqlx.GetContext(ctx, q, &allowed, `SELECT EXISTS (SELECT 1 FROM workspaces WHERE id=$1 AND deleted_at IS NULL)`, ws)
	case actor.UserID != uuid.Nil:
		err = sqlx.GetContext(ctx, q, &allowed, `SELECT EXISTS (
	SELECT 1 FROM workspaces w WHERE w.id=$1 AND w.deleted_at IS NULL
	AND (w.owner_id=$2 OR EXISTS (SELECT 1 FROM workspace_members m WHERE m.workspace_id=w.id AND m.user_id=$2)))`, ws, actor.UserID)
	default:
		return apierror.Forbidden("runtime capacity requires an authenticated workspace member")
	}
	if err != nil {
		return err
	}
	if !allowed {
		return apierror.NotFound("Workspace")
	}
	return nil
}

// runtimeActiveReservation matches a reservation that still holds its task:
// consumed (running or reconcile) or reserved and unexpired.
const runtimeActiveReservation = `(r.state='consumed' OR (r.state='reserved' AND r.expires_at>now()))`

// Capacity projects configured/effective/reserved/running/ready per agent
// identity of the workspace (home agents and agents with an active grant),
// derived only from durable state in one consistent snapshot. Display status
// and human_gate never free a slot: only the absence of a live reservation
// and of a held checkout does. agent narrows the list to one identity.
func (r *RuntimeRepo) Capacity(ctx context.Context, ws uuid.UUID, actor RuntimeActor, agent *uuid.UUID) (*domain.RuntimeCapacity, error) {
	tx, err := r.db.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	err = runtimeWorkspaceReader(ctx, tx, ws, actor)
	if err != nil {
		return nil, err
	}
	out := &domain.RuntimeCapacity{WorkspaceID: ws, Source: domain.RuntimeCapacitySourceStore, Agents: []domain.RuntimeAgentCapacity{}}
	err = tx.GetContext(ctx, &out.ObservedAt, `SELECT now()`)
	if err != nil {
		return nil, err
	}
	var agents []struct {
		ID         uuid.UUID `db:"id"`
		Name       string    `db:"name"`
		Configured int       `db:"max_concurrent_tasks"`
	}
	err = tx.SelectContext(ctx, &agents, `SELECT a.id,a.name,a.max_concurrent_tasks FROM agents a
 WHERE a.deleted_at IS NULL AND ($2::uuid IS NULL OR a.id=$2)
 AND (a.workspace_id=$1 OR EXISTS (SELECT 1 FROM agent_workspace_grants g WHERE g.agent_id=a.id AND g.workspace_id=$1 AND g.revoked_at IS NULL))
 ORDER BY a.name,a.id`, ws, agent)
	if err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		if agent != nil {
			return nil, apierror.NotFound("Agent")
		}
		return out, nil
	}
	ids := make([]uuid.UUID, len(agents))
	index := make(map[uuid.UUID]int, len(agents))
	for i, a := range agents {
		ids[i], index[a.ID] = a.ID, i
		out.Agents = append(out.Agents, domain.RuntimeAgentCapacity{AgentID: a.ID, Name: a.Name, Configured: a.Configured})
	}
	idArray := pq.Array(ids)

	// Reservations are identity-global, across every workspace and controller.
	var reservations []struct {
		Agent     uuid.UUID `db:"agent_id"`
		Reserved  int       `db:"reserved"`
		Running   int       `db:"running"`
		Reconcile int       `db:"reconcile"`
	}
	err = tx.SelectContext(ctx, &reservations, `SELECT r.agent_id,
 count(*) FILTER (WHERE r.state='reserved' AND r.expires_at>now()) AS reserved,
 count(*) FILTER (WHERE r.state='consumed' AND r.expires_at>now()) AS running,
 count(*) FILTER (WHERE r.state='consumed' AND r.expires_at<=now()) AS reconcile
 FROM runtime_reservations r WHERE r.agent_id=ANY($1) AND r.state IN ('reserved','consumed') GROUP BY r.agent_id`, idArray)
	if err != nil {
		return nil, err
	}
	for _, row := range reservations {
		c := &out.Agents[index[row.Agent]]
		c.Reserved, c.Running, c.Reconcile = row.Reserved, row.Running, row.Reconcile
	}

	// A held checkout without an active reservation is a writer, whatever the
	// task's status or gate; one held past its lease is unknown, still occupied.
	var writers []struct {
		Agent uuid.UUID `db:"agent_id"`
		Live  int       `db:"live"`
		Stale int       `db:"stale"`
	}
	err = tx.SelectContext(ctx, &writers, `SELECT t.checked_out_by AS agent_id,
 count(*) FILTER (WHERE t.checkout_expires IS NULL OR t.checkout_expires>now()) AS live,
 count(*) FILTER (WHERE t.checkout_expires<=now()) AS stale
 FROM tasks t WHERE t.checked_out_by=ANY($1) AND t.deleted_at IS NULL
 AND NOT EXISTS (SELECT 1 FROM runtime_reservations r WHERE r.task_id=t.id AND r.agent_id=t.checked_out_by AND `+runtimeActiveReservation+`)
 GROUP BY t.checked_out_by`, idArray)
	if err != nil {
		return nil, err
	}
	for _, row := range writers {
		c := &out.Agents[index[row.Agent]]
		c.Writers, c.StaleWriters = row.Live, row.Stale
	}

	// Idle assigned work in this workspace: ready unless a durable blocker holds
	// it (human gate, triage stage, registered parked wait, open blocking
	// dependency, future start_after). Backlog and review are not schedulable.
	var tasks []struct {
		Agent        uuid.UUID `db:"agent_id"`
		Ready        int       `db:"ready"`
		Waiting      int       `db:"waiting"`
		HumanGate    int       `db:"human_gate"`
		Triage       int       `db:"triage"`
		ParkedWait   int       `db:"parked_wait"`
		Dependencies int       `db:"dependencies"`
		StartAfter   int       `db:"start_after"`
	}
	err = tx.SelectContext(ctx, &tasks, `WITH idle AS (
 SELECT t.assignee_id AS agent_id, t.human_gate AS gate, s.category='triage' AS triage,
  EXISTS (SELECT 1 FROM parked_waits w WHERE w.task_id=t.id AND w.release_id IS NULL) AS parked,
  EXISTS (SELECT 1 FROM task_dependencies d JOIN tasks b ON b.id=d.depends_on_task_id JOIN task_statuses bs ON bs.id=b.status_id
   WHERE d.task_id=t.id AND d.dependency_type='blocks' AND b.deleted_at IS NULL AND bs.category NOT IN ('done','cancelled')) AS deps,
  COALESCE(t.start_after>now(), false) AS later
 FROM tasks t JOIN task_statuses s ON s.id=t.status_id JOIN projects p ON p.id=t.project_id
 WHERE p.workspace_id=$1 AND p.deleted_at IS NULL AND t.deleted_at IS NULL
 AND t.assignee_type='agent' AND t.assignee_id=ANY($2)
 AND s.category IN ('triage','todo','in_progress') AND t.checked_out_by IS NULL
 AND NOT EXISTS (SELECT 1 FROM runtime_reservations r WHERE r.task_id=t.id AND `+runtimeActiveReservation+`)
)
SELECT agent_id,
 count(*) FILTER (WHERE NOT (gate OR triage OR parked OR deps OR later)) AS ready,
 count(*) FILTER (WHERE gate OR triage OR parked OR deps OR later) AS waiting,
 count(*) FILTER (WHERE gate) AS human_gate, count(*) FILTER (WHERE triage) AS triage,
 count(*) FILTER (WHERE parked) AS parked_wait, count(*) FILTER (WHERE deps) AS dependencies,
 count(*) FILTER (WHERE later) AS start_after
FROM idle GROUP BY agent_id`, ws, idArray)
	if err != nil {
		return nil, err
	}
	for _, row := range tasks {
		c := &out.Agents[index[row.Agent]]
		c.Tasks = domain.RuntimeTaskCapView{Ready: row.Ready, Waiting: row.Waiting, WaitingBy: domain.RuntimeTaskWaitReasons{
			HumanGate: row.HumanGate, Triage: row.Triage, ParkedWait: row.ParkedWait, Dependencies: row.Dependencies, StartAfter: row.StartAfter}}
	}
	for i := range out.Agents {
		out.Agents[i].Derive()
	}
	return out, nil
}
