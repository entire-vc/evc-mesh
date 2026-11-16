package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

const (
	deployedWaitProdMeta = `{"merged_sha":"0123456789abcdef0123456789abcdef01234567","deployed":{"sha":"0123456789abcdef0123456789abcdef01234567","environment":"prod","at":"2026-11-14T08:00:00Z"}}`
	deployedWaitMerged   = `{"merged_sha":"1111111111111111111111111111111111111111"}`
	deployedWaitSecond   = `{"merged_sha":"1111111111111111111111111111111111111111","deployed":{"sha":"1111111111111111111111111111111111111111","environment":"prod","at":"2026-11-14T09:00:00Z"}}`
)

func deployedWaitLink(t *testing.T, f *typedOutcomeFixture, task uuid.UUID, iid, status, meta string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.Exec(`INSERT INTO vcs_links (id,task_id,provider,link_type,external_id,url,status,metadata)
		VALUES ($1,$2,'gitlab','pr',$3,$4,$5,$6::jsonb)`, id, task, iid, "https://git.example/deployed/"+iid, status, meta)
	require.NoError(t, err)
	return id
}

func deployedWaitDeclare(t *testing.T) (f, target *typedOutcomeFixture, declare func() *domain.TaskWait) {
	t.Helper()
	f, target = newTypedOutcomeFixture(t), newTypedOutcomeFixture(t)
	crossWaitGrant(t, f, f.agent, target.task)
	f.holdCheckout(t, f.task, f.agent)
	return f, target, func() *domain.TaskWait {
		res, err := NewTurnCommandRepo(f.db).CommitTurn(context.Background(), crossWaitCommand(f.task, target.task, f.agent, "deployed"))
		require.NoError(t, err)
		require.NotNil(t, res.Wait)
		require.True(t, res.Wait.ServerObserved)
		return res.Wait
	}
}

func deployedWaitOpen(t *testing.T, f *typedOutcomeFixture, waitID uuid.UUID) bool {
	t.Helper()
	var open bool
	require.NoError(t, f.db.Get(&open, `SELECT resolved_at IS NULL FROM task_waits WHERE id=$1`, waitID))
	return open
}

// A target delivered by two MRs, one on prod and one merged without
// a deploy. A server-observed `deployed` wait keeps waiting like the edge does
// (domain.DeploymentReleasesSQL) and is met only once the second MR is on prod.
func TestTurnCommand_ServerCardWaitDeployedHoldsWhileSiblingMRNotOnProd_DB(t *testing.T) {
	f, target, declare := deployedWaitDeclare(t)
	deployedWaitLink(t, target, target.task, "120", "merged", deployedWaitProdMeta)
	second := deployedWaitLink(t, target, target.task, "100", "merged", deployedWaitMerged)

	w := declare()
	assert.Nil(t, w.ResolvedAt, "declaration: one MR on prod, the other merged without a deploy")
	assert.True(t, deployedWaitOpen(t, f, w.ID), "the wait stays open while a sibling MR is not on prod")
	edges, err := NewTaskDependencyRepo(f.db).ListByTask(context.Background(), f.task)
	require.NoError(t, err)
	held := false
	for _, e := range edges {
		if e.DependsOnTaskID == target.task {
			held = !e.ConditionSatisfied
		}
	}
	assert.True(t, held, "the Go rule holds the edge too")

	_, err = f.db.Exec(`UPDATE vcs_links SET metadata=$2::jsonb WHERE id=$1`, second, deployedWaitSecond)
	require.NoError(t, err)
	got := crossWaitReceipt(t, f, w.ID, domain.WaitResolutionMet)
	require.NotNil(t, got.ResolveCause)
	assert.Equal(t, "target_condition_met", *got.ResolveCause)
	require.NotNil(t, got.ReleasedBy)
	assert.Equal(t, domain.ReleasedByTargetState, *got.ReleasedBy)
}

// Review-subject and relates_to links are not the target's own work: their
// prod deploy alone neither meets the wait; an own link on prod then does.
func TestTurnCommand_ServerCardWaitDeployedIgnoresForeignLinks_DB(t *testing.T) {
	for name, meta := range map[string]string{
		"review-subject": `{"role":"review","deployed":{"sha":"0123456789abcdef0123456789abcdef01234567","environment":"prod","at":"2026-11-14T08:00:00Z"}}`,
		"relates_to":     `{"relation":"relates_to","deployed":{"sha":"0123456789abcdef0123456789abcdef01234567","environment":"prod","at":"2026-11-14T08:00:00Z"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f, target, declare := deployedWaitDeclare(t)
			w := declare()
			deployedWaitLink(t, target, target.task, "7", "merged", meta)
			assert.True(t, deployedWaitOpen(t, f, w.ID), "a %s link on prod alone does not meet deployed", name)

			// positive control: the same fixture is met by an own link on prod
			deployedWaitLink(t, target, target.task, "8", "merged", deployedWaitProdMeta)
			got := crossWaitReceipt(t, f, w.ID, domain.WaitResolutionMet)
			require.NotNil(t, got.ResolveCause)
			assert.Equal(t, "target_condition_met", *got.ResolveCause)
		})
	}
}
