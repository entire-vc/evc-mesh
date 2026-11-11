package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func deleteCASRequest(t *testing.T, mockSvc *MockTaskDependencyService, taskID, depID uuid.UUID, query string) *httptest.ResponseRecorder {
	t.Helper()
	h, e := setupDependencyTest(mockSvc)
	req := httptest.NewRequest(http.MethodDelete, "/"+query, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies/:dep_id")
	c.SetParamNames("task_id", "dep_id")
	c.SetParamValues(taskID.String(), depID.String())
	require.NoError(t, h.Delete(c))
	return rec
}

func TestDependencyHandler_DeleteCAS(t *testing.T) {
	taskID, depID := uuid.New(), uuid.New()
	list := func(context.Context, uuid.UUID) ([]domain.TaskDependency, error) {
		return []domain.TaskDependency{{ID: depID, TaskID: taskID}}, nil
	}

	t.Run("current generation deletes via the CAS method only", func(t *testing.T) {
		var gotGen int64
		legacy := false
		rec := deleteCASRequest(t, &MockTaskDependencyService{
			ListByTaskFunc: list,
			DeleteFunc:     func(context.Context, uuid.UUID) error { legacy = true; return nil },
			DeleteIfGenerationFunc: func(_ context.Context, tid, id uuid.UUID, gen int64) error {
				assert.Equal(t, taskID, tid)
				assert.Equal(t, depID, id)
				gotGen = gen
				return nil
			},
		}, taskID, depID, "?expected_generation=3")
		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.EqualValues(t, 3, gotGen)
		assert.False(t, legacy, "unconditional delete must not run when a generation is given")
	})

	t.Run("stale generation is 409", func(t *testing.T) {
		rec := deleteCASRequest(t, &MockTaskDependencyService{
			ListByTaskFunc: list,
			DeleteIfGenerationFunc: func(context.Context, uuid.UUID, uuid.UUID, int64) error {
				return apierror.Conflict("stale")
			},
		}, taskID, depID, "?expected_generation=1")
		assert.Equal(t, http.StatusConflict, rec.Code)
	})

	t.Run("edge vanished under the CAS is 404", func(t *testing.T) {
		rec := deleteCASRequest(t, &MockTaskDependencyService{
			ListByTaskFunc: list,
			DeleteIfGenerationFunc: func(context.Context, uuid.UUID, uuid.UUID, int64) error {
				return apierror.NotFound("TaskDependency")
			},
		}, taskID, depID, "?expected_generation=1")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("foreign source edge is 404 and nothing is deleted", func(t *testing.T) {
		called := false
		rec := deleteCASRequest(t, &MockTaskDependencyService{
			ListByTaskFunc: func(context.Context, uuid.UUID) ([]domain.TaskDependency, error) { return nil, nil },
			DeleteIfGenerationFunc: func(context.Context, uuid.UUID, uuid.UUID, int64) error {
				called = true
				return nil
			},
		}, taskID, depID, "?expected_generation=1")
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.False(t, called)
	})

	for name, q := range map[string]string{
		"zero":      "?expected_generation=0",
		"negative":  "?expected_generation=-2",
		"non-int":   "?expected_generation=abc",
		"empty":     "?expected_generation=",
		"repeated":  "?expected_generation=1&expected_generation=1",
		"overflow":  "?expected_generation=99999999999999999999",
		"fractions": "?expected_generation=1.5",
	} {
		t.Run("malformed "+name+" is 400", func(t *testing.T) {
			called := false
			rec := deleteCASRequest(t, &MockTaskDependencyService{
				ListByTaskFunc: list,
				DeleteFunc:     func(context.Context, uuid.UUID) error { called = true; return nil },
				DeleteIfGenerationFunc: func(context.Context, uuid.UUID, uuid.UUID, int64) error {
					called = true
					return nil
				},
			}, taskID, depID, q)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.False(t, called, "a malformed generation must not fall back to an unconditional delete")
		})
	}
}
