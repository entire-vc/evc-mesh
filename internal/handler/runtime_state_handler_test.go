package handler

import (
	"context"
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
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
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

func TestPutRuntimeState_PassesReasonCodeAndParams(t *testing.T) {
	var got *domain.RuntimeState
	h := NewTaskHandler(&MockTaskService{SetRuntimeStateFunc: func(s *domain.RuntimeState) (*domain.RuntimeState, error) {
		got = s
		return s, nil
	}})
	rec, err := runtimeStateCall(t, h, http.MethodPut, `{"kind":"stopped","reason_code":"review_cooldown","reason_params":{"minutes":"4"}}`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got)
	assert.Equal(t, "review_cooldown", *got.ReasonCode)
	assert.Equal(t, map[string]string{"minutes": "4"}, got.ReasonParams)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "review_cooldown", body["reason_code"])
}

func TestPutRuntimeState_ReasonCodeWithoutKindIsRejected(t *testing.T) {
	h := NewTaskHandler(&MockTaskService{SetRuntimeStateFunc: func(s *domain.RuntimeState) (*domain.RuntimeState, error) {
		t.Fatal("must not reach the service")
		return nil, nil
	}})
	rec, err := runtimeStateCall(t, h, http.MethodPut, `{"reason_code":"pause"}`)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

type stubDepReader struct {
	repository.TaskDependencyRepository
	edges []domain.TaskDependency
	err   error
}

func (s stubDepReader) ListByTask(context.Context, uuid.UUID) ([]domain.TaskDependency, error) {
	return s.edges, s.err
}

func putWithAccess(t *testing.T, h *TaskHandler, body string, may func(uuid.UUID) bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames("task_id")
	c.SetParamValues(uuid.New().String())
	mw.SetProjectAccess(c, mw.ProjectAccessCheck{Task: may})
	require.NoError(t, h.PutRuntimeState(c))
	return rec
}

func TestPutRuntimeState_ConstraintsPassedToService(t *testing.T) {
	var got *domain.RuntimeState
	h := NewTaskHandler(&MockTaskService{SetRuntimeStateFunc: func(s *domain.RuntimeState) (*domain.RuntimeState, error) {
		got = s
		return s, nil
	}})
	body := `{"kind":"stopped","constraints":[{"id":"acl:1","class":"write_acl","reason_code":"forbidden","wake":{"predicate":"unknown"},
	"source":{"kind":"runner"},"observed_at":"2026-11-03T10:00:00Z","next_actor":{"kind":"human","name":"owner"}}]}`
	rec, err := runtimeStateCall(t, h, http.MethodPut, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, got.Constraints, 1)
	assert.Equal(t, domain.ConstraintWriteACL, got.Constraints[0].Class)
	assert.Equal(t, "human", got.Constraints[0].NextActor.Kind)
}

// Negative control: constraints alone must not turn into a clear.
func TestPutRuntimeState_ConstraintsWithoutKindIsRejected(t *testing.T) {
	called := false
	h := NewTaskHandler(&MockTaskService{SetRuntimeStateFunc: func(*domain.RuntimeState) (*domain.RuntimeState, error) {
		called = true
		return nil, nil
	}})
	rec, err := runtimeStateCall(t, h, http.MethodPut, `{"constraints":[]}`)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.False(t, called)
}

func TestPutRuntimeState_ConstraintsValidationIs422(t *testing.T) {
	h := NewTaskHandler(&MockTaskService{SetRuntimeStateFunc: func(*domain.RuntimeState) (*domain.RuntimeState, error) {
		return nil, &domain.RuntimeStateValidationError{Field: "constraints[0].class", Message: "must be one of x"}
	}})
	rec, err := runtimeStateCall(t, h, http.MethodPut, `{"kind":"stopped","constraints":[{"id":"a","class":"bogus"}]}`)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), `"field":"constraints[0].class"`)
}

// Read-side union: live blocker + producer local WAIT both returned; a fulfilled
// edge and relates_to are not; a closed blocker is unknown, not absent, and not leaked.
func TestPutRuntimeState_ReadUnionAndACLRedaction(t *testing.T) {
	visible, hidden := uuid.New(), uuid.New()
	mk := func(to uuid.UUID, typ domain.DependencyType, done bool) domain.TaskDependency {
		return domain.TaskDependency{ID: uuid.New(), DependsOnTaskID: to, DependencyType: typ, Condition: domain.DependencyConditionTaskDone, ConditionSatisfied: done}
	}
	fulfilled := uuid.New()
	h := NewTaskHandler(&MockTaskService{SetRuntimeStateFunc: func(s *domain.RuntimeState) (*domain.RuntimeState, error) {
		s.Source = domain.RuntimeStateSourceExplicit
		return s, nil
	}}).WithDependencyReader(stubDepReader{edges: []domain.TaskDependency{
		mk(visible, domain.DependencyTypeBlocks, false),
		mk(hidden, domain.DependencyTypeBlocks, false),
		mk(fulfilled, domain.DependencyTypeBlocks, true),
		mk(uuid.New(), domain.DependencyTypeRelatesTo, false),
	}})
	body := `{"kind":"waiting","constraints":[{"id":"wait:l","class":"local_wait","reason_code":"mr_wait","wake":{"predicate":"mr","ref":"g/p!1"},
	"source":{"kind":"runner"},"observed_at":"2026-11-03T10:00:00Z"}]}`
	rec := putWithAccess(t, h, body, func(id uuid.UUID) bool { return id != hidden })
	require.Equal(t, http.StatusOK, rec.Code)
	var out domain.RuntimeState
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Constraints, 3, "visible dep + hidden dep + local wait")
	byID := map[string]domain.OperationalConstraint{}
	for _, c := range out.Constraints {
		byID[c.ID] = c
	}
	assert.Contains(t, byID, "wait:l")
	raw := rec.Body.String()
	assert.NotContains(t, raw, hidden.String(), "closed blocker must not leak")
	assert.NotContains(t, raw, fulfilled.String(), "fulfilled blocks edge is excluded")
	assert.Contains(t, raw, visible.String())
	assert.Contains(t, raw, "dependency_not_visible")
}

