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
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func setupCommentTest(mockSvc *MockCommentService) (*CommentHandler, *echo.Echo) {
	e := echo.New()
	h := NewCommentHandler(mockSvc)
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
