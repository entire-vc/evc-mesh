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

// provPtr/chanPtr live in human_gate_decision_repo_sqlmock_test.go (no build
// tag, so both this file and the sqlmock tests can use them).

// TestHumanGateDecisionRepo_AppendOnly_RejectsUpdate proves the DB-level
// immutability guarantee (migration 20260821001) independently of any Go
// code ever calling UPDATE — the same "test against real Postgres, not just
// our own code path" reasoning as project_integration_encryption_db_test.go.
func TestHumanGateDecisionRepo_AppendOnly_RejectsUpdate(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	pavel := createTestUser(t, db, "pavel")

	repo := NewHumanGateDecisionRepo(db)
	d := &domain.HumanGateDecision{
		TaskID:       taskID,
		CanonicalKey: strPtr("canonical-decision-2026-08-21-append-only-test"),
		DecidedBy:    pavel,
		Provenance:   provPtr(domain.HumanGateProvenanceDirect),
		Channel:      chanPtr(domain.HumanGateChannelMesh),
	}
	require.NoError(t, repo.Create(context.Background(), d))

	_, err := db.ExecContext(context.Background(),
		`UPDATE human_gate_decisions SET quote = 'rewritten' WHERE id = $1`, d.ID)
	require.Error(t, err, "the ledger must reject any UPDATE — a revocation is a new row, never a rewrite")
	assert.Contains(t, err.Error(), "append-only")
}

