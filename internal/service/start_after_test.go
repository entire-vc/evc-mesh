package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// The start_after validator on the service paths every transport goes
// through (REST, and the MCP server which calls REST): what an agent may set,
// what it may not, and that a refusal writes nothing.

func goodEvidence() *domain.StartAfterEvidence {
	return &domain.StartAfterEvidence{
		EventRef: "nightly-export",
		Source:   "https://ci.example/schedules/nightly",
		Why:      "the nightly export publishes its report at this time",
	}
}

func startAfterEnv(t *testing.T) (*taskService, *MockTaskRepository, uuid.UUID, *domain.Task) {
	t.Helper()
	svc, taskRepo, _, _, projRepo := setupTaskServiceForDelegation()
	projID := uuid.New()
	projRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: uuid.New()}
	statusID := uuid.New()
	svc.statusRepo.(*MockTaskStatusRepository).items[statusID] = &domain.TaskStatus{
		ID: statusID, ProjectID: projID, Slug: "todo", Category: domain.StatusCategoryTodo, IsDefault: true,
	}
	existingAt := frozenTime.Add(24 * time.Hour)
	legacy := domain.StartAfterLegacyUnknown
	task := &domain.Task{ID: uuid.New(), ProjectID: projID, Title: "T", StartAfter: &existingAt, StartAfterOrigin: &legacy}
	taskRepo.items[task.ID] = task
	return svc, taskRepo, projID, task
}

func agentCtx() context.Context {
	return actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
}

func userCtx() context.Context {
	return actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
}

func patch(task *domain.Task, w *domain.StartAfterWrite) *domain.Task {
	return &domain.Task{ID: task.ID, ProjectID: task.ProjectID, Title: task.Title, StartAfter: task.StartAfter, StartAfterWrite: w}
}

func TestStartAfter_AgentUpdateRefusals(t *testing.T) {
	future := frozenTime.Add(72 * time.Hour).Format(time.RFC3339)
	cases := []struct {
		name string
		w    domain.StartAfterWrite
		code string
	}{
		{"no evidence", domain.StartAfterWrite{Raw: future}, domain.CodeMissingEvidence},
		{"naive timestamp", domain.StartAfterWrite{Raw: "2025-06-20T06:00:00", Evidence: goodEvidence()}, domain.CodeNaiveTime},
		{"bare date", domain.StartAfterWrite{Raw: "2025-06-20", Evidence: goodEvidence()}, domain.CodeNaiveTime},
		{"relative 6h", domain.StartAfterWrite{Raw: "6h", Evidence: goodEvidence()}, domain.CodeRelativeTime},
		{"relative words", domain.StartAfterWrite{Raw: "in 2 days", Evidence: goodEvidence()}, domain.CodeRelativeTime},
		{"forged human origin", domain.StartAfterWrite{Raw: future, Origin: domain.StartAfterHumanExplicit, Evidence: goodEvidence()}, domain.CodeOriginForbidden},
		{"forged legacy origin", domain.StartAfterWrite{Raw: future, Origin: domain.StartAfterLegacyUnknown, Evidence: goodEvidence()}, domain.CodeOriginForbidden},
		{"generic retry", domain.StartAfterWrite{Raw: future, Evidence: &domain.StartAfterEvidence{
			EventRef: "retry-6h", Source: "https://ci.example/x", Why: "check again whether it is done"}}, domain.CodeGenericRetry},
		{"card is not a source", domain.StartAfterWrite{Raw: future, Evidence: &domain.StartAfterEvidence{
			EventRef: "release", Source: "card:00000000", Why: "the release happens at this time"}}, domain.CodeMissingEvidence},
		{"past date", domain.StartAfterWrite{Raw: frozenTime.Add(-time.Hour).Format(time.RFC3339), Evidence: goodEvidence()}, domain.CodeInvalidTarget},
		{"unconditional null", domain.StartAfterWrite{Null: true}, domain.CodeClearNeedsCompare},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, _, task := startAfterEnv(t)
			before := *task.StartAfter
			w := tc.w
			err := svc.Update(agentCtx(), patch(task, &w))
			assert.Equal(t, tc.code, outcomeCode(t, err))
			stored := repo.items[task.ID]
			assert.Same(t, task, stored, "a refused write reached the repository")
			assert.True(t, before.Equal(*stored.StartAfter), "the stored date changed")
		})
	}
}

func TestStartAfter_AgentSetWithEvidence(t *testing.T) {
	svc, repo, _, task := startAfterEnv(t)
	err := svc.Update(agentCtx(), patch(task, &domain.StartAfterWrite{Raw: "2025-06-20T09:00:00+03:00", Evidence: goodEvidence()}))
	require.NoError(t, err)
	stored := repo.items[task.ID]
	require.NotNil(t, stored.StartAfter)
	assert.Equal(t, "2025-06-20T06:00:00Z", stored.StartAfter.UTC().Format(time.RFC3339), "normalized to UTC")
	require.NotNil(t, stored.StartAfterOrigin)
	assert.Equal(t, domain.StartAfterAgentEvidence, *stored.StartAfterOrigin)
	require.NotNil(t, stored.StartAfterSet, "the repository gets the receipt to write")
	assert.Equal(t, *stored.StartAfterReceiptID, stored.StartAfterSet.ID)
	assert.Equal(t, "+03:00", *stored.StartAfterSet.SourceOffset, "the original offset is kept")
	assert.Equal(t, domain.TimeEvidenceUnverified, *stored.StartAfterSet.EvidenceStatus)
	assert.Equal(t, "nightly-export", *stored.StartAfterSet.EventRef)
}

