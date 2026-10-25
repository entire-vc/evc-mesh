package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func patchDependency(t *testing.T, h *DependencyHandler, e *echo.Echo, taskID, depID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies/:dep_id")
	c.SetParamNames("task_id", "dep_id")
	c.SetParamValues(taskID, depID)
	require.NoError(t, h.Update(c))
	return rec
}

func TestDependencyHandler_Update(t *testing.T) {
	taskID, depID := uuid.New(), uuid.New()
	var got service.UpdateDependencyConditionInput
	mockSvc := &MockTaskDependencyService{
		ListByTaskFunc: func(context.Context, uuid.UUID) ([]domain.TaskDependency, error) {
			return []domain.TaskDependency{{ID: depID, TaskID: taskID}}, nil
		},
		UpdateConditionFunc: func(_ context.Context, id uuid.UUID, in service.UpdateDependencyConditionInput) (*domain.TaskDependency, error) {
			assert.Equal(t, depID, id)
			got = in
			return &domain.TaskDependency{ID: depID, Condition: domain.DependencyConditionAccessReady, ConditionSatisfied: true}, nil
		},
	}
	h, e := setupDependencyTest(mockSvc)

	rec := patchDependency(t, h, e, taskID.String(), depID.String(), `{"condition":"access_ready","evidence_url":"https://mesh.example/e"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, got.Condition)
	assert.Equal(t, domain.DependencyConditionAccessReady, *got.Condition)
	require.NotNil(t, got.EvidenceURL)
	var out domain.TaskDependency
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.True(t, out.ConditionSatisfied)
}

func TestDependencyHandler_Update_Refusals(t *testing.T) {
	taskID, depID := uuid.New(), uuid.New()
	called := false
	mockSvc := &MockTaskDependencyService{
		ListByTaskFunc: func(context.Context, uuid.UUID) ([]domain.TaskDependency, error) {
			return []domain.TaskDependency{{ID: depID, TaskID: taskID}}, nil
		},
		UpdateConditionFunc: func(context.Context, uuid.UUID, service.UpdateDependencyConditionInput) (*domain.TaskDependency, error) {
			called = true
			return nil, apierror.BadRequest("invalid condition")
		},
	}
	h, e := setupDependencyTest(mockSvc)

	assert.Equal(t, http.StatusBadRequest, patchDependency(t, h, e, taskID.String(), "not-a-uuid", `{"condition":"deployed"}`).Code)
	assert.Equal(t, http.StatusBadRequest, patchDependency(t, h, e, taskID.String(), depID.String(), `{`).Code)
	assert.Equal(t, http.StatusBadRequest, patchDependency(t, h, e, taskID.String(), depID.String(), `{}`).Code, "nothing to change")
	assert.False(t, called)
	assert.Equal(t, http.StatusNotFound, patchDependency(t, h, e, taskID.String(), uuid.NewString(), `{"condition":"deployed"}`).Code, "an edge of another task is not found")
	assert.False(t, called)
	assert.Equal(t, http.StatusBadRequest, patchDependency(t, h, e, taskID.String(), depID.String(), `{"condition":"nonsense"}`).Code, "service refusal is passed through")
	assert.True(t, called)

	mockSvc.ListByTaskFunc = func(context.Context, uuid.UUID) ([]domain.TaskDependency, error) { return nil, errors.New("boom") }
	assert.Equal(t, http.StatusInternalServerError, patchDependency(t, h, e, taskID.String(), depID.String(), `{"condition":"deployed"}`).Code)
}
