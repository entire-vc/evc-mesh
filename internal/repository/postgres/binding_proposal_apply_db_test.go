package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

type applyFixture struct {
	f        graphDBFixture
	repo     *postgres.BindingProposalRepo
	task     uuid.UUID
	edge     uuid.UUID
	proposal uuid.UUID
	ctx      context.Context
	tasks    []uuid.UUID
}

func newApplyFixture(t *testing.T) *applyFixture {
	t.Helper()
	f := newGraphDBFixture(t, 3)
	ctx := context.Background()
	var tasks []uuid.UUID
	require.NoError(t, f.db.SelectContext(ctx, &tasks, `SELECT id FROM tasks WHERE project_id=$1 ORDER BY task_number`, f.project))
	require.Len(t, tasks, 3)
	a := &applyFixture{f: f, repo: postgres.NewBindingProposalRepo(f.db), task: tasks[0], edge: uuid.New(), proposal: uuid.New(), ctx: ctx, tasks: tasks}
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition) VALUES($1,$2,$3,'blocks','task_done')`, a.edge, tasks[0], tasks[1])
	f.exec(t, `UPDATE tasks SET assignee_id=$1 WHERE id=$2`, f.user, tasks[0])
	require.NoError(t, a.repo.Create(ctx, &domain.StoredBindingProposal{
		BindingProposal: domain.BindingProposal{ID: a.proposal.String(), EdgeID: a.edge, Old: domain.DependencyConditionTaskDone,
			New: domain.DependencyConditionContractReady, Sources: []string{"s"}, Scope: "edge", Generation: "g"},
		ProposedBy: f.user,
	}))
	return a
}

func (a *applyFixture) state(t *testing.T) (cond string, applied bool) {
	t.Helper()
	require.NoError(t, a.f.db.GetContext(a.ctx, &cond, `SELECT condition FROM task_dependencies WHERE id=$1`, a.edge))
	p, err := a.repo.Get(a.ctx, a.proposal)
	require.NoError(t, err)
	require.NotNil(t, p)
	return cond, p.Applied
}

func TestApplyConfirm_AppliesEdgeAndProposalTogether(t *testing.T) {
	a := newApplyFixture(t)
	fp, err := a.repo.EdgeStateFingerprint(a.ctx, a.task)
	require.NoError(t, err)
	dep, err := a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
	require.NoError(t, err)
	assert.Equal(t, domain.DependencyConditionContractReady, dep.Condition)
	assert.Nil(t, dep.EvidenceURL, "a confirmed binding carries no evidence")
	assert.Nil(t, dep.ConditionMetAt, "and is never marked met")
	cond, applied := a.state(t)
	assert.Equal(t, "contract_ready", cond)
	assert.True(t, applied)

	_, err = a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
	assert.ErrorIs(t, err, domain.ErrReplay, "second apply of a consumed proposal is a replay")
}

func TestApplyConfirm_ConcurrentChangeRollsEverythingBack(t *testing.T) {
	a := newApplyFixture(t)
	fp, err := a.repo.EdgeStateFingerprint(a.ctx, a.task)
	require.NoError(t, err)
	// the edge moves after the fingerprint (and generation) were read
	a.f.exec(t, `UPDATE task_dependencies SET condition='shipped' WHERE id=$1`, a.edge)
	_, err = a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
	assert.ErrorIs(t, err, domain.ErrStaleGeneration)
	cond, applied := a.state(t)
	assert.Equal(t, "shipped", cond, "the concurrent write is not overwritten")
	assert.False(t, applied, "the proposal stays pending")

	// a change on another edge of the same task also invalidates the read
	a.f.exec(t, `UPDATE task_dependencies SET condition='task_done' WHERE id=$1`, a.edge)
	fp, err = a.repo.EdgeStateFingerprint(a.ctx, a.task)
	require.NoError(t, err)
	a.f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition) VALUES($1,$2,$3,'blocks','task_done')`, uuid.New(), a.task, a.tasks[2])
	_, err = a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
	assert.ErrorIs(t, err, domain.ErrStaleGeneration)
	_, applied = a.state(t)
	assert.False(t, applied)
}