// TestHumanGateDecisionRepo_FindLiveByRef_SkipsRevoked is the core query the
// repeat-prevention check (contract §6) depends on: a decision matching
// question_ref must stop being "live" the moment it is revoked, and a
// completely different question_ref on the same task must never match.
func TestHumanGateDecisionRepo_FindLiveByRef_SkipsRevoked(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	pavel := createTestUser(t, db, "pavel")

	repo := NewHumanGateDecisionRepo(db)
	ctx := context.Background()
	qRef := uuid.New()

	// Nothing recorded yet — no live match.
	got, err := repo.FindLiveByRef(ctx, taskID, &qRef, nil)
	require.NoError(t, err)
	assert.Nil(t, got)

	// Record a decision for this question_ref.
	d := &domain.HumanGateDecision{
		TaskID:      taskID,
		QuestionRef: &qRef,
		DecidedBy:   pavel,
		Provenance:  provPtr(domain.HumanGateProvenanceDirect),
		Channel:     chanPtr(domain.HumanGateChannelMesh),
	}
	require.NoError(t, repo.Create(ctx, d))

	got, err = repo.FindLiveByRef(ctx, taskID, &qRef, nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, d.ID, got.ID)
	assert.Nil(t, got.RevokedAt)

	// A DIFFERENT question_ref on the same task must not match (reverse
	// control — this is what proves the repeat-check isn't vacuously true).
	otherRef := uuid.New()
	got, err = repo.FindLiveByRef(ctx, taskID, &otherRef, nil)
	require.NoError(t, err)
	assert.Nil(t, got, "a different question_ref must never match another question's decision")

	// Revoke the decision — it must stop being live.
	revocation := &domain.HumanGateDecision{
		TaskID:        taskID,
		DecidedBy:     pavel,
		RevokesID:     &d.ID,
		RevokedReason: strPtr("dispute — Pavel says he never confirmed this"),
	}
	require.NoError(t, repo.Create(ctx, revocation))

	got, err = repo.FindLiveByRef(ctx, taskID, &qRef, nil)
	require.NoError(t, err)
	assert.Nil(t, got, "a revoked decision must not be found as live")

	// GetByID on the original decision must now show it as revoked.
	fetched, err := repo.GetByID(ctx, d.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	require.NotNil(t, fetched.RevokedAt)
	require.NotNil(t, fetched.RevokedReason)
	assert.Equal(t, "dispute — Pavel says he never confirmed this", *fetched.RevokedReason)
}

// TestHumanGateDecisionRepo_FindLiveByRef_CanonicalKeyMatch proves the
// canonical_key branch of the OR independently of question_ref.
func TestHumanGateDecisionRepo_FindLiveByRef_CanonicalKeyMatch(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	pavel := createTestUser(t, db, "pavel")

	repo := NewHumanGateDecisionRepo(db)
	ctx := context.Background()
	key := "canonical-decision-2026-08-21-tr-free-tier"

	d := &domain.HumanGateDecision{
		TaskID:       taskID,
		CanonicalKey: &key,
		DecidedBy:    pavel,
		Provenance:   provPtr(domain.HumanGateProvenanceBridged),
		Channel:      chanPtr(domain.HumanGateChannelTelegram),
		Quote:        strPtr("да! но для моего аккаунта отдельный тариф без лимита!"),
	}
	require.NoError(t, repo.Create(ctx, d))

	got, err := repo.FindLiveByRef(ctx, taskID, nil, &key)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, d.ID, got.ID)

	otherKey := "canonical-decision-2026-08-22-something-else"
	got, err = repo.FindLiveByRef(ctx, taskID, nil, &otherKey)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestHumanGateDecisionRepo_QuoteRequiredForBridgedAndAttested proves the DB
// CHECK constraint independently — a Go-level validation bug must not be the
// only thing standing between a bridged/attested row and a missing quote.
func TestHumanGateDecisionRepo_QuoteRequiredForBridgedAndAttested(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	pavel := createTestUser(t, db, "pavel")
	repo := NewHumanGateDecisionRepo(db)
	ctx := context.Background()

	// bridged, no quote — rejected.
	ref1 := uuid.New()
	err := repo.Create(ctx, &domain.HumanGateDecision{
		TaskID: taskID, QuestionRef: &ref1, DecidedBy: pavel,
		Provenance: provPtr(domain.HumanGateProvenanceBridged), Channel: chanPtr(domain.HumanGateChannelTelegram),
	})
	require.Error(t, err)

	// direct, no quote — allowed (Pavel's own Mesh comment IS the record).
	ref2 := uuid.New()
	err = repo.Create(ctx, &domain.HumanGateDecision{
		TaskID: taskID, QuestionRef: &ref2, DecidedBy: pavel,
		Provenance: provPtr(domain.HumanGateProvenanceDirect), Channel: chanPtr(domain.HumanGateChannelMesh),
	})
	require.NoError(t, err)
}

// TestHumanGateDecisionRepo_RevocationRequiresReason proves the DB refuses a
// revocation row with no reason — a dispute must be named, not silent.
func TestHumanGateDecisionRepo_RevocationRequiresReason(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	pavel := createTestUser(t, db, "pavel")
	repo := NewHumanGateDecisionRepo(db)
	ctx := context.Background()

	ref := uuid.New()
	d := &domain.HumanGateDecision{
		TaskID: taskID, QuestionRef: &ref, DecidedBy: pavel,
		Provenance: provPtr(domain.HumanGateProvenanceDirect), Channel: chanPtr(domain.HumanGateChannelMesh),
	}
	require.NoError(t, repo.Create(ctx, d))

	err := repo.Create(ctx, &domain.HumanGateDecision{
		TaskID: taskID, DecidedBy: pavel, RevokesID: &d.ID,
	})
	require.Error(t, err, "a revocation with no reason must be rejected")
}

// TestHumanGateDecisionRepo_ListByTask_NewestFirst is a light sanity check on
// ordering, since every consumer of this list (a task detail view) assumes it.
func TestHumanGateDecisionRepo_ListByTask_NewestFirst(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	pavel := createTestUser(t, db, "pavel")
	repo := NewHumanGateDecisionRepo(db)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	ref1, ref2 := uuid.New(), uuid.New()
	first := &domain.HumanGateDecision{
		TaskID: taskID, QuestionRef: &ref1, DecidedBy: pavel, CreatedAt: now,
		Provenance: provPtr(domain.HumanGateProvenanceDirect), Channel: chanPtr(domain.HumanGateChannelMesh),
	}
	require.NoError(t, repo.Create(ctx, first))
	second := &domain.HumanGateDecision{
		TaskID: taskID, QuestionRef: &ref2, DecidedBy: pavel, CreatedAt: now.Add(time.Second),
		Provenance: provPtr(domain.HumanGateProvenanceDirect), Channel: chanPtr(domain.HumanGateChannelMesh),
	}
	require.NoError(t, repo.Create(ctx, second))

	list, err := repo.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, second.ID, list[0].ID)
	assert.Equal(t, first.ID, list[1].ID)
}