func TestStartAfter_SameDateIsNoOp(t *testing.T) {
	svc, repo, _, task := startAfterEnv(t)
	raw := task.StartAfter.Format(time.RFC3339)
	require.NoError(t, svc.Update(agentCtx(), patch(task, &domain.StartAfterWrite{Raw: raw})),
		"re-sending the stored date needs no evidence")
	stored := repo.items[task.ID]
	assert.Nil(t, stored.StartAfterSet, "no new schedule receipt")
	assert.Equal(t, domain.StartAfterLegacyUnknown, *stored.StartAfterOrigin, "the origin is kept")
}

func TestStartAfter_OmittedKeepsProvenance(t *testing.T) {
	svc, repo, _, task := startAfterEnv(t)
	require.NoError(t, svc.Update(agentCtx(), &domain.Task{ID: task.ID, ProjectID: task.ProjectID, Title: "renamed", StartAfter: task.StartAfter}))
	stored := repo.items[task.ID]
	assert.Equal(t, domain.StartAfterLegacyUnknown, *stored.StartAfterOrigin)
}

// A caller that sets StartAfter on the struct without a write intent (an
// internal path) is held to the agent rules too: there is no side door.
func TestStartAfter_BareStructWriteIsValidated(t *testing.T) {
	svc, repo, _, task := startAfterEnv(t)
	at := frozenTime.Add(96 * time.Hour)
	err := svc.Update(agentCtx(), &domain.Task{ID: task.ID, ProjectID: task.ProjectID, Title: "T", StartAfter: &at})
	assert.Equal(t, domain.CodeMissingEvidence, outcomeCode(t, err))
	assert.Same(t, task, repo.items[task.ID])
}

func TestStartAfter_HumanDateAndClear(t *testing.T) {
	svc, repo, _, task := startAfterEnv(t)
	require.NoError(t, svc.Update(userCtx(), patch(task, &domain.StartAfterWrite{Raw: "2025-07-01"})),
		"a person may give a bare date and no evidence")
	stored := repo.items[task.ID]
	assert.Equal(t, "2025-07-01", stored.StartAfter.Format("2006-01-02"))
	assert.Equal(t, domain.StartAfterHumanExplicit, *stored.StartAfterOrigin)
	require.NotNil(t, stored.StartAfterSet)
	assert.Nil(t, stored.StartAfterSet.EventRef)

	require.NoError(t, svc.Update(userCtx(), patch(stored, &domain.StartAfterWrite{Null: true})))
	cleared := repo.items[task.ID]
	require.NotNil(t, cleared.StartAfterSet, "a person's clear leaves a receipt")
	assert.Equal(t, "clear", cleared.StartAfterSet.Op)
	assert.Equal(t, domain.StartAfterHumanExplicit, cleared.StartAfterSet.Origin)
	assert.Equal(t, *stored.StartAfterReceiptID, *cleared.StartAfterSet.ClearedReceiptID)
	assert.True(t, stored.StartAfter.Equal(cleared.StartAfterSet.StartAfter), "the receipt names the removed date")
	assert.NotNil(t, cleared.StartAfterSet.IdempotencyKey)
	assert.Nil(t, cleared.StartAfter)
	assert.Nil(t, cleared.StartAfterOrigin)
	assert.Nil(t, cleared.StartAfterReceiptID)
}

func TestStartAfter_CreatePathsRefuseWithoutEvidence(t *testing.T) {
	svc, repo, projID, parent := startAfterEnv(t)
	future := frozenTime.Add(72 * time.Hour).Format(time.RFC3339)

	created := &domain.Task{ID: uuid.New(), ProjectID: projID, Title: "new", StartAfterWrite: &domain.StartAfterWrite{Raw: future}}
	err := svc.Create(agentCtx(), created)
	assert.Equal(t, domain.CodeMissingEvidence, outcomeCode(t, err))
	assert.NotContains(t, repo.items, created.ID, "create_task wrote a card")

	before := len(repo.items)
	_, err = svc.CreateSubtask(agentCtx(), parent.ID, CreateSubtaskInput{
		Title: "child", Priority: domain.PriorityMedium, StartAfterWrite: &domain.StartAfterWrite{Raw: future},
	})
	assert.Equal(t, domain.CodeMissingEvidence, outcomeCode(t, err))
	assert.Len(t, repo.items, before, "create_subtask wrote a card")

	ok := &domain.Task{ID: uuid.New(), ProjectID: projID, Title: "new",
		StartAfterWrite: &domain.StartAfterWrite{Raw: future, Evidence: goodEvidence()}}
	require.NoError(t, svc.Create(agentCtx(), ok))
	assert.Equal(t, domain.StartAfterAgentEvidence, *repo.items[ok.ID].StartAfterOrigin)
}
