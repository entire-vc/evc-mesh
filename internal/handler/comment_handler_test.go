package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func setupCommentTest(mockSvc *MockCommentService) (*CommentHandler, *echo.Echo) {
	e := echo.New()
	h := NewCommentHandler(mockSvc, nil) // nil = full-UUID only; existing tests use full UUIDs
	return h, e
}

// --- TestCommentHandler_Create ---

func TestCommentHandler_Create_Success(t *testing.T) {
	taskID := uuid.New()
	userID := uuid.New()

	mockSvc := &MockCommentService{
		CreateFunc: func(ctx context.Context, comment *domain.Comment) error {
			assert.Equal(t, taskID, comment.TaskID)
			assert.Equal(t, "This is a comment", comment.Body)
			assert.Equal(t, userID, comment.AuthorID)
			assert.Equal(t, domain.ActorTypeUser, comment.AuthorType)
			assert.False(t, comment.IsInternal)
			return nil
		},
	}

	h, e := setupCommentTest(mockSvc)

	body := `{"body":"This is a comment"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/comments")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	c.Set("user_id", userID)

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var result domain.Comment
	err = json.Unmarshal(rec.Body.Bytes(), &result)
	require.NoError(t, err)
	assert.Equal(t, "This is a comment", result.Body)
}

func TestCommentHandler_Create_Internal(t *testing.T) {
	taskID := uuid.New()
	agentID := uuid.New()

	mockSvc := &MockCommentService{
		CreateFunc: func(ctx context.Context, comment *domain.Comment) error {
			assert.True(t, comment.IsInternal)
			assert.Equal(t, agentID, comment.AuthorID)
			assert.Equal(t, domain.ActorTypeAgent, comment.AuthorType)
			return nil
		},
	}

	h, e := setupCommentTest(mockSvc)

	body := `{"body":"Agent internal note","is_internal":true}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/comments")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	c.Set("agent_id", agentID)

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestCommentHandler_Create_MissingBody(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockCommentService{}
	h, e := setupCommentTest(mockSvc)

	body := `{"is_internal":false}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/comments")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var apiErr apierror.Error
	err = json.Unmarshal(rec.Body.Bytes(), &apiErr)
	require.NoError(t, err)
	assert.Equal(t, "body is required", apiErr.Validation["body"])
}

func TestCommentHandler_Create_InvalidTaskID(t *testing.T) {
	mockSvc := &MockCommentService{}
	h, e := setupCommentTest(mockSvc)

	body := `{"body":"Test"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/comments")
	c.SetParamNames("task_id")
	c.SetParamValues("not-valid")

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCommentHandler_Create_ServiceError(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockCommentService{
		CreateFunc: func(ctx context.Context, comment *domain.Comment) error {
			return apierror.NotFound("Task")
		},
	}

	h, e := setupCommentTest(mockSvc)

	body := `{"body":"Test comment"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/comments")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- TestCommentHandler_List ---

func TestCommentHandler_List_Success(t *testing.T) {
	taskID := uuid.New()
	now := time.Now()
	comments := []domain.Comment{
		{ID: uuid.New(), TaskID: taskID, Body: "Comment 1", AuthorType: domain.ActorTypeUser, CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), TaskID: taskID, Body: "Comment 2", AuthorType: domain.ActorTypeAgent, CreatedAt: now, UpdatedAt: now},
	}

	mockSvc := &MockCommentService{
		ListByTaskFunc: func(ctx context.Context, tid uuid.UUID, filter repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error) {
			assert.Equal(t, taskID, tid)
			return pagination.NewPage(comments, 2, pg), nil
		},
	}

	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/?page=1&page_size=10", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/comments")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var page pagination.Page[domain.Comment]
	err = json.Unmarshal(rec.Body.Bytes(), &page)
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
}

func TestCommentHandler_List_WithIncludeInternal(t *testing.T) {
	taskID := uuid.New()

	mockSvc := &MockCommentService{
		ListByTaskFunc: func(ctx context.Context, tid uuid.UUID, filter repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error) {
			assert.True(t, filter.IncludeInternal)
			return pagination.NewPage([]domain.Comment{}, 0, pg), nil
		},
	}

	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/?include_internal=true", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/comments")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestCommentHandler_List_SortDirBinding pins that the handler binds and
// normalizes sort_dir and passes it through to the service unchanged — the
// actual defect (task 4222c17d, D3) was one layer down, in the repository
// hardcoding ASC regardless of what the handler bound, so this test alone
// would NOT have caught the regression. It documents that this layer was
// already correct, so a future change here doesn't quietly break it while
// the repo-level test (comment_repo_test.go, real DB per §1o) covers the
// part that was actually broken.
func TestCommentHandler_List_SortDirBinding(t *testing.T) {
	taskID := uuid.New()

	cases := []struct {
		name        string
		query       string
		wantSortDir string
	}{
		{"explicit desc", "?sort_dir=desc", "desc"},
		{"explicit asc", "?sort_dir=asc", "asc"},
		{"unspecified defaults to asc", "", "asc"},
		{"unrecognized value falls back to asc, not left as-is", "?sort_dir=bogus", "asc"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotSortDir string
			mockSvc := &MockCommentService{
				ListByTaskFunc: func(ctx context.Context, tid uuid.UUID, filter repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error) {
					gotSortDir = pg.SortDir
					return pagination.NewPage([]domain.Comment{}, 0, pg), nil
				},
			}
			h, e := setupCommentTest(mockSvc)

			req := httptest.NewRequest(http.MethodGet, "/"+tc.query, http.NoBody)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetPath("/tasks/:task_id/comments")
			c.SetParamNames("task_id")
			c.SetParamValues(taskID.String())

			err := h.List(c)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tc.wantSortDir, gotSortDir)
		})
	}
}

func TestCommentHandler_List_IncludesReplies(t *testing.T) {
	taskID := uuid.New()
	parentID := uuid.New()
	replyID := uuid.New()
	now := time.Now()

	comments := []domain.Comment{
		{ID: parentID, TaskID: taskID, Body: "top-level", AuthorType: domain.ActorTypeUser, CreatedAt: now, UpdatedAt: now},
		{ID: replyID, TaskID: taskID, ParentCommentID: &parentID, Body: "reply", AuthorType: domain.ActorTypeUser, CreatedAt: now, UpdatedAt: now},
	}

	mockSvc := &MockCommentService{
		ListByTaskFunc: func(_ context.Context, tid uuid.UUID, _ repository.CommentFilter, pg pagination.Params) (*pagination.Page[domain.Comment], error) {
			return pagination.NewPage(comments, 2, pg), nil
		},
	}

	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/comments")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	err := h.List(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var page pagination.Page[domain.Comment]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	assert.Len(t, page.Items, 2)

	var replyFound bool
	for _, c := range page.Items {
		if c.ID == replyID {
			replyFound = true
			assert.NotNil(t, c.ParentCommentID)
			assert.Equal(t, parentID, *c.ParentCommentID)
		}
	}
	assert.True(t, replyFound, "reply comment must appear in list response")
}

// --- TestCommentHandler_Delete ---

func TestCommentHandler_Delete_Success(t *testing.T) {
	commentID := uuid.New()
	mockSvc := &MockCommentService{
		DeleteFunc: func(ctx context.Context, id uuid.UUID) error {
			assert.Equal(t, commentID, id)
			return nil
		},
	}

	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/comments/:comment_id")
	c.SetParamNames("comment_id")
	c.SetParamValues(commentID.String())

	err := h.Delete(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestCommentHandler_Delete_InvalidUUID(t *testing.T) {
	mockSvc := &MockCommentService{}
	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/comments/:comment_id")
	c.SetParamNames("comment_id")
	c.SetParamValues("bad")

	err := h.Delete(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCommentHandler_Delete_NotFound(t *testing.T) {
	commentID := uuid.New()
	mockSvc := &MockCommentService{
		DeleteFunc: func(ctx context.Context, id uuid.UUID) error {
			return apierror.NotFound("Comment")
		},
	}

	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/comments/:comment_id")
	c.SetParamNames("comment_id")
	c.SetParamValues(commentID.String())

	err := h.Delete(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- TestCommentHandler_GetMyComments ---

func TestCommentHandler_GetMyComments_Success(t *testing.T) {
	userID := uuid.New()
	now := time.Now().UTC()
	page := &domain.CommentViewPage{
		Items: []domain.CommentView{
			{CommentID: uuid.New(), TaskTitle: "Task A", CommentBody: "hello", CreatedAt: now},
		},
		NextCursor: nil,
	}

	mockSvc := &MockCommentService{
		ListByAuthorFunc: func(_ context.Context, authorID uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error) {
			assert.Equal(t, userID, authorID)
			assert.Equal(t, 50, filter.Limit)
			return page, nil
		},
	}
	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/comments?limit=50", http.NoBody)
	ctx := actorctx.WithActor(req.Context(), userID, domain.ActorTypeUser)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.GetMyComments(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var got domain.CommentViewPage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Len(t, got.Items, 1)
	assert.Equal(t, "Task A", got.Items[0].TaskTitle)
}

func TestCommentHandler_GetMyComments_Unauthenticated(t *testing.T) {
	mockSvc := &MockCommentService{}
	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/comments", http.NoBody)
	// no actorctx injected → actor ID is uuid.Nil
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.GetMyComments(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestCommentHandler_GetMyComments_BadCursor(t *testing.T) {
	userID := uuid.New()
	mockSvc := &MockCommentService{}
	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/comments?before=not-a-timestamp", http.NoBody)
	ctx := actorctx.WithActor(req.Context(), userID, domain.ActorTypeUser)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.GetMyComments(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var apiErr apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.NotEmpty(t, apiErr.Validation["before"])
}

func TestCommentHandler_GetMyComments_BadBeforeID(t *testing.T) {
	userID := uuid.New()
	mockSvc := &MockCommentService{}
	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/comments?before=2026-01-01T00:00:00Z&before_id=not-a-uuid", http.NoBody)
	ctx := actorctx.WithActor(req.Context(), userID, domain.ActorTypeUser)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.GetMyComments(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var apiErr apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.NotEmpty(t, apiErr.Validation["before_id"])
}

// TestCommentHandler_GetMyComments_TupleCursor pins that before/before_id
// reach the service as a pair (#c6dc694e) — the id half is the tiebreaker
// that keeps a page boundary landing inside a same-timestamp group from
// silently dropping the rest of that group.
func TestCommentHandler_GetMyComments_TupleCursor(t *testing.T) {
	userID := uuid.New()
	before := time.Now().UTC()
	beforeID := uuid.New()
	page := &domain.CommentViewPage{Items: []domain.CommentView{}}

	mockSvc := &MockCommentService{
		ListByAuthorFunc: func(_ context.Context, _ uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error) {
			require.NotNil(t, filter.Before)
			require.NotNil(t, filter.BeforeID)
			assert.WithinDuration(t, before, *filter.Before, time.Second)
			assert.Equal(t, beforeID, *filter.BeforeID)
			return page, nil
		},
	}
	h, e := setupCommentTest(mockSvc)

	url := "/api/v1/me/comments?before=" + before.Format(time.RFC3339) + "&before_id=" + beforeID.String()
	req := httptest.NewRequest(http.MethodGet, url, http.NoBody)
	ctx := actorctx.WithActor(req.Context(), userID, domain.ActorTypeUser)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.GetMyComments(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// --- TestCommentHandler_GetRecentByWorkspace ---

func TestCommentHandler_GetRecentByWorkspace_Success(t *testing.T) {
	wsID := uuid.New()
	now := time.Now().UTC()
	nextCursor := now.Add(-time.Minute)
	page := &domain.CommentViewPage{
		Items: []domain.CommentView{
			{CommentID: uuid.New(), TaskTitle: "Task B", AuthorName: "Garfield", CreatedAt: now},
		},
		NextCursor: &nextCursor,
	}

	mockSvc := &MockCommentService{
		ListRecentByWorkspaceFunc: func(_ context.Context, id uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error) {
			assert.Equal(t, wsID, id)
			assert.Equal(t, 50, filter.Limit)
			return page, nil
		},
	}
	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.GetRecentByWorkspace(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var got domain.CommentViewPage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Len(t, got.Items, 1)
	assert.NotNil(t, got.NextCursor)
}

func TestCommentHandler_GetRecentByWorkspace_BadWorkspaceID(t *testing.T) {
	mockSvc := &MockCommentService{}
	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues("not-a-uuid")

	err := h.GetRecentByWorkspace(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCommentHandler_GetRecentByWorkspace_BadCursor(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockCommentService{}
	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/?before=definitely-not-rfc3339", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.GetRecentByWorkspace(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var apiErr apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.NotEmpty(t, apiErr.Validation["before"])
}

func TestCommentHandler_GetRecentByWorkspace_BadBeforeID(t *testing.T) {
	wsID := uuid.New()
	mockSvc := &MockCommentService{}
	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodGet, "/?before=2026-01-01T00:00:00Z&before_id=not-a-uuid", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.GetRecentByWorkspace(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var apiErr apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.NotEmpty(t, apiErr.Validation["before_id"])
}

// TestCommentHandler_GetRecentByWorkspace_TupleCursor pins that before/before_id
// reach the service as a pair (#c6dc694e) — see the /me/comments twin above.
func TestCommentHandler_GetRecentByWorkspace_TupleCursor(t *testing.T) {
	wsID := uuid.New()
	before := time.Now().UTC()
	beforeID := uuid.New()
	page := &domain.CommentViewPage{Items: []domain.CommentView{}}

	mockSvc := &MockCommentService{
		ListRecentByWorkspaceFunc: func(_ context.Context, _ uuid.UUID, filter repository.CommentViewFilter) (*domain.CommentViewPage, error) {
			require.NotNil(t, filter.Before)
			require.NotNil(t, filter.BeforeID)
			assert.WithinDuration(t, before, *filter.Before, time.Second)
			assert.Equal(t, beforeID, *filter.BeforeID)
			return page, nil
		},
	}
	h, e := setupCommentTest(mockSvc)

	url := "/?before=" + before.Format(time.RFC3339) + "&before_id=" + beforeID.String()
	req := httptest.NewRequest(http.MethodGet, url, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	err := h.GetRecentByWorkspace(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// --- comment.metadata pass-through (task #13e391d2) ---
//
// The defect these cover was invisible at every layer that had a test: the domain struct,
// the repository, and the DB column all handled Metadata correctly, and the only thing
// missing was the field on createCommentRequest — so `c.Bind` dropped it and the API
// answered 201 with `{}`. A test asserting "POST succeeds" passed throughout. What was
// missing is an assertion that the value the caller SENT reached the service.

func TestCommentHandler_Create_MetadataReachesService(t *testing.T) {
	taskID := uuid.New()
	agentID := uuid.New()

	var got json.RawMessage
	mockSvc := &MockCommentService{
		CreateFunc: func(_ context.Context, comment *domain.Comment) error {
			got = comment.Metadata
			return nil
		},
	}

	h, e := setupCommentTest(mockSvc)

	// Mixed value types on purpose: the original bug report suspected the boolean was to
	// blame, so a string-only payload would not discriminate between "booleans break it"
	// and "the whole field is dropped".
	body := `{"body":"auto nudge","metadata":{"source":"pr-task-driver","auto":true,"attempt":3}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/comments")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	c.Set("agent_id", agentID)

	err := h.Create(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, rec.Code)

	require.NotEmpty(t, got, "metadata never reached the service — the DTO dropped it")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(got, &decoded))
	assert.Equal(t, "pr-task-driver", decoded["source"])
	assert.Equal(t, true, decoded["auto"])
	assert.Equal(t, float64(3), decoded["attempt"])

	// And the response must not claim something different from what was stored.
	var result domain.Comment
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	var echoed map[string]any
	require.NoError(t, json.Unmarshal(result.Metadata, &echoed))
	assert.Equal(t, "pr-task-driver", echoed["source"])
}

func TestCommentHandler_Create_MetadataOmittedStaysEmpty(t *testing.T) {
	taskID := uuid.New()

	called := false
	mockSvc := &MockCommentService{
		CreateFunc: func(_ context.Context, comment *domain.Comment) error {
			called = true
			assert.Empty(t, comment.Metadata, "absent metadata must not be invented")
			return nil
		},
	}

	h, e := setupCommentTest(mockSvc)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"body":"plain comment"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/comments")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	c.Set("user_id", uuid.New())

	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.True(t, called)
}
