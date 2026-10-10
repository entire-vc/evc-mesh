package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func runtimeStateCall(t *testing.T, h *TaskHandler, method, body string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetPath("/tasks/:task_id/runtime-state")
	c.SetParamNames("task_id")
	c.SetParamValues(uuid.New().String())
	if method == http.MethodDelete {
		return rec, h.DeleteRuntimeState(c)
	}
	return rec, h.PutRuntimeState(c)
}

func TestPutRuntimeState_PassesFieldsAndReturnsState(t *testing.T) {
	var got *domain.RuntimeState
	h := NewTaskHandler(&MockTaskService{SetRuntimeStateFunc: func(s *domain.RuntimeState) (*domain.RuntimeState, error) {
		got = s
		return &domain.RuntimeState{Kind: s.Kind, Source: domain.RuntimeStateSourceExplicit}, nil
	}})
	rec, err := runtimeStateCall(t, h, http.MethodPut, `{"kind":"waiting","ref":"pipeline:g/p#7","lane":"linus","reason":"ci"}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, domain.RuntimeKindWaiting, got.Kind)
	assert.Equal(t, "pipeline:g/p#7", *got.Ref)
	assert.Equal(t, "linus", *got.Lane)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "waiting", body["kind"])
}

func TestPutRuntimeState_NullKindClears(t *testing.T) {
	called, cleared := false, false
	h := NewTaskHandler(&MockTaskService{SetRuntimeStateFunc: func(s *domain.RuntimeState) (*domain.RuntimeState, error) {
		called, cleared = true, s == nil
		return nil, nil
	}})
	rec, err := runtimeStateCall(t, h, http.MethodPut, `{"kind":null}`)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, called && cleared, "kind=null must reach the service as a clear")
	assert.JSONEq(t, `{"runtime_state":null}`, rec.Body.String())
}

func TestPutRuntimeState_OmittedKindWithOtherFieldsIsRejectedNotCleared(t *testing.T) {
	called := false
	h := NewTaskHandler(&MockTaskService{SetRuntimeStateFunc: func(*domain.RuntimeState) (*domain.RuntimeState, error) {
		called = true
		return nil, nil
	}})
	rec, err := runtimeStateCall(t, h, http.MethodPut, `{"ref":"pipeline:g/p#7"}`)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), `"field":"kind"`)
	assert.False(t, called, "a malformed write must not reach the service as a clear")
}

func TestPutRuntimeState_ValidationIs422NamingField(t *testing.T) {
	h := NewTaskHandler(&MockTaskService{SetRuntimeStateFunc: func(*domain.RuntimeState) (*domain.RuntimeState, error) {
		return nil, &domain.RuntimeStateValidationError{Field: "kind", Message: "must be one of working, waiting, queued, stopped"}
	}})
	rec, err := runtimeStateCall(t, h, http.MethodPut, `{"kind":"dozing"}`)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), `"field":"kind"`)
	assert.Contains(t, rec.Body.String(), "invalid runtime_state: kind")
}

func TestDeleteRuntimeState_204(t *testing.T) {
	cleared := false
	h := NewTaskHandler(&MockTaskService{SetRuntimeStateFunc: func(s *domain.RuntimeState) (*domain.RuntimeState, error) {
		cleared = s == nil
		return nil, nil
	}})
	rec, err := runtimeStateCall(t, h, http.MethodDelete, ``)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, cleared)
}

func TestListFilter_RuntimeKind(t *testing.T) {
	f, err := listTasksQuery{RuntimeKind: "working"}.taskFilter()
	require.NoError(t, err)
	require.NotNil(t, f.RuntimeKind)
	assert.Equal(t, domain.RuntimeKindWorking, *f.RuntimeKind)
	_, err = listTasksQuery{RuntimeKind: "dozing"}.taskFilter()
	assert.Error(t, err, "an unknown kind must not silently widen the query")
	f, err = listTasksQuery{}.taskFilter()
	require.NoError(t, err)
	assert.Nil(t, f.RuntimeKind)
}
