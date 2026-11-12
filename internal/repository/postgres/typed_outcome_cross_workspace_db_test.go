package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func crossWaitGrant(t *testing.T, f *typedOutcomeFixture, agent, task uuid.UUID) (grantID, workspaceID uuid.UUID) {
	t.Helper()
	_, ws, err := NewTurnCommandRepo(f.db).TaskScope(context.Background(), task)
	require.NoError(t, err)
	id := uuid.New()
	_, err = f.db.Exec(`INSERT INTO agent_workspace_grants
		(id, agent_id, workspace_id, api_key_prefix, api_key_hash) VALUES ($1,$2,$3,$4,'x')`,
		id, agent, ws, uuid.New().String()[:16])
	require.NoError(t, err)
	return id, ws
}

func crossWaitCommand(source, target, agent uuid.UUID, condition string) *domain.TurnCommand {
	return &domain.TurnCommand{TaskID: source, AgentID: agent, Kind: domain.TurnOutcomeWait,
		CommentBody: "Waiting on the granted workspace's card.",
		Wait:        &domain.TaskWait{Kind: domain.TaskWaitCard, TargetTaskID: &target, TargetCondition: &condition}}
}

func crossWaitDoneStatus(t *testing.T, f *typedOutcomeFixture) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.Exec(`INSERT INTO task_statuses (id,project_id,name,slug,category,position)
		SELECT $1,project_id,'Done','cross-done','done',9 FROM tasks WHERE id=$2`, id, f.task)
	require.NoError(t, err)
	return id
}

func crossWaitReceipt(t *testing.T, f *typedOutcomeFixture, waitID uuid.UUID, resolution domain.WaitResolution) *domain.TaskWait {
	t.Helper()
	w, ws, err := f.waits.GetForTask(context.Background(), f.task, waitID)
	require.NoError(t, err)
	require.NotNil(t, w)
	_, sourceWS, err := NewTurnCommandRepo(f.db).TaskScope(context.Background(), f.task)
	require.NoError(t, err)
	assert.Equal(t, sourceWS, ws, "the receipt is scoped to the source, not the target")
	assert.True(t, w.ServerObserved)
	require.NotNil(t, w.ResolvedAt)
	require.NotNil(t, w.Resolution)
	assert.Equal(t, resolution, *w.Resolution)
	return w
}

func TestTurnCommand_CrossWorkspaceGrantAndNoOracle_DB(t *testing.T) {
	f, target := newTypedOutcomeFixture(t), newTypedOutcomeFixture(t)
	ctx := context.Background()
	f.holdCheckout(t, f.task, f.agent)
	repo := NewTurnCommandRepo(f.db)
	_, denied := repo.CommitTurn(ctx, crossWaitCommand(f.task, target.task, f.agent, "task_done"))
	_, missing := repo.CommitTurn(ctx, crossWaitCommand(f.task, uuid.New(), f.agent, "task_done"))
	assert.Equal(t, domain.CodeInvalidTarget, outcomeCodeOf(t, denied))
	assert.Equal(t, domain.CodeInvalidTarget, outcomeCodeOf(t, missing))
	assert.EqualError(t, denied, "invalid_target: target_task_id: target card not found")
	assert.Equal(t, denied.Error(), missing.Error(), "membership must not become an existence oracle")
	projectDenied := crossWaitCommand(f.task, f.other, f.agent, "task_done")
	projectDenied.DenySameWorkspaceTarget = true
	_, sameWorkspaceDenied := repo.CommitTurn(ctx, projectDenied)
	assert.EqualError(t, sameWorkspaceDenied, "invalid_target: target_task_id: target card not found")
	var rows int
	require.NoError(t, f.db.Get(&rows, `SELECT
		(SELECT count(*) FROM task_dependencies WHERE task_id=$1) +
		(SELECT count(*) FROM task_waits WHERE task_id=$1) +
		(SELECT count(*) FROM turn_outcomes WHERE task_id=$1)`, f.task))
	assert.Zero(t, rows, "refused declarations must be atomic")

	crossWaitGrant(t, f, f.agent, target.task)
	res, err := repo.CommitTurn(ctx, crossWaitCommand(f.task, target.task, f.agent, "task_done"))
	require.NoError(t, err)
	require.NotNil(t, res.Wait)
	assert.True(t, res.Wait.ServerObserved)
	assert.True(t, res.Released)
	var edge struct {
		Kind  string    `db:"dependency_type"`
		Agent uuid.UUID `db:"wait_agent_id"`
	}
	require.NoError(t, f.db.Get(&edge, `SELECT dependency_type,wait_agent_id FROM task_dependencies
		WHERE task_id=$1 AND depends_on_task_id=$2`, f.task, target.task))
	assert.Equal(t, "blocks", edge.Kind)
	assert.Equal(t, f.agent, edge.Agent)

	// Membership does not relax the ordinary dependency write path. Only
	// the typed command supplies the provenance required by the DB guard.
	_, err = f.db.Exec(`INSERT INTO task_dependencies (id,task_id,depends_on_task_id,dependency_type)
		VALUES (gen_random_uuid(),$1,$2,'blocks')`, f.other, target.other)
	require.Error(t, err)
	var pe *pq.Error
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "completion_scope", pe.Constraint)
}

