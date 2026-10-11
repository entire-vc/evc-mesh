package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// Task #8f191cf7 (rule 4, #0f3e944d twice): an executor's own «⏳ WAIT …» status
// line says "I keep the current state", yet its last paragraph routinely holds a
// negator ("resolved mapping paths", "не требуется"), and the withdrawal path read that as
// the author retracting its own Pavel-ask. The gate was lifted although the
// blocker was untouched.
const waitMarkerWithNegatorBody = "⏳ WAIT 20m: существующий human_gate остаётся; AC1 и живой цикл не приняты.\n\n" +
	"Ask прежний: дать readonly доступ к vault либо приложить отчёт цикла с resolved mapping paths."

func TestReleaseHumanGateOnWithdrawal_OwnWaitMarker_DoesNotRelease(t *testing.T) {
	env := setupTriageEnv(t, true)
	taskID := env.seedGatedTask(env.inProgressID)
	askerID := uuid.New()
	env.seedAgentBlockingComment(taskID, askerID)

	ctx := actorctx.WithActor(context.Background(), askerID, domain.ActorTypeAgent)
	require.NoError(t, env.svc.Create(ctx, &domain.Comment{
		TaskID: taskID, AuthorID: askerID, AuthorType: domain.ActorTypeAgent,
		Body: waitMarkerWithNegatorBody,
	}))

	assert.Empty(t, env.taskMover.humanGateCalls(), "an own ⏳ WAIT line is not a retraction of the ask")
}

func TestIsHoldMarkerComment(t *testing.T) {
	for body, want := range map[string]bool{
		"⏳ WAIT pipeline:g/r#1\nrest":         true,
		"  \n↻ NEXT: do the thing":            true,
		"⏳ WAIT decision:@garfield: GO?":      true,
		"Отзываю запрос: не нужен.":           false,
		"Итог.\n⏳ WAIT 20m: позже — в хвосте": false,
		"": false,
	} {
		assert.Equal(t, want, isHoldMarkerComment(body), body)
	}
}
