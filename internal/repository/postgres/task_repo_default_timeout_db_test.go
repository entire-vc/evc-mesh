//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// ArmHumanGate's auto-default gate_deadline logic (task #060ccaae) — real Postgres,
// so the interval arithmetic and the CASE's own conditional are exercised for real
// rather than merely pinned as text (that half lives in the sqlmock sibling file).
// ---------------------------------------------------------------------------

func armGate(t *testing.T, repo *TaskRepo, taskID, author uuid.UUID, class domain.HumanGateClass, recommendedDefault string, deadline *time.Time) {
	t.Helper()
	require.NoError(t, repo.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: taskID, Author: author, AuthorType: domain.ActorTypeAgent,
		Reason: "why", RecommendedDefault: recommendedDefault, Deadline: deadline, Class: class,
	}))
}

func TestTaskRepo_ArmHumanGate_AutoDefaultsDeadline_SoftWithDefault(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)

	before := time.Now().UTC()
	armGate(t, taskRepo, taskID, uuid.New(), domain.HumanGateClassSoft, "merge as-is", nil)
	after := time.Now().UTC()

	got, err := taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, got.GateDeadline, "a soft gate with a stated default must get an auto-computed deadline")
	// A fresh TaskRepo (SetDefaultGateTimeoutHours never called) must use the built-in
	// default — DefaultGateTimeoutHours (24h, Pavel decision 2026-09-06, task #060ccaae;
	// the original spec said 72h, changed before ship) — not zero hours.
	assert.False(t, got.GateDeadline.Before(before.Add(DefaultGateTimeoutHours*time.Hour-time.Second)))
	assert.False(t, got.GateDeadline.After(after.Add(DefaultGateTimeoutHours*time.Hour+time.Second)))
}

// TestTaskRepo_ArmHumanGate_AutoDefaultsDeadline_HonorsConfiguredHours is the real-Postgres
// half of the sqlmock arg-binding proof: sqlmock can confirm the RIGHT NUMBER was bound,
// but only a real database can confirm make_interval(hours => $8) actually computes the
// right INTERVAL — a sqlmock-only test would be blind to e.g. hours being silently
// interpreted as minutes or days by a typo'd interval unit.
func TestTaskRepo_ArmHumanGate_AutoDefaultsDeadline_HonorsConfiguredHours(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	taskRepo.SetDefaultGateTimeoutHours(48)
	ctx := context.Background()
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)

	before := time.Now().UTC()
	armGate(t, taskRepo, taskID, uuid.New(), domain.HumanGateClassSoft, "merge as-is", nil)
	after := time.Now().UTC()

	got, err := taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, got.GateDeadline)
	assert.False(t, got.GateDeadline.Before(before.Add(48*time.Hour-time.Second)),
		"SetDefaultGateTimeoutHours(48) must actually reach the computed deadline, not just the built-in 24h default")
	assert.False(t, got.GateDeadline.After(after.Add(48*time.Hour+time.Second)))
}

func TestTaskRepo_ArmHumanGate_AutoDefaultsDeadline_HardNeverGetsOne(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)

	armGate(t, taskRepo, taskID, uuid.New(), domain.HumanGateClassHard, "merge as-is", nil)

	got, err := taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	assert.Nil(t, got.GateDeadline, "a hard gate must never get an auto-computed deadline — no code path may release it on a clock")
}

func TestTaskRepo_ArmHumanGate_AutoDefaultsDeadline_NoDefaultMeansNoDeadline(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)

	armGate(t, taskRepo, taskID, uuid.New(), domain.HumanGateClassSoft, "", nil)

	got, err := taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	assert.Nil(t, got.GateDeadline, "a soft gate with no stated default has nothing for the sweep to apply — must stay NULL")
}

func TestTaskRepo_ArmHumanGate_ExplicitDeadlineAlwaysWins(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)

	explicit := time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)
	armGate(t, taskRepo, taskID, uuid.New(), domain.HumanGateClassSoft, "merge as-is", &explicit)

	got, err := taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, got.GateDeadline)
	assert.True(t, explicit.Equal(*got.GateDeadline), "an explicitly passed deadline must be used verbatim, not overridden by the auto-default")
}

func TestTaskRepo_ArmHumanGate_ReArmComputesFromOriginalArmedAt(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	author := uuid.New()

	armGate(t, taskRepo, taskID, author, domain.HumanGateClassSoft, "merge as-is", nil)
	first, err := taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, first.GateDeadline)

	// Re-arm without a new explicit deadline: the auto-default must be recomputed from
	// the SAME original armed_at (unchanged by re-arming), not a fresh NOW()+window — a
	// re-arm that silently pushed the deadline out would let a repeat ping keep a soft
	// gate alive past its stated window, the same latch shape #84ab54fd already fixed
	// for armed_at itself.
	armGate(t, taskRepo, taskID, author, domain.HumanGateClassSoft, "merge as-is, still", nil)
	second, err := taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, second.GateDeadline)
	assert.True(t, first.GateDeadline.Equal(*second.GateDeadline),
		"re-arming must not push the deadline out from under an already-running clock")
}

// ---------------------------------------------------------------------------
// FindExpiredDefaultGates / FindSoftTimedOutGates handoff (task #060ccaae)
// ---------------------------------------------------------------------------

