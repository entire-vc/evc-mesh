//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func addTestStatus(t *testing.T, db *sqlx.DB, projectID uuid.UUID, slug string, cat domain.StatusCategory) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default, auto_transition) VALUES ($1,$2,$3,$4,'#000000',5,$5,false,$6)`,
		id, projectID, slug, slug, cat, json.RawMessage(`{}`))
	require.NoError(t, err)
	return id
}

func addTestTask(t *testing.T, repo *TaskRepo, projID, statusID uuid.UUID, title string) uuid.UUID {
	t.Helper()
	task := &domain.Task{
		ID: uuid.New(), ProjectID: projID, StatusID: statusID, Title: title,
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CustomFields: json.RawMessage(`{}`), CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond), UpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, repo.Create(context.Background(), task, nil))
	return task.ID
}

func TestTaskRepo_ListTriageQueue(t *testing.T) {
	db := testDB(t)
	ws, proj, _ := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	inProgress := addTestStatus(t, db, proj.ID, "wip", domain.StatusCategoryInProgress)
	triage := addTestStatus(t, db, proj.ID, "triage", domain.StatusCategoryTriage)
	done := addTestStatus(t, db, proj.ID, "done", domain.StatusCategoryDone)

	autoHard := addTestTask(t, repo, proj.ID, inProgress, "auto in_progress hard gate")
	plainTriage := addTestTask(t, repo, proj.ID, triage, "triage no gate")
	released := addTestTask(t, repo, proj.ID, inProgress, "released gate")
	doneGated := addTestTask(t, repo, proj.ID, done, "done with gate")
	dup := addTestTask(t, repo, proj.ID, triage, "triage + gate")
	softGate := addTestTask(t, repo, proj.ID, inProgress, "soft gate")
	_ = addTestTask(t, repo, proj.ID, inProgress, "no gate, in progress")

	require.NoError(t, repo.SetHumanGate(ctx, autoHard, true))
	require.NoError(t, repo.SetHumanGate(ctx, released, true))
	require.NoError(t, repo.SetHumanGate(ctx, released, false))
	require.NoError(t, repo.SetHumanGate(ctx, doneGated, true))
	require.NoError(t, repo.SetHumanGate(ctx, dup, true))
	require.NoError(t, repo.SetHumanGate(ctx, softGate, true))
	require.NoError(t, repo.SetHumanGateClass(ctx, softGate, domain.HumanGateClassSoft))

	page, err := repo.ListTriageQueue(ctx, ws.ID, pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)

	var ids []uuid.UUID
	for _, tk := range page.Items {
		ids = append(ids, tk.ID)
	}
	assert.Contains(t, ids, autoHard, "auto in_progress with hard gate must be listed")
	assert.Contains(t, ids, plainTriage, "triage status without gate must be listed")
	assert.Contains(t, ids, softGate)
	assert.NotContains(t, ids, released, "released gate must not be listed")
	assert.NotContains(t, ids, doneGated, "done card with gate must not be listed")
	n := 0
	for _, id := range ids {
		if id == dup {
			n++
		}
	}
	assert.Equal(t, 1, n, "triage+gate card must appear exactly once")
	assert.Equal(t, 4, page.TotalCount)
	assert.Len(t, page.Items, 4)

	// gated cards must carry their gate state back to the caller
	for _, tk := range page.Items {
		if tk.ID == autoHard {
			assert.True(t, tk.HumanGate, "returned gated card must have human_gate=true")
			assert.Equal(t, domain.HumanGateClassHard, tk.HumanGateClass)
			assert.NotNil(t, tk.HumanGateArmedAt)
		}
	}

	// hard gates sort before soft and before ungated triage cards
	soft := -1
	for i, id := range ids {
		if id == softGate {
			soft = i
		}
	}
	for i, id := range ids {
		if id == autoHard || id == dup {
			assert.Less(t, i, soft, "hard gate must precede soft gate")
		}
	}

	// pagination keeps total
	p2, err := repo.ListTriageQueue(ctx, ws.ID, pagination.Params{Page: 2, PageSize: 3})
	require.NoError(t, err)
	assert.Equal(t, 4, p2.TotalCount)
	assert.Len(t, p2.Items, 1)
}
