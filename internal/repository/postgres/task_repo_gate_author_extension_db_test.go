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

// An extension actor (Keep) arms the gate on the card it opened: tasks and
// gate_predicate_log must store 'extension' as the author type (migration
// 20261027001), while an unknown type is still refused by the CHECK.
func TestTaskRepo_ArmHumanGate_ExtensionAuthor(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	ctx := context.Background()
	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)
	actor := uuid.New()

	require.NoError(t, taskRepo.ArmHumanGate(ctx, domain.ArmHumanGateInput{
		TaskID: taskID, Author: actor, AuthorType: domain.ActorTypeExtension,
		Reason: "rotation needs an owner", RecommendedDefault: "keep the old key", Class: domain.HumanGateClassHard,
	}))
	got, err := taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, got.GateAuthorType)
	assert.Equal(t, domain.ActorTypeExtension, *got.GateAuthorType)
	require.NotNil(t, got.GateAuthor)
	assert.Equal(t, actor, *got.GateAuthor)

	require.NoError(t, NewGatePredicateLogRepo(db).Record(ctx, &domain.GatePredicateLogEntry{
		ID: uuid.New(), TaskID: taskID, ActorID: actor, ActorType: domain.ActorTypeExtension,
		Outcome: domain.GatePredicateAllowed, Source: domain.ArmHumanGateSourceAPI, CreatedAt: time.Now().UTC(),
	}))

	_, err = db.ExecContext(ctx, `UPDATE tasks SET gate_author_type = 'robot' WHERE id = $1`, taskID)
	assert.Error(t, err, "the CHECK must still refuse an unknown author type")
}
