//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// TestTaskRepo_SetHumanGate_ArmDefaultsClassHard proves acceptance criterion 3
// (contract docs/human-gate-decision-recorded.md §5, task #b1d5c742): a task armed
// through the ordinary flow — no prior SetHumanGateClass call, i.e. a "живая
// немаркированная карточка" — ends up human_gate_class='hard', fail-closed, and
// human_gate_armed_at gets stamped.
func TestTaskRepo_SetHumanGate_ArmDefaultsClassHard(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)

	before := time.Now().UTC()
	require.NoError(t, taskRepo.SetHumanGate(ctx, taskID, true))
	after := time.Now().UTC()

	got, err := taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.HumanGate)
	assert.Equal(t, domain.HumanGateClassHard, got.HumanGateClass, "an ordinary arm with no prior classification must default hard")
	require.NotNil(t, got.HumanGateArmedAt)
	assert.False(t, got.HumanGateArmedAt.Before(before.Add(-time.Second)), "armed_at should be stamped at arm time")
	assert.False(t, got.HumanGateArmedAt.After(after.Add(time.Second)))
}

// TestTaskRepo_SetHumanGate_ReleaseResetsClassToHard proves the release-side of the
// fail-closed default: a soft classification never survives past the ask it was
// configured for. Arm, classify soft, release, arm again with no reclassification —
// the second arm must be hard.
func TestTaskRepo_SetHumanGate_ReleaseResetsClassToHard(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)

	require.NoError(t, taskRepo.SetHumanGate(ctx, taskID, true))
	require.NoError(t, taskRepo.SetHumanGateClass(ctx, taskID, domain.HumanGateClassSoft))

	got, err := taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, domain.HumanGateClassSoft, got.HumanGateClass, "precondition: classified soft before release")

	require.NoError(t, taskRepo.SetHumanGate(ctx, taskID, false))
	got, err = taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	assert.False(t, got.HumanGate)
	assert.Equal(t, domain.HumanGateClassHard, got.HumanGateClass, "release must reset class back to hard")

	// Re-arm with no reclassification in between: must be hard again, not a stale soft.
	require.NoError(t, taskRepo.SetHumanGate(ctx, taskID, true))
	got, err = taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	assert.Equal(t, domain.HumanGateClassHard, got.HumanGateClass)
}

// TestTaskRepo_FindSoftTimedOutGates_ExcludesHardStructurally is the structural proof
// contract §5.1 demands: "не тем, что за окно наблюдения ничего не разморозилось" — a
// hard gate must be provably unreachable from the sweep, not merely absent because no
// one waited long enough. This constructs a hard-classified gate that is eligible by
// EVERY OTHER criterion (armed, decades-old armed_at) and queries with a cutoff decades
// in the future — if class exclusion were not structural (e.g. accidentally dropped from
// the WHERE clause), this hard row would be the FIRST one returned. It is not.
func TestTaskRepo_FindSoftTimedOutGates_ExcludesHardStructurally(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()

	hardID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	softID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)

	require.NoError(t, taskRepo.SetHumanGate(ctx, hardID, true)) // defaults hard
	require.NoError(t, taskRepo.SetHumanGate(ctx, softID, true))
	require.NoError(t, taskRepo.SetHumanGateClass(ctx, softID, domain.HumanGateClassSoft))

	ancient := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.ExecContext(ctx, `UPDATE tasks SET human_gate_armed_at = $2 WHERE id = $1`, hardID, ancient)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE tasks SET human_gate_armed_at = $2 WHERE id = $1`, softID, ancient)
	require.NoError(t, err)

	// Deliberately absurd cutoff — decades in the future — so ONLY the class predicate,
	// never the time comparison, can be excluding the hard row.
	farFutureCutoff := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	candidates, err := taskRepo.FindSoftTimedOutGates(ctx, farFutureCutoff)
	require.NoError(t, err)

	ids := make(map[uuid.UUID]bool, len(candidates))
	for _, c := range candidates {
		ids[c.TaskID] = true
	}
	assert.True(t, ids[softID], "soft gate armed decades ago must be found")
	assert.False(t, ids[hardID], "hard gate must be structurally unreachable regardless of cutoff")
}

// TestTaskRepo_FindSoftTimedOutGates_RespectsCutoffAndLiveFlag covers the two other
// predicates in the same query: armed_at vs cutoff, and human_gate must still be true
// (a soft gate already released must not be re-swept).
func TestTaskRepo_FindSoftTimedOutGates_RespectsCutoffAndLiveFlag(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()

	notYetDueID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	require.NoError(t, taskRepo.SetHumanGate(ctx, notYetDueID, true))
	require.NoError(t, taskRepo.SetHumanGateClass(ctx, notYetDueID, domain.HumanGateClassSoft))
	// armed_at defaults to "now" from SetHumanGate above — freshly armed, not due yet.

	alreadyReleasedID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	require.NoError(t, taskRepo.SetHumanGate(ctx, alreadyReleasedID, true))
	require.NoError(t, taskRepo.SetHumanGateClass(ctx, alreadyReleasedID, domain.HumanGateClassSoft))
	ancient := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.ExecContext(ctx, `UPDATE tasks SET human_gate_armed_at = $2 WHERE id = $1`, alreadyReleasedID, ancient)
	require.NoError(t, err)
	require.NoError(t, taskRepo.SetHumanGate(ctx, alreadyReleasedID, false)) // released — human_gate now false

	dueID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	require.NoError(t, taskRepo.SetHumanGate(ctx, dueID, true))
	require.NoError(t, taskRepo.SetHumanGateClass(ctx, dueID, domain.HumanGateClassSoft))
	_, err = db.ExecContext(ctx, `UPDATE tasks SET human_gate_armed_at = $2 WHERE id = $1`, dueID, ancient)
	require.NoError(t, err)

	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	candidates, err := taskRepo.FindSoftTimedOutGates(ctx, cutoff)
	require.NoError(t, err)

	ids := make(map[uuid.UUID]bool, len(candidates))
	for _, c := range candidates {
		ids[c.TaskID] = true
	}
	assert.True(t, ids[dueID], "armed decades ago, still live, soft → due")
	assert.False(t, ids[notYetDueID], "armed moments ago → not due yet")
	assert.False(t, ids[alreadyReleasedID], "human_gate already false → must not be re-swept")
}
