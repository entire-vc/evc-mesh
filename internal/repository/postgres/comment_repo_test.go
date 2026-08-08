//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// createTestTaskForComments inserts a task under a fresh workspace/project so
// comment-ordering tests don't share rows with other tests' threads.
func createTestTaskForComments(t *testing.T, taskRepo *TaskRepo, projID, statusID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	task := &domain.Task{
		ID:            uuid.New(),
		ProjectID:     projID,
		StatusID:      statusID,
		Title:         "Comment order test task",
		AssigneeType:  domain.AssigneeTypeUnassigned,
		Priority:      domain.PriorityMedium,
		CustomFields:  json.RawMessage(`{}`),
		CreatedBy:     uuid.New(),
		CreatedByType: domain.ActorTypeUser,
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	return task.ID
}

// createTestComment inserts a comment with an explicit created_at so ordering
// is deterministic regardless of how fast the test runs.
func createTestComment(t *testing.T, repo *CommentRepo, taskID uuid.UUID, body string, createdAt time.Time) uuid.UUID {
	t.Helper()
	c := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     taskID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeUser,
		Body:       body,
		IsInternal: false,
		CreatedAt:  createdAt,
		UpdatedAt:  createdAt,
	}
	require.NoError(t, repo.Create(context.Background(), c))
	return c.ID
}

// TestCommentRepo_ListByTask_SortDir is the regression + negative-control test
// for D3 (task 4222c17d): comment_repo.go used to hardcode `ORDER BY
// c.created_at ASC` regardless of the sort_dir the handler already correctly
// bound from the query string — a syntactically valid, silently ignored
// parameter (the exact "vacuous green probe" class named in the task).
func TestCommentRepo_ListByTask_SortDir(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	commentRepo := NewCommentRepo(db)
	ctx := context.Background()

	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)

	base := time.Now().UTC().Truncate(time.Microsecond)
	oldest := createTestComment(t, commentRepo, taskID, "oldest", base)
	middle := createTestComment(t, commentRepo, taskID, "middle", base.Add(time.Minute))
	newest := createTestComment(t, commentRepo, taskID, "newest", base.Add(2*time.Minute))

	t.Run("regression: unparameterized request stays created_at ASC (REST default must not move)", func(t *testing.T) {
		page, err := commentRepo.ListByTask(ctx, taskID, repository.CommentFilter{}, pagination.Params{})
		require.NoError(t, err)
		require.Equal(t, 3, page.TotalCount)
		require.Len(t, page.Items, 3)
		assert.Equal(t, oldest, page.Items[0].ID, "default order: oldest first")
		assert.Equal(t, middle, page.Items[1].ID)
		assert.Equal(t, newest, page.Items[2].ID, "default order: newest last")
		assert.False(t, page.HasMore)
	})

	t.Run("explicit sort_dir=asc matches the default (documents the default rather than merely matching it)", func(t *testing.T) {
		page, err := commentRepo.ListByTask(ctx, taskID, repository.CommentFilter{}, pagination.Params{SortDir: "asc"})
		require.NoError(t, err)
		require.Len(t, page.Items, 3)
		assert.Equal(t, oldest, page.Items[0].ID)
		assert.Equal(t, newest, page.Items[2].ID)
	})

	t.Run("sort_dir=desc is honored, not silently dropped", func(t *testing.T) {
		page, err := commentRepo.ListByTask(ctx, taskID, repository.CommentFilter{}, pagination.Params{SortDir: "desc"})
		require.NoError(t, err)
		require.Equal(t, 3, page.TotalCount)
		require.Len(t, page.Items, 3)
		assert.Equal(t, newest, page.Items[0].ID, "sort_dir=desc: newest first")
		assert.Equal(t, middle, page.Items[1].ID)
		assert.Equal(t, oldest, page.Items[2].ID, "sort_dir=desc: oldest last")
	})

	t.Run("sort_dir=desc with page_size honors has_more and total_count together", func(t *testing.T) {
		page, err := commentRepo.ListByTask(ctx, taskID, repository.CommentFilter{}, pagination.Params{SortDir: "desc", PageSize: 2})
		require.NoError(t, err)
		require.Equal(t, 3, page.TotalCount)
		require.Len(t, page.Items, 2)
		assert.True(t, page.HasMore)
		assert.Equal(t, newest, page.Items[0].ID)
		assert.Equal(t, middle, page.Items[1].ID, "page 1 of sort_dir=desc must NOT include the oldest comment")
	})
}

// TestCommentRepo_ListByTask_ManyComments_TailIsReachable is the negative
// control for the incident this task fixes: on a thread with more than
// DefaultPageSize comments, sort_dir=desc must be able to reach the true tail
// (the newest comment) — pre-fix, this request silently returned the SAME
// oldest-first page regardless of sort_dir, so this test would have failed
// against the unfixed repository with the newest comment absent from items.
func TestCommentRepo_ListByTask_ManyComments_TailIsReachable(t *testing.T) {
	db := testDB(t)
	_, proj, status := createTestProject(t, db)
	taskRepo := NewTaskRepo(db)
	commentRepo := NewCommentRepo(db)
	ctx := context.Background()

	taskID := createTestTaskForComments(t, taskRepo, proj.ID, status.ID)

	const total = pagination.DefaultPageSize + 5
	base := time.Now().UTC().Truncate(time.Microsecond)
	var newestID uuid.UUID
	for i := 0; i < total; i++ {
		id := createTestComment(t, commentRepo, taskID, "c", base.Add(time.Duration(i)*time.Second))
		if i == total-1 {
			newestID = id
		}
	}

	// Unparameterized (what a naive caller sends): oldest DefaultPageSize, tail unreachable.
	oldestPage, err := commentRepo.ListByTask(ctx, taskID, repository.CommentFilter{}, pagination.Params{})
	require.NoError(t, err)
	require.Equal(t, total, oldestPage.TotalCount)
	require.Len(t, oldestPage.Items, pagination.DefaultPageSize)
	assert.True(t, oldestPage.HasMore)
	for _, c := range oldestPage.Items {
		assert.NotEqual(t, newestID, c.ID, "default request must not accidentally include the newest comment")
	}

	// sort_dir=desc must reach it.
	newestPage, err := commentRepo.ListByTask(ctx, taskID, repository.CommentFilter{}, pagination.Params{SortDir: "desc"})
	require.NoError(t, err)
	require.Equal(t, total, newestPage.TotalCount)
	require.Len(t, newestPage.Items, pagination.DefaultPageSize)
	assert.True(t, newestPage.HasMore)
	require.NotEmpty(t, newestPage.Items)
	assert.Equal(t, newestID, newestPage.Items[0].ID, "sort_dir=desc page 1 must contain the newest comment first")
}