func TestTurnCommand_CrossWorkspaceTargetEventsResolveInTransaction_DB(t *testing.T) {
	for _, condition := range []string{"task_done", "accepted", "shipped", "deployed"} {
		t.Run(condition, func(t *testing.T) {
			f, target := newTypedOutcomeFixture(t), newTypedOutcomeFixture(t)
			crossWaitGrant(t, f, f.agent, target.task)
			f.holdCheckout(t, f.task, f.agent)
			res, err := NewTurnCommandRepo(f.db).CommitTurn(context.Background(), crossWaitCommand(f.task, target.task, f.agent, condition))
			require.NoError(t, err)
			require.NotNil(t, res.Wait)
			tx, err := f.db.Beginx()
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			switch condition {
			case "shipped":
				_, err = tx.Exec(`UPDATE tasks SET is_shipped=true WHERE id=$1`, target.task)
			case "deployed":
				_, err = tx.Exec(`INSERT INTO vcs_links (task_id,provider,link_type,external_id,url,metadata)
					VALUES ($1,'gitlab','pr','42','https://git.example/cross/42',
					'{"deployed":{"sha":"0123456789abcdef0123456789abcdef01234567","at":"2026-11-11T12:00:00Z","environment":"staging"}}')`, target.task)
				require.NoError(t, err)
				var active bool
				require.NoError(t, tx.Get(&active, `SELECT resolved_at IS NULL FROM task_waits WHERE id=$1`, res.Wait.ID))
				assert.True(t, active, "a staging deployment cannot satisfy deployed")
				_, err = tx.Exec(`UPDATE vcs_links SET metadata=jsonb_set(metadata,'{deployed,environment}','"prod"') WHERE task_id=$1`, target.task)
			default:
				_, err = tx.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, target.task, crossWaitDoneStatus(t, target))
			}
			require.NoError(t, err)
			var got domain.WaitResolution
			require.NoError(t, tx.Get(&got, `SELECT resolution FROM task_waits WHERE id=$1 AND resolved_at IS NOT NULL`, res.Wait.ID))
			assert.Equal(t, domain.WaitResolutionMet, got, "the target mutation itself must resolve, before commit")
			require.NoError(t, tx.Commit())
			crossWaitReceipt(t, f, res.Wait.ID, domain.WaitResolutionMet)
		})
	}
}