// No access check on the request: every blocker is treated as closed.
func TestPutRuntimeState_ReadUnionWithoutAccessCheckRedactsAll(t *testing.T) {
	blocker := uuid.New()
	h := NewTaskHandler(&MockTaskService{SetRuntimeStateFunc: func(s *domain.RuntimeState) (*domain.RuntimeState, error) { return s, nil }}).
		WithDependencyReader(stubDepReader{edges: []domain.TaskDependency{{ID: uuid.New(), DependsOnTaskID: blocker, DependencyType: domain.DependencyTypeBlocks, Condition: domain.DependencyConditionTaskDone}}})
	rec, err := runtimeStateCall(t, h, http.MethodPut, `{"kind":"waiting"}`)
	require.NoError(t, err)
	assert.NotContains(t, rec.Body.String(), blocker.String())
	assert.Contains(t, rec.Body.String(), "dependency_not_visible")
}

// A checkout-derived state is a read-time guess, never a written one: a live
// blocker turns it into the derived waiting state, while a task with nothing
// blocking keeps the working state.
func TestUnionConstraints_CheckoutDerivedStateYieldsToLiveBlocker(t *testing.T) {
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	mw.SetProjectAccess(c, mw.ProjectAccessCheck{Task: func(uuid.UUID) bool { return true }})
	blocker := uuid.New()
	edge := domain.TaskDependency{ID: uuid.New(), DependsOnTaskID: blocker, DependencyType: domain.DependencyTypeBlocks}
	checkout := func() *domain.RuntimeState {
		return &domain.RuntimeState{Kind: domain.RuntimeKindWorking, Source: domain.RuntimeStateSourceCheckout}
	}

	got := unionConstraints(c, checkout(), []domain.TaskDependency{edge})
	require.NotNil(t, got)
	assert.Equal(t, domain.RuntimeKindWaiting, got.Kind)
	assert.Equal(t, domain.RuntimeStateSourceDependency, got.Source)
	require.Len(t, got.Constraints, 1)
	assert.Equal(t, domain.ConstraintMeshDependency, got.Constraints[0].Class)

	// negative control: no blocker, the checkout state is left as it was
	kept := unionConstraints(c, checkout(), nil)
	assert.Equal(t, domain.RuntimeKindWorking, kept.Kind)
	assert.Equal(t, domain.RuntimeStateSourceCheckout, kept.Source)

	// an explicit producer state keeps its kind and gains the live blocker
	explicit := &domain.RuntimeState{Kind: domain.RuntimeKindWorking, Source: domain.RuntimeStateSourceExplicit}
	merged := unionConstraints(c, explicit, []domain.TaskDependency{edge})
	assert.Equal(t, domain.RuntimeKindWorking, merged.Kind)
	require.Len(t, merged.Constraints, 1)
}

// A closed card carries no active constraint: stored producer constraints are
// dropped from the response too, while an open card keeps them.
func TestAttachTaskConstraints_ClosedTaskDropsStoredConstraints(t *testing.T) {
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	mw.SetProjectAccess(c, mw.ProjectAccessCheck{Task: func(uuid.UUID) bool { return true }})
	h := (&TaskHandler{}).WithDependencyReader(stubDepReader{})
	stored := func() *domain.RuntimeState {
		return &domain.RuntimeState{Kind: domain.RuntimeKindWaiting, Source: domain.RuntimeStateSourceExplicit,
			Constraints: []domain.OperationalConstraint{{ID: "runner:w1", Class: domain.ConstraintLocalWait}}}
	}
	closed := &domain.Task{ID: uuid.New(), StatusCategory: domain.StatusCategoryDone, RuntimeState: stored()}
	cancelled := &domain.Task{ID: uuid.New(), StatusCategory: domain.StatusCategoryCancelled, RuntimeState: stored()}
	open := &domain.Task{ID: uuid.New(), StatusCategory: domain.StatusCategoryInProgress, RuntimeState: stored()}

	h.attachTaskConstraints(c, []*domain.Task{closed, cancelled, open})

	assert.Empty(t, closed.RuntimeState.Constraints)
	assert.Empty(t, cancelled.RuntimeState.Constraints)
	assert.Equal(t, domain.RuntimeKindWaiting, closed.RuntimeState.Kind, "the state itself is not rewritten")
	require.Len(t, open.RuntimeState.Constraints, 1, "negative control: an open card keeps its stored constraint")
}
