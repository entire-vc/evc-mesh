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
// (contract docs/human-gate-decision-recorded.md §5, the regression): a task armed
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

// TestTaskRepo_ListMarkerArmedHardGateIDs lists exactly the live hard gates whose
// recorded arm source is a marker: the input of the marker-gate reclassification (task
// this change). A soft gate, a released one, an API arm and a raw PATCH flip (source
// unrecorded) are not in it.
func TestTaskRepo_ListMarkerArmedHardGateIDs(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	marker := createTestTaskForComments(t, repo, proj.ID, status.ID)
	api := createTestTaskForComments(t, repo, proj.ID, status.ID)
	soft := createTestTaskForComments(t, repo, proj.ID, status.ID)
	released := createTestTaskForComments(t, repo, proj.ID, status.ID)
	rearmed := createTestTaskForComments(t, repo, proj.ID, status.ID)
	for id, arm := range map[uuid.UUID]struct {
		class  domain.HumanGateClass
		source domain.ArmHumanGateSource
	}{
		marker:   {domain.HumanGateClassHard, domain.ArmHumanGateSourceMarker},
		api:      {domain.HumanGateClassHard, domain.ArmHumanGateSourceAPI},
		soft:     {domain.HumanGateClassSoft, domain.ArmHumanGateSourceMarker},
		released: {domain.HumanGateClassHard, domain.ArmHumanGateSourceMarker},
		rearmed:  {domain.HumanGateClassHard, domain.ArmHumanGateSourceMarker},
	} {
		require.NoError(t, repo.ArmHumanGate(ctx, domain.ArmHumanGateInput{
			TaskID: id, Author: uuid.New(), AuthorType: domain.ActorTypeAgent, Reason: "why",
			RecommendedDefault: "go on", Class: arm.class, Source: arm.source,
		}))
	}
	require.NoError(t, repo.SetHumanGate(ctx, released, false))
	// Released, then flipped back on by a raw PATCH: the old marker source is gone.
	require.NoError(t, repo.SetHumanGate(ctx, rearmed, false))
	got, err := repo.GetByID(ctx, rearmed)
	require.NoError(t, err)
	got.HumanGate = true
	require.NoError(t, repo.Update(ctx, got))

	// Walk it one row per page, as the reconciliation walks it: every page is reached.
	seen := map[uuid.UUID]bool{}
	after := uuid.Nil
	for {
		ids, err := repo.ListMarkerArmedHardGateIDs(ctx, after, 1)
		require.NoError(t, err)
		require.LessOrEqual(t, len(ids), 1)
		if len(ids) == 0 {
			break
		}
		require.NotContains(t, seen, ids[0], "keyset pages never repeat a row")
		seen[ids[0]] = true
		after = ids[0]
	}
	assert.True(t, seen[marker])
	assert.NotContains(t, seen, api, "an API arm is never reclassified")
	assert.NotContains(t, seen, soft)
	assert.NotContains(t, seen, released)
	assert.NotContains(t, seen, rearmed, "a raw PATCH arm has no recorded source")
}