func TestTurnCommand_CrossWorkspaceGrantRevokeInvalidatesAndUnblocks_DB(t *testing.T) {
	for _, revokedSide := range []string{"source", "target"} {
		t.Run(revokedSide, func(t *testing.T) {
			f, target, home := newTypedOutcomeFixture(t), newTypedOutcomeFixture(t), newTypedOutcomeFixture(t)
			// The caller is a guest on both sides: neither grant can be
			// accidentally masked by the caller's home membership.
			sourceGrant, sourceWS := crossWaitGrant(t, f, home.agent, f.task)
			targetGrant, targetWS := crossWaitGrant(t, f, home.agent, target.task)
			f.holdCheckout(t, f.task, home.agent)
			res, err := NewTurnCommandRepo(f.db).CommitTurn(context.Background(), crossWaitCommand(f.task, target.task, home.agent, "task_done"))
			require.NoError(t, err)
			require.NotNil(t, res.Wait)
			grant, ws := targetGrant, targetWS
			if revokedSide == "source" {
				grant, ws = sourceGrant, sourceWS
			}
			found, err := NewAgentWorkspaceGrantRepo(f.db).Revoke(context.Background(), grant, ws)
			require.NoError(t, err)
			require.True(t, found)
			w := crossWaitReceipt(t, f, res.Wait.ID, domain.WaitResolutionInvalid)
			require.NotNil(t, w.ResolveCause)
			assert.Equal(t, "grant_revoked", *w.ResolveCause)
			var edges int
			require.NoError(t, f.db.Get(&edges, `SELECT count(*) FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2`, f.task, target.task))
			assert.Zero(t, edges, "a revoked wait must not strand its source behind a blocks edge")
		})
	}
}

func TestTurnCommand_CrossWorkspaceCycleAndForeignWitnessRedaction_DB(t *testing.T) {
	f, foreign := newTypedOutcomeFixture(t), newTypedOutcomeFixture(t)
	ctx := context.Background()
	// A(local) -> B(foreign) -> C(local). The final ordinary local
	// edge C -> A must still see B, without exposing B in cycle details.
	crossWaitGrant(t, f, f.agent, foreign.task)
	f.holdCheckout(t, f.task, f.agent)
	_, err := NewTurnCommandRepo(f.db).CommitTurn(ctx, crossWaitCommand(f.task, foreign.task, f.agent, "task_done"))
	require.NoError(t, err)
	crossWaitGrant(t, f, foreign.agent, f.other)
	foreign.holdCheckout(t, foreign.task, foreign.agent)
	_, err = NewTurnCommandRepo(f.db).CommitTurn(ctx, crossWaitCommand(foreign.task, f.other, foreign.agent, "task_done"))
	require.NoError(t, err)
	err = NewTaskDependencyRepo(f.db).Create(ctx, &domain.TaskDependency{ID: uuid.New(), TaskID: f.other,
		DependsOnTaskID: f.task, DependencyType: domain.DependencyTypeBlocks, CreatedAt: time.Now()})
	require.Error(t, err)
	var pe *pq.Error
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "completion_cycle", pe.Constraint)
	assert.Empty(t, pe.Detail, "the local caller must not receive a foreign cycle witness")
	assert.NotContains(t, err.Error(), foreign.task.String())
	var edges int
	require.NoError(t, f.db.Get(&edges, `SELECT count(*) FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2`, f.other, f.task))
	assert.Zero(t, edges)
}

func TestTurnCommand_CrossWorkspaceTargetChangeDeclarationRace_DB(t *testing.T) {
	// Exercise both contenders against the same target. Regardless of which
	// commits first, the outcome is already_met or a durable met receipt;
	// an active wait after both transactions finish is a lost wake.
	for attempt := 0; attempt < 4; attempt++ {
		f, target := newTypedOutcomeFixture(t), newTypedOutcomeFixture(t)
		crossWaitGrant(t, f, f.agent, target.task)
		done := crossWaitDoneStatus(t, target)
		f.holdCheckout(t, f.task, f.agent)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		start := make(chan struct{})
		type declaration struct {
			result *domain.TurnCommandResult
			err    error
		}
		declared := make(chan declaration, 1)
		changed := make(chan error, 1)
		go func() {
			<-start
			res, err := NewTurnCommandRepo(f.db).CommitTurn(ctx, crossWaitCommand(f.task, target.task, f.agent, "task_done"))
			declared <- declaration{res, err}
		}()
		go func() {
			<-start
			_, err := f.db.ExecContext(ctx, `UPDATE tasks SET status_id=$2 WHERE id=$1`, target.task, done)
			changed <- err
		}()
		close(start)
		got, changeErr := <-declared, <-changed
		cancel()
		require.NoError(t, changeErr)
		if got.err != nil {
			var oe *domain.OutcomeError
			require.True(t, errors.As(got.err, &oe), "unexpected race error: %v", got.err)
			assert.Equal(t, domain.CodeAlreadyMet, oe.Code)
		} else {
			require.NotNil(t, got.result)
			require.NotNil(t, got.result.Wait)
			crossWaitReceipt(t, f, got.result.Wait.ID, domain.WaitResolutionMet)
		}
		var active int
		require.NoError(t, f.db.Get(&active, `SELECT count(*) FROM task_waits WHERE task_id=$1 AND resolved_at IS NULL`, f.task))
		assert.Zero(t, active, "a target transition raced past declaration")
	}
}