func TestApplyConfirm_FailureLeavesPendingAndRetryAppliesOnce(t *testing.T) {
	a := newApplyFixture(t)
	fp, err := a.repo.EdgeStateFingerprint(a.ctx, a.task)
	require.NoError(t, err)
	_, err = a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), "")
	assert.ErrorIs(t, err, domain.ErrStaleGeneration, "no fingerprint, no write")
	// a proposal addressed through the wrong edge is refused without touching anything
	_, err = a.repo.ApplyConfirm(a.ctx, a.proposal, uuid.New(), a.task, a.f.user, time.Now(), fp)
	assert.Error(t, err, "proposal does not belong to that edge")
	cond, applied := a.state(t)
	assert.Equal(t, "task_done", cond)
	assert.False(t, applied)

	dep, err := a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
	require.NoError(t, err, "retry after the failures applies")
	assert.Equal(t, domain.DependencyConditionContractReady, dep.Condition)
	_, err = a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
	assert.Error(t, err)
}

func TestApplyConfirm_ConcurrentConfirmsApplyExactlyOnce(t *testing.T) {
	a := newApplyFixture(t)
	fp, err := a.repo.EdgeStateFingerprint(a.ctx, a.task)
	require.NoError(t, err)
	const n = 8
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
		}(i)
	}
	wg.Wait()
	won := 0
	for _, e := range results {
		if e == nil {
			won++
		} else {
			assert.True(t, e == domain.ErrReplay || e == domain.ErrStaleGeneration, "loser gets a conflict, got %v", e)
		}
	}
	assert.Equal(t, 1, won, "exactly one confirm applies")
	cond, applied := a.state(t)
	assert.Equal(t, "contract_ready", cond)
	assert.True(t, applied)
}

func TestApplyConfirm_RevalidatesOwnerAndGateInsideTransaction(t *testing.T) {
	a := newApplyFixture(t)
	fp, err := a.repo.EdgeStateFingerprint(a.ctx, a.task)
	require.NoError(t, err)

	// reassigned after the handler read the task: the former owner is refused
	a.f.exec(t, `UPDATE tasks SET assignee_id=$1 WHERE id=$2`, uuid.New(), a.task)
	_, err = a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
	require.ErrorIs(t, err, domain.ErrNotOwner)
	cond, applied := a.state(t)
	assert.Equal(t, "task_done", cond)
	assert.False(t, applied, "a refused confirm leaves edge and proposal untouched")

	// owner again, but a human gate was raised meanwhile
	a.f.exec(t, `UPDATE tasks SET assignee_id=$1, human_gate=true WHERE id=$2`, a.f.user, a.task)
	_, err = a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
	require.ErrorIs(t, err, domain.ErrHeldGate)
	_, applied = a.state(t)
	assert.False(t, applied)

	a.f.exec(t, `UPDATE tasks SET human_gate=false WHERE id=$1`, a.task)
	_, err = a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
	require.NoError(t, err)
}

// The next-action generation also folds in the blocker's own state (name,
// status, VCS links with head/verdict metadata). A change there between the
// read and the apply must refuse the confirm even though no edge field moved.
func TestApplyConfirm_BlockerStateChangeInvalidatesTheRead(t *testing.T) {
	a := newApplyFixture(t)
	blocker := a.tasks[1]

	fp, err := a.repo.EdgeStateFingerprint(a.ctx, a.task)
	require.NoError(t, err)
	a.f.exec(t, `UPDATE tasks SET title = title || ' (renamed)' WHERE id=$1`, blocker)
	_, err = a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
	require.ErrorIs(t, err, domain.ErrStaleGeneration, "blocker name is part of the generation")
	_, applied := a.state(t)
	assert.False(t, applied)

	fp, err = a.repo.EdgeStateFingerprint(a.ctx, a.task)
	require.NoError(t, err)
	a.f.exec(t, `INSERT INTO vcs_links(task_id, provider, link_type, external_id, url, status, metadata)
		VALUES($1,'gitlab','pr','example/product!1','https://x/1','open','{"head_sha":"abc"}')`, blocker)
	_, err = a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
	require.ErrorIs(t, err, domain.ErrStaleGeneration, "blocker VCS link/head is part of the generation")

	fp, err = a.repo.EdgeStateFingerprint(a.ctx, a.task)
	require.NoError(t, err)
	a.f.exec(t, `UPDATE vcs_links SET metadata='{"head_sha":"def"}' WHERE task_id=$1`, blocker)
	_, err = a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
	require.ErrorIs(t, err, domain.ErrStaleGeneration, "a new head on the blocker's PR link invalidates it")

	fp, err = a.repo.EdgeStateFingerprint(a.ctx, a.task)
	require.NoError(t, err)
	_, err = a.repo.ApplyConfirm(a.ctx, a.proposal, a.edge, a.task, a.f.user, time.Now(), fp)
	require.NoError(t, err, "with an unchanged blocker the confirm applies")
}