func backdateDeadline(t *testing.T, db *sqlx.DB, taskID uuid.UUID, when time.Time) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `UPDATE tasks SET gate_deadline = $2 WHERE id = $1`, taskID, when)
	require.NoError(t, err)
}

func TestTaskRepo_FindExpiredDefaultGates_FindsExpiredSoftGateWithDefault(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	author := uuid.New()

	armGate(t, taskRepo, taskID, author, domain.HumanGateClassSoft, "merge as-is", nil)
	past := time.Now().UTC().Add(-time.Hour)
	backdateDeadline(t, db, taskID, past)

	candidates, err := taskRepo.FindExpiredDefaultGates(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.Equal(t, taskID, candidates[0].TaskID)
	assert.Equal(t, "merge as-is", candidates[0].RecommendedDefault)
	assert.Equal(t, author, candidates[0].GateAuthor)
}

// TestTaskRepo_FindExpiredDefaultGates_ExcludesHardStructurally mirrors
// TestTaskRepo_FindSoftTimedOutGates_ExcludesHardStructurally's method: construct a hard
// gate eligible by every OTHER criterion (armed, real default, decades-expired deadline)
// and prove the class predicate — not the deadline comparison — is what excludes it, by
// querying with a decades-future `now`.
func TestTaskRepo_FindExpiredDefaultGates_ExcludesHardStructurally(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()

	hardID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	softID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	armGate(t, taskRepo, hardID, uuid.New(), domain.HumanGateClassHard, "merge as-is", nil)
	armGate(t, taskRepo, softID, uuid.New(), domain.HumanGateClassSoft, "merge as-is", nil)

	// Hard gates never auto-default a deadline — force one in directly so this row is
	// eligible by every predicate except class, same technique the sibling test uses.
	ancient := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	backdateDeadline(t, db, hardID, ancient)
	backdateDeadline(t, db, softID, ancient)

	farFuture := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	candidates, err := taskRepo.FindExpiredDefaultGates(ctx, farFuture)
	require.NoError(t, err)

	ids := make(map[uuid.UUID]bool, len(candidates))
	for _, c := range candidates {
		ids[c.TaskID] = true
	}
	assert.True(t, ids[softID], "soft gate with a real default, expired, must be found")
	assert.False(t, ids[hardID], "hard gate must be structurally unreachable regardless of how it got a deadline")
}

func TestTaskRepo_FindExpiredDefaultGates_ExcludesNotYetDueAndReleased(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()

	notYetDueID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	armGate(t, taskRepo, notYetDueID, uuid.New(), domain.HumanGateClassSoft, "merge as-is", nil)
	// Freshly armed — auto-deadline is ~24h out (the default window), nowhere near due.

	releasedID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	armGate(t, taskRepo, releasedID, uuid.New(), domain.HumanGateClassSoft, "merge as-is", nil)
	backdateDeadline(t, db, releasedID, time.Now().UTC().Add(-time.Hour))
	require.NoError(t, taskRepo.SetHumanGate(ctx, releasedID, false)) // released before the sweep ever ran

	dueID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	armGate(t, taskRepo, dueID, uuid.New(), domain.HumanGateClassSoft, "merge as-is", nil)
	backdateDeadline(t, db, dueID, time.Now().UTC().Add(-time.Hour))

	candidates, err := taskRepo.FindExpiredDefaultGates(ctx, time.Now().UTC())
	require.NoError(t, err)

	ids := make(map[uuid.UUID]bool, len(candidates))
	for _, c := range candidates {
		ids[c.TaskID] = true
	}
	assert.True(t, ids[dueID], "expired, still live, real default → due")
	assert.False(t, ids[notYetDueID], "deadline not yet reached → not due")
	assert.False(t, ids[releasedID], "human_gate already false → must not be re-swept")
}

// TestTaskRepo_FindSoftTimedOutGates_ExcludesGateWithDeadline is the real-Postgres proof
// of the handoff fix: a soft gate that names a real recommended_default (and therefore
// has a gate_deadline) must be invisible to the OLDER 24h-window sweep even though its
// armed_at cutoff would otherwise select it — that gate belongs to
// FindExpiredDefaultGates exclusively. See both methods' docs in task_repo.go.
func TestTaskRepo_FindSoftTimedOutGates_ExcludesGateWithDeadline(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()

	withDeadlineID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	armGate(t, taskRepo, withDeadlineID, uuid.New(), domain.HumanGateClassSoft, "merge as-is", nil)

	noDeadlineID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	armGate(t, taskRepo, noDeadlineID, uuid.New(), domain.HumanGateClassSoft, "", nil) // no default → no deadline

	ancient := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.ExecContext(ctx, `UPDATE tasks SET human_gate_armed_at = $2 WHERE id IN ($1, $3)`,
		withDeadlineID, ancient, noDeadlineID)
	require.NoError(t, err)

	farFutureCutoff := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	candidates, err := taskRepo.FindSoftTimedOutGates(ctx, farFutureCutoff)
	require.NoError(t, err)

	ids := make(map[uuid.UUID]bool, len(candidates))
	for _, c := range candidates {
		ids[c.TaskID] = true
	}
	assert.True(t, ids[noDeadlineID], "a soft gate with no stated default still gets the blunt 24h-style release")
	assert.False(t, ids[withDeadlineID], "a soft gate with a real deadline must be owned exclusively by FindExpiredDefaultGates")
}