func TestTurnCommand_CrossWorkspaceRevokeDeclarationRace_DB(t *testing.T) {
	for attempt := 0; attempt < 4; attempt++ {
		f, target := newTypedOutcomeFixture(t), newTypedOutcomeFixture(t)
		grant, ws := crossWaitGrant(t, f, f.agent, target.task)
		f.holdCheckout(t, f.task, f.agent)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		start := make(chan struct{})
		type declaration struct {
			result *domain.TurnCommandResult
			err    error
		}
		declared := make(chan declaration, 1)
		type revocation struct {
			found bool
			err   error
		}
		revoked := make(chan revocation, 1)
		go func() {
			<-start
			res, err := NewTurnCommandRepo(f.db).CommitTurn(ctx, crossWaitCommand(f.task, target.task, f.agent, "task_done"))
			declared <- declaration{res, err}
		}()
		go func() {
			<-start
			found, err := NewAgentWorkspaceGrantRepo(f.db).Revoke(ctx, grant, ws)
			revoked <- revocation{found, err}
		}()
		close(start)
		got, revoke := <-declared, <-revoked
		cancel()
		require.NoError(t, revoke.err)
		require.True(t, revoke.found)
		if got.err != nil {
			assert.Equal(t, domain.CodeInvalidTarget, outcomeCodeOf(t, got.err))
			assert.EqualError(t, got.err, "invalid_target: target_task_id: target card not found")
		} else {
			require.NotNil(t, got.result)
			require.NotNil(t, got.result.Wait)
			w := crossWaitReceipt(t, f, got.result.Wait.ID, domain.WaitResolutionInvalid)
			require.NotNil(t, w.ResolveCause)
			assert.Equal(t, "grant_revoked", *w.ResolveCause)
		}
		var active, edges int
		require.NoError(t, f.db.Get(&active, `SELECT count(*) FROM task_waits WHERE task_id=$1 AND resolved_at IS NULL`, f.task))
		require.NoError(t, f.db.Get(&edges, `SELECT count(*) FROM task_dependencies
			WHERE task_id=$1 AND depends_on_task_id=$2 AND dependency_type='blocks'`, f.task, target.task))
		assert.Zero(t, active, "declaration must not survive a concurrent grant revoke")
		assert.Zero(t, edges, "the revoked wait must leave no active blocks edge")
	}
}

func TestTurnCommand_CrossWorkspaceDeletedTargetRetainsSourceReceipt_DB(t *testing.T) {
	for _, deletion := range []string{"soft", "hard"} {
		t.Run(deletion, func(t *testing.T) {
			f, target := newTypedOutcomeFixture(t), newTypedOutcomeFixture(t)
			crossWaitGrant(t, f, f.agent, target.task)
			f.holdCheckout(t, f.task, f.agent)
			res, err := NewTurnCommandRepo(f.db).CommitTurn(context.Background(), crossWaitCommand(f.task, target.task, f.agent, "task_done"))
			require.NoError(t, err)
			require.NotNil(t, res.Wait)
			tx, err := f.db.Beginx()
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			if deletion == "soft" {
				_, err = tx.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, target.task)
			} else {
				_, err = tx.Exec(`DELETE FROM tasks WHERE id=$1`, target.task)
			}
			require.NoError(t, err)
			var resolved bool
			require.NoError(t, tx.Get(&resolved, `SELECT resolved_at IS NOT NULL
				AND resolution='invalid' AND resolve_cause='target_deleted' FROM task_waits WHERE id=$1`, res.Wait.ID))
			assert.True(t, resolved, "target deletion must resolve the wait inside the deleting transaction")
			require.NoError(t, tx.Commit())
			w := crossWaitReceipt(t, f, res.Wait.ID, domain.WaitResolutionInvalid)
			require.NotNil(t, w.ResolveCause)
			assert.Equal(t, "target_deleted", *w.ResolveCause)
			if deletion == "hard" {
				assert.Nil(t, w.TargetTaskID, "the own-card receipt must survive the target FK being cleared")
			}
		})
	}
}