// TestTaskRepo_ReclassifyHumanGateSoft moves a live hard gate to soft with the ask's own
// default in one statement, only while the recorded arm source is a marker and author and
// reason are the ones the caller checked; never an API arm, an unrecorded source, a soft
// gate or a released one.
func TestTaskRepo_ReclassifyHumanGateSoft(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()
	marker := domain.ArmHumanGateSourceMarker
	// The marker comment the arms below come from; the reclassification names it.
	ask := uuid.New()

	hard := createTestTaskForComments(t, repo, proj.ID, status.ID)
	released := createTestTaskForComments(t, repo, proj.ID, status.ID)
	api := createTestTaskForComments(t, repo, proj.ID, status.ID)
	unrecorded := createTestTaskForComments(t, repo, proj.ID, status.ID)
	author := uuid.New()
	for id, source := range map[uuid.UUID]domain.ArmHumanGateSource{hard: marker, released: marker, api: domain.ArmHumanGateSourceAPI, unrecorded: marker} {
		require.NoError(t, repo.ArmHumanGate(ctx, domain.ArmHumanGateInput{
			TaskID: id, Author: author, AuthorType: domain.ActorTypeAgent, Reason: "why",
			RecommendedDefault: "a context default", Class: domain.HumanGateClassHard, Source: source, SourceCommentID: ask,
		}))
	}
	require.NoError(t, repo.SetHumanGate(ctx, released, false))

	// An API arm with the very author and reason a marker would have: never.
	_, ok, err := repo.ReclassifyHumanGateSoft(ctx, api, author, ask, "why", "x")
	require.NoError(t, err)
	assert.False(t, ok, "API arm")

	// No recorded source, author and reason intact: a gate armed before the column
	// existed. Nothing proves a marker armed it, so it stays hard.
	_, err = db.ExecContext(ctx, `UPDATE tasks SET human_gate_source = NULL WHERE id = $1`, unrecorded)
	require.NoError(t, err)
	_, ok, err = repo.ReclassifyHumanGateSoft(ctx, unrecorded, author, ask, "why", "x")
	require.NoError(t, err)
	assert.False(t, ok, "unrecorded source")

	// A re-arm between the caller's read and the UPDATE: another author or reason, or a
	// newer re-ask by the same author with the same reason (another marker comment).
	_, ok, err = repo.ReclassifyHumanGateSoft(ctx, hard, author, uuid.New(), "why", "x")
	require.NoError(t, err)
	assert.False(t, ok, "another marker comment")
	_, ok, err = repo.ReclassifyHumanGateSoft(ctx, hard, uuid.New(), ask, "why", "x")
	require.NoError(t, err)
	assert.False(t, ok, "another author")
	_, ok, err = repo.ReclassifyHumanGateSoft(ctx, hard, author, ask, "another reason", "x")
	require.NoError(t, err)
	assert.False(t, ok, "another reason")

	_, ok, err = repo.ReclassifyHumanGateSoft(ctx, hard, author, ask, "why", "the ask's own default")
	require.NoError(t, err)
	assert.True(t, ok)
	got, err := repo.GetByID(ctx, hard)
	require.NoError(t, err)
	assert.Equal(t, domain.HumanGateClassSoft, got.HumanGateClass)
	require.NotNil(t, got.RecommendedDefault)
	assert.Equal(t, "the ask's own default", *got.RecommendedDefault)

	_, ok, err = repo.ReclassifyHumanGateSoft(ctx, hard, author, ask, "why", "again")
	require.NoError(t, err)
	assert.False(t, ok, "already soft")
	_, ok, err = repo.ReclassifyHumanGateSoft(ctx, released, author, ask, "why", "x")
	require.NoError(t, err)
	assert.False(t, ok, "gate released")

	// Armed with no default, so no deadline: the reclassification gives it armed_at +
	// the default window, or the default-on-timeout sweep would never reach it.
	nodef := createTestTaskForComments(t, repo, proj.ID, status.ID)
	require.NoError(t, repo.ArmHumanGate(ctx, domain.ArmHumanGateInput{
		TaskID: nodef, Author: author, AuthorType: domain.ActorTypeAgent, Reason: "why",
		Class: domain.HumanGateClassHard, Source: marker, SourceCommentID: ask,
	}))
	before, err := repo.GetByID(ctx, nodef)
	require.NoError(t, err)
	require.Nil(t, before.GateDeadline)
	require.NotNil(t, before.HumanGateArmedAt)
	deadline, ok, err := repo.ReclassifyHumanGateSoft(ctx, nodef, author, ask, "why", "the inline default")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, deadline)
	assert.WithinDuration(t, before.HumanGateArmedAt.Add(DefaultGateTimeoutHours*time.Hour), *deadline, time.Second)
	after, err := repo.GetByID(ctx, nodef)
	require.NoError(t, err)
	require.NotNil(t, after.GateDeadline)
	assert.WithinDuration(t, *deadline, *after.GateDeadline, time.Millisecond)

	// An existing deadline is kept.
	keep := createTestTaskForComments(t, repo, proj.ID, status.ID)
	fixed := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	require.NoError(t, repo.ArmHumanGate(ctx, domain.ArmHumanGateInput{
		TaskID: keep, Author: author, AuthorType: domain.ActorTypeAgent, Reason: "why",
		RecommendedDefault: "x", Deadline: &fixed, Class: domain.HumanGateClassHard, Source: marker, SourceCommentID: ask,
	}))
	deadline, ok, err = repo.ReclassifyHumanGateSoft(ctx, keep, author, ask, "why", "y")
	require.NoError(t, err)
	require.True(t, ok)
	assert.WithinDuration(t, fixed, *deadline, time.Second)
}
