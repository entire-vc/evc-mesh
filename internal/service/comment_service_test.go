package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// setupCommentService returns a commentService wired to fresh mocks.
func setupCommentService() (*commentService, *MockCommentRepository, *MockTaskRepository) {
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	svc := NewCommentService(commentRepo, taskRepo, activityRepo).(*commentService)

	// Freeze the clock.
	timeNow = func() time.Time { return frozenTime }

	return svc, commentRepo, taskRepo
}

// ---------------------------------------------------------------------------
// TestCommentService_Create
// ---------------------------------------------------------------------------

func TestCommentService_Create(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(commentRepo *MockCommentRepository, taskRepo *MockTaskRepository) *domain.Comment
		wantErr   bool
		errCode   int
		errMsg    string
		checkFunc func(t *testing.T, comment *domain.Comment, repo *MockCommentRepository)
	}{
		{
			name: "success",
			setup: func(_ *MockCommentRepository, taskRepo *MockTaskRepository) *domain.Comment {
				taskID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "A task"}
				return &domain.Comment{
					TaskID:     taskID,
					AuthorID:   uuid.New(),
					AuthorType: domain.ActorTypeUser,
					Body:       "This is a comment",
				}
			},
			wantErr: false,
			checkFunc: func(t *testing.T, comment *domain.Comment, repo *MockCommentRepository) {
				assert.NotEqual(t, uuid.Nil, comment.ID)
				assert.Equal(t, frozenTime, comment.CreatedAt)
				assert.Equal(t, frozenTime, comment.UpdatedAt)
				stored := repo.items[comment.ID]
				require.NotNil(t, stored)
				assert.Equal(t, "This is a comment", stored.Body)
			},
		},
		{
			name: "success - with valid parent comment",
			setup: func(commentRepo *MockCommentRepository, taskRepo *MockTaskRepository) *domain.Comment {
				taskID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "A task"}

				parentID := uuid.New()
				commentRepo.items[parentID] = &domain.Comment{
					ID:     parentID,
					TaskID: taskID,
					Body:   "Parent comment",
				}

				return &domain.Comment{
					TaskID:          taskID,
					ParentCommentID: &parentID,
					AuthorID:        uuid.New(),
					AuthorType:      domain.ActorTypeAgent,
					Body:            "Reply to parent",
				}
			},
			wantErr: false,
			checkFunc: func(t *testing.T, comment *domain.Comment, _ *MockCommentRepository) {
				assert.NotNil(t, comment.ParentCommentID)
			},
		},
		{
			name: "error - empty body",
			setup: func(_ *MockCommentRepository, taskRepo *MockTaskRepository) *domain.Comment {
				taskID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "A task"}
				return &domain.Comment{
					TaskID:     taskID,
					AuthorID:   uuid.New(),
					AuthorType: domain.ActorTypeUser,
					Body:       "",
				}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
		},
		{
			name: "error - task not found",
			setup: func(_ *MockCommentRepository, _ *MockTaskRepository) *domain.Comment {
				return &domain.Comment{
					TaskID:     uuid.New(),
					AuthorID:   uuid.New(),
					AuthorType: domain.ActorTypeUser,
					Body:       "Orphan comment",
				}
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
		{
			name: "error - parent comment not found",
			setup: func(_ *MockCommentRepository, taskRepo *MockTaskRepository) *domain.Comment {
				taskID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "A task"}
				nonExistentParent := uuid.New()
				return &domain.Comment{
					TaskID:          taskID,
					ParentCommentID: &nonExistentParent,
					AuthorID:        uuid.New(),
					AuthorType:      domain.ActorTypeUser,
					Body:            "Reply to nothing",
				}
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
		{
			name: "error - parent comment belongs to different task",
			setup: func(commentRepo *MockCommentRepository, taskRepo *MockTaskRepository) *domain.Comment {
				taskID := uuid.New()
				otherTaskID := uuid.New()
				taskRepo.items[taskID] = &domain.Task{ID: taskID, Title: "Task A"}
				taskRepo.items[otherTaskID] = &domain.Task{ID: otherTaskID, Title: "Task B"}

				parentID := uuid.New()
				commentRepo.items[parentID] = &domain.Comment{
					ID:     parentID,
					TaskID: otherTaskID, // belongs to a different task
					Body:   "Parent on other task",
				}

				return &domain.Comment{
					TaskID:          taskID,
					ParentCommentID: &parentID,
					AuthorID:        uuid.New(),
					AuthorType:      domain.ActorTypeUser,
					Body:            "Cross-task reply",
				}
			},
			wantErr: true,
			errCode: http.StatusBadRequest,
			errMsg:  "does not belong to the same task",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, commentRepo, taskRepo := setupCommentService()
			ctx := context.Background()
			comment := tt.setup(commentRepo, taskRepo)

			err := svc.Create(ctx, comment)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
				if tt.errMsg != "" {
					assert.Contains(t, apiErr.Message, tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				if tt.checkFunc != nil {
					tt.checkFunc(t, comment, commentRepo)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCommentService_Update
// ---------------------------------------------------------------------------

func TestCommentService_Update(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockCommentRepository) (context.Context, *domain.Comment)
		wantErr bool
		errCode int
	}{
		{
			name: "success - only body is updated",
			setup: func(repo *MockCommentRepository) (context.Context, *domain.Comment) {
				authorID := uuid.New()
				id := uuid.New()
				repo.items[id] = &domain.Comment{
					ID:         id,
					TaskID:     uuid.New(),
					AuthorID:   authorID,
					AuthorType: domain.ActorTypeUser,
					Body:       "Original body",
				}
				ctx := actorctx.WithActor(context.Background(), authorID, domain.ActorTypeUser)
				return ctx, &domain.Comment{ID: id, Body: "Updated body"}
			},
			wantErr: false,
		},
		{
			name: "error - comment not found",
			setup: func(_ *MockCommentRepository) (context.Context, *domain.Comment) {
				return context.Background(), &domain.Comment{ID: uuid.New(), Body: "Ghost"}
			},
			wantErr: true,
			errCode: http.StatusNotFound,
		},
		{
			name: "error - forbidden when not the author",
			setup: func(repo *MockCommentRepository) (context.Context, *domain.Comment) {
				id := uuid.New()
				repo.items[id] = &domain.Comment{
					ID:         id,
					TaskID:     uuid.New(),
					AuthorID:   uuid.New(),
					AuthorType: domain.ActorTypeUser,
					Body:       "Original body",
				}
				// actor is a different user
				ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
				return ctx, &domain.Comment{ID: id, Body: "Tampered body"}
			},
			wantErr: true,
			errCode: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, commentRepo, _ := setupCommentService()
			ctx, comment := tt.setup(commentRepo)

			err := svc.Update(ctx, comment)

			if tt.wantErr {
				require.Error(t, err)
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.errCode, apiErr.Code)
			} else {
				require.NoError(t, err)
				stored := commentRepo.items[comment.ID]
				require.NotNil(t, stored)
				assert.Equal(t, "Updated body", stored.Body)
				assert.Equal(t, frozenTime, stored.UpdatedAt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCommentService_ListByTask
// ---------------------------------------------------------------------------

func TestCommentService_ListByTask(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(repo *MockCommentRepository) uuid.UUID
		wantLen int
	}{
		{
			name: "with matching comments",
			setup: func(repo *MockCommentRepository) uuid.UUID {
				taskID := uuid.New()
				for i := 0; i < 3; i++ {
					id := uuid.New()
					repo.items[id] = &domain.Comment{ID: id, TaskID: taskID, Body: "Comment"}
				}
				// Comment on another task.
				otherID := uuid.New()
				repo.items[otherID] = &domain.Comment{ID: otherID, TaskID: uuid.New(), Body: "Other"}
				return taskID
			},
			wantLen: 3,
		},
		{
			name: "empty result",
			setup: func(_ *MockCommentRepository) uuid.UUID {
				return uuid.New()
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, commentRepo, _ := setupCommentService()
			ctx := context.Background()
			taskID := tt.setup(commentRepo)

			page, err := svc.ListByTask(ctx, taskID, repository.CommentFilter{}, pagination.Params{Page: 1, PageSize: 50})

			require.NoError(t, err)
			require.NotNil(t, page)
			assert.Len(t, page.Items, tt.wantLen)
		})
	}
}