func TestTurnCommand_CrossWorkspaceStatusCategoryChangeResolvesInTransaction_DB(t *testing.T) {
	f, target := newTypedOutcomeFixture(t), newTypedOutcomeFixture(t)
	crossWaitGrant(t, f, f.agent, target.task)
	f.holdCheckout(t, f.task, f.agent)
	res, err := NewTurnCommandRepo(f.db).CommitTurn(context.Background(), crossWaitCommand(f.task, target.task, f.agent, "accepted"))
	require.NoError(t, err)
	require.NotNil(t, res.Wait)
	var statusID uuid.UUID
	require.NoError(t, f.db.Get(&statusID, `SELECT status_id FROM tasks WHERE id=$1`, target.task))
	tx, err := f.db.Beginx()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// No task move occurs: editing the existing status changes the
	// condition's truth, so a task-only trigger would miss this event.
	_, err = tx.Exec(`UPDATE task_statuses SET category='done' WHERE id=$1`, statusID)
	require.NoError(t, err)
	var met bool
	require.NoError(t, tx.Get(&met, `SELECT resolved_at IS NOT NULL AND resolution='met' FROM task_waits WHERE id=$1`, res.Wait.ID))
	assert.True(t, met, "the status-category transaction itself must resolve the wait")
	require.NoError(t, tx.Commit())
	crossWaitReceipt(t, f, res.Wait.ID, domain.WaitResolutionMet)
	var unchangedStatus uuid.UUID
	require.NoError(t, f.db.Get(&unchangedStatus, `SELECT status_id FROM tasks WHERE id=$1`, target.task))
	assert.Equal(t, statusID, unchangedStatus)
}

func TestTurnCommand_CrossWorkspaceConditionChangeRevokesPreviousEvidence_DB(t *testing.T) {
	f, target := newTypedOutcomeFixture(t), newTypedOutcomeFixture(t)
	ctx := context.Background()
	crossWaitGrant(t, f, f.agent, target.task)
	f.holdCheckout(t, f.task, f.agent)
	repo := NewTurnCommandRepo(f.db)
	first, err := repo.CommitTurn(ctx, crossWaitCommand(f.task, target.task, f.agent, "shipped"))
	require.NoError(t, err)
	require.NotNil(t, first.Wait)
	var previousGeneration int64
	// Real trusted evidence releases the shipped edge even though the
	// target itself remains open, unshipped and without a prod deployment.
	require.NoError(t, f.db.Get(&previousGeneration, `UPDATE task_dependencies
		SET evidence_status='trusted', evidence_url='https://git.example/ship/42', condition_met_at=now()
		WHERE task_id=$1 AND depends_on_task_id=$2 RETURNING generation`, f.task, target.task))
	crossWaitReceipt(t, f, first.Wait.ID, domain.WaitResolutionMet)

	f.holdCheckout(t, f.task, f.agent)
	second, err := repo.CommitTurn(ctx, crossWaitCommand(f.task, target.task, f.agent, "deployed"))
	require.NoError(t, err)
	require.NotNil(t, second.Wait)
	assert.NotEqual(t, first.Wait.ID, second.Wait.ID)
	w, _, err := f.waits.GetForTask(ctx, f.task, second.Wait.ID)
	require.NoError(t, err)
	require.NotNil(t, w)
	assert.True(t, w.ServerObserved)
	assert.Nil(t, w.ResolvedAt, "shipped evidence cannot satisfy the new deployed wait")
	assert.Nil(t, w.Resolution)
	edges, err := NewTaskDependencyRepo(f.db).ListByTask(ctx, f.task)
	require.NoError(t, err)
	require.Len(t, edges, 1, "the typed command must update the existing cross edge")
	assert.Equal(t, domain.DependencyConditionDeployed, edges[0].Condition)
	assert.Equal(t, domain.EvidenceRevoked, edges[0].EvidenceStatus)
	assert.Greater(t, edges[0].Generation, previousGeneration)
	assert.False(t, edges[0].ConditionSatisfied, "a receipt for another condition must not release the edge")
}
