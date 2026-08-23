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
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func setupHumanGateDecisionTest(mockSvc *MockCommentService) (*HumanGateDecisionHandler, *echo.Echo) {
	e := echo.New()
	h := NewHumanGateDecisionHandler(mockSvc, nil) // nil = full-UUID only, same convention as setupCommentTest
	return h, e
}

func newHGDRequest(method, path, body string) (*http.Request, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	return req, httptest.NewRecorder()
}

// --- Create ---

func TestHumanGateDecisionHandler_Create_DirectProvenance_Success(t *testing.T) {
	taskID, pavel := uuid.New(), uuid.New()
	var captured domain.RecordHumanGateDecisionInput
	mockSvc := &MockCommentService{
		RecordHumanGateDecisionFunc: func(_ context.Context, input domain.RecordHumanGateDecisionInput) (*domain.HumanGateDecision, error) {
			captured = input
			return &domain.HumanGateDecision{ID: uuid.New(), TaskID: input.TaskID}, nil
		},
	}
	h, e := setupHumanGateDecisionTest(mockSvc)

	ref := uuid.New()
	reqBody, _ := json.Marshal(map[string]any{
		"question_ref": ref, "decided_by": pavel, "provenance": "direct", "channel": "mesh",
	})
	req, rec := newHGDRequest(http.MethodPost, "/", string(reqBody))
	// direct provenance requires the authenticated caller to BE decided_by.
	req = req.WithContext(actorctx.WithActor(req.Context(), pavel, domain.ActorTypeUser))
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/human-gate-decisions")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, taskID, captured.TaskID)
	assert.Nil(t, captured.RecordedBy, "direct provenance leaves recorded_by nil")
}

func TestHumanGateDecisionHandler_Create_AgentAttestedProvenance_SetsRecordedBy(t *testing.T) {
	taskID, pavel, agent := uuid.New(), uuid.New(), uuid.New()
	var captured domain.RecordHumanGateDecisionInput
	mockSvc := &MockCommentService{
		RecordHumanGateDecisionFunc: func(_ context.Context, input domain.RecordHumanGateDecisionInput) (*domain.HumanGateDecision, error) {
			captured = input
			return &domain.HumanGateDecision{ID: uuid.New()}, nil
		},
	}
	h, e := setupHumanGateDecisionTest(mockSvc)

	key := "canonical-decision-test"
	reqBody, _ := json.Marshal(map[string]any{
		"canonical_key": key, "decided_by": pavel, "provenance": "attested", "channel": "telegram", "quote": "да, отвечал",
	})
	req, rec := newHGDRequest(http.MethodPost, "/", string(reqBody))
	req = req.WithContext(actorctx.WithActor(req.Context(), agent, domain.ActorTypeAgent))
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/human-gate-decisions")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, captured.RecordedBy)
	assert.Equal(t, agent, *captured.RecordedBy)
}

// TestHumanGateDecisionHandler_Create_AgentCannotClaimDirect proves an agent
// cannot self-declare provenance=direct to skip attribution — direct is
// reserved for the authenticated user acting as themselves.
func TestHumanGateDecisionHandler_Create_AgentCannotClaimDirect(t *testing.T) {
	taskID, pavel, agent := uuid.New(), uuid.New(), uuid.New()
	mockSvc := &MockCommentService{
		RecordHumanGateDecisionFunc: func(context.Context, domain.RecordHumanGateDecisionInput) (*domain.HumanGateDecision, error) {
			t.Fatal("service must not be called when provenance=direct is claimed by an agent")
			return nil, nil
		},
	}
	h, e := setupHumanGateDecisionTest(mockSvc)

	ref := uuid.New()
	reqBody, _ := json.Marshal(map[string]any{
		"question_ref": ref, "decided_by": pavel, "provenance": "direct", "channel": "mesh",
	})
	req, rec := newHGDRequest(http.MethodPost, "/", string(reqBody))
	req = req.WithContext(actorctx.WithActor(req.Context(), agent, domain.ActorTypeAgent))
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/human-gate-decisions")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHumanGateDecisionHandler_Create_MissingDecidedBy_BadRequest(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockCommentService{}
	h, e := setupHumanGateDecisionTest(mockSvc)

	reqBody, _ := json.Marshal(map[string]any{"provenance": "direct", "channel": "mesh"})
	req, rec := newHGDRequest(http.MethodPost, "/", string(reqBody))
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/human-gate-decisions")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHumanGateDecisionHandler_Create_ServiceErrorMapped(t *testing.T) {
	taskID, pavel := uuid.New(), uuid.New()
	mockSvc := &MockCommentService{
		RecordHumanGateDecisionFunc: func(context.Context, domain.RecordHumanGateDecisionInput) (*domain.HumanGateDecision, error) {
			return nil, service.ErrHumanGateDecisionRepoUnavailable
		},
	}
	h, e := setupHumanGateDecisionTest(mockSvc)

	ref := uuid.New()
	reqBody, _ := json.Marshal(map[string]any{
		"question_ref": ref, "decided_by": pavel, "provenance": "direct", "channel": "mesh",
	})
	req, rec := newHGDRequest(http.MethodPost, "/", string(reqBody))
	req = req.WithContext(actorctx.WithActor(req.Context(), pavel, domain.ActorTypeUser))
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/human-gate-decisions")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestHumanGateDecisionHandler_Create_ValidationError_400NotAny500 pins the
// #62560d6d fix end-to-end at the handler layer: when the service rejects the
// input (e.g. missing question_ref/canonical_key — the live repro's exact
// shape), Create must answer 400, not fall through to the generic 500. Before
// the fix, every handler test here stubbed RecordHumanGateDecisionFunc to
// return either nil or one of the ErrHumanGateDecision* sentinels — none of
// them exercised a *service.HumanGateDecisionValidationError, which is
// exactly how this regression shipped unnoticed.
func TestHumanGateDecisionHandler_Create_ValidationError_400NotAny500(t *testing.T) {
	taskID, pavel := uuid.New(), uuid.New()
	mockSvc := &MockCommentService{
		RecordHumanGateDecisionFunc: func(context.Context, domain.RecordHumanGateDecisionInput) (*domain.HumanGateDecision, error) {
			return nil, &service.HumanGateDecisionValidationError{Msg: "question_ref or canonical_key is required"}
		},
	}
	h, e := setupHumanGateDecisionTest(mockSvc)

	reqBody, _ := json.Marshal(map[string]any{"decided_by": pavel})
	req, rec := newHGDRequest(http.MethodPost, "/", string(reqBody))
	req = req.WithContext(actorctx.WithActor(req.Context(), pavel, domain.ActorTypeUser))
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/human-gate-decisions")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code, "a validation failure must be 400, never the generic 500 fallback")
	assert.Contains(t, rec.Body.String(), "question_ref or canonical_key is required")
}

func TestMapHumanGateDecisionError_ValidationError(t *testing.T) {
	err := mapHumanGateDecisionError(&service.HumanGateDecisionValidationError{Msg: "invalid channel \"carrier-pigeon\""})
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok, "must map to *apierror.Error so handleError's type assertion recognizes it")
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode())
	assert.Equal(t, `invalid channel "carrier-pigeon"`, apiErr.Message)
}

// --- Revoke ---

func TestHumanGateDecisionHandler_Revoke_UserActor_Success(t *testing.T) {
	decisionID, pavel := uuid.New(), uuid.New()
	var captured domain.RevokeHumanGateDecisionInput
	mockSvc := &MockCommentService{
		RevokeHumanGateDecisionFunc: func(_ context.Context, input domain.RevokeHumanGateDecisionInput) error {
			captured = input
			return nil
		},
	}
	h, e := setupHumanGateDecisionTest(mockSvc)

	reqBody, _ := json.Marshal(map[string]any{"reason": "dispute — never confirmed"})
	req, rec := newHGDRequest(http.MethodPost, "/", string(reqBody))
	req = req.WithContext(actorctx.WithActor(req.Context(), pavel, domain.ActorTypeUser))
	c := e.NewContext(req, rec)
	c.SetPath("/human-gate-decisions/:decision_id/revoke")
	c.SetParamNames("decision_id")
	c.SetParamValues(decisionID.String())

	require.NoError(t, h.Revoke(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, decisionID, captured.DecisionID)
	assert.Equal(t, domain.ActorTypeUser, captured.RevokedByType)
}

// TestHumanGateDecisionHandler_Revoke_AgentActor_Forbidden is the handler-level
// mirror of task_handler.go's PATCH {human_gate:false} 403 — an agent must
// never be able to revoke a decision (which re-freezes a task) on its own key.
func TestHumanGateDecisionHandler_Revoke_AgentActor_Forbidden(t *testing.T) {
	decisionID, agent := uuid.New(), uuid.New()
	mockSvc := &MockCommentService{
		RevokeHumanGateDecisionFunc: func(context.Context, domain.RevokeHumanGateDecisionInput) error {
			t.Fatal("service must not be called for a non-user actor")
			return nil
		},
	}
	h, e := setupHumanGateDecisionTest(mockSvc)

	reqBody, _ := json.Marshal(map[string]any{"reason": "trying to self-clear"})
	req, rec := newHGDRequest(http.MethodPost, "/", string(reqBody))
	req = req.WithContext(actorctx.WithActor(req.Context(), agent, domain.ActorTypeAgent))
	c := e.NewContext(req, rec)
	c.SetPath("/human-gate-decisions/:decision_id/revoke")
	c.SetParamNames("decision_id")
	c.SetParamValues(decisionID.String())

	require.NoError(t, h.Revoke(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHumanGateDecisionHandler_Revoke_MissingReason_BadRequest(t *testing.T) {
	decisionID, pavel := uuid.New(), uuid.New()
	mockSvc := &MockCommentService{}
	h, e := setupHumanGateDecisionTest(mockSvc)

	req, rec := newHGDRequest(http.MethodPost, "/", `{}`)
	req = req.WithContext(actorctx.WithActor(req.Context(), pavel, domain.ActorTypeUser))
	c := e.NewContext(req, rec)
	c.SetPath("/human-gate-decisions/:decision_id/revoke")
	c.SetParamNames("decision_id")
	c.SetParamValues(decisionID.String())

	require.NoError(t, h.Revoke(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHumanGateDecisionHandler_Revoke_InvalidDecisionID_BadRequest(t *testing.T) {
	mockSvc := &MockCommentService{}
	h, e := setupHumanGateDecisionTest(mockSvc)

	req, rec := newHGDRequest(http.MethodPost, "/", `{"reason":"x"}`)
	c := e.NewContext(req, rec)
	c.SetPath("/human-gate-decisions/:decision_id/revoke")
	c.SetParamNames("decision_id")
	c.SetParamValues("not-a-uuid")

	require.NoError(t, h.Revoke(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHumanGateDecisionHandler_Revoke_NotFound(t *testing.T) {
	decisionID, pavel := uuid.New(), uuid.New()
	mockSvc := &MockCommentService{
		RevokeHumanGateDecisionFunc: func(context.Context, domain.RevokeHumanGateDecisionInput) error {
			return service.ErrHumanGateDecisionNotFound
		},
	}
	h, e := setupHumanGateDecisionTest(mockSvc)

	req, rec := newHGDRequest(http.MethodPost, "/", `{"reason":"x"}`)
	req = req.WithContext(actorctx.WithActor(req.Context(), pavel, domain.ActorTypeUser))
	c := e.NewContext(req, rec)
	c.SetPath("/human-gate-decisions/:decision_id/revoke")
	c.SetParamNames("decision_id")
	c.SetParamValues(decisionID.String())

	require.NoError(t, h.Revoke(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHumanGateDecisionHandler_Revoke_AlreadyRevoked_Conflict(t *testing.T) {
	decisionID, pavel := uuid.New(), uuid.New()
	mockSvc := &MockCommentService{
		RevokeHumanGateDecisionFunc: func(context.Context, domain.RevokeHumanGateDecisionInput) error {
			return service.ErrHumanGateDecisionAlreadyRevoked
		},
	}
	h, e := setupHumanGateDecisionTest(mockSvc)

	req, rec := newHGDRequest(http.MethodPost, "/", `{"reason":"x"}`)
	req = req.WithContext(actorctx.WithActor(req.Context(), pavel, domain.ActorTypeUser))
	c := e.NewContext(req, rec)
	c.SetPath("/human-gate-decisions/:decision_id/revoke")
	c.SetParamNames("decision_id")
	c.SetParamValues(decisionID.String())

	require.NoError(t, h.Revoke(c))
	assert.Equal(t, http.StatusConflict, rec.Code)
}

// --- List ---

func TestHumanGateDecisionHandler_List_Success(t *testing.T) {
	taskID := uuid.New()
	want := []domain.HumanGateDecision{{ID: uuid.New(), TaskID: taskID}}
	mockSvc := &MockCommentService{
		ListHumanGateDecisionsFunc: func(_ context.Context, gotTaskID uuid.UUID) ([]domain.HumanGateDecision, error) {
			assert.Equal(t, taskID, gotTaskID)
			return want, nil
		},
	}
	h, e := setupHumanGateDecisionTest(mockSvc)

	req, rec := newHGDRequest(http.MethodGet, "/", "")
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/human-gate-decisions")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	var got []domain.HumanGateDecision
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, want[0].ID, got[0].ID)
}

func TestHumanGateDecisionHandler_List_ServiceErrorMapped(t *testing.T) {
	taskID := uuid.New()
	mockSvc := &MockCommentService{
		ListHumanGateDecisionsFunc: func(context.Context, uuid.UUID) ([]domain.HumanGateDecision, error) {
			return nil, service.ErrHumanGateDecisionRepoUnavailable
		},
	}
	h, e := setupHumanGateDecisionTest(mockSvc)

	req, rec := newHGDRequest(http.MethodGet, "/", "")
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/human-gate-decisions")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestHumanGateDecisionHandler_Create_InvalidTaskID(t *testing.T) {
	h, e := setupHumanGateDecisionTest(&MockCommentService{})
	req, rec := newHGDRequest(http.MethodPost, "/", `{}`)
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/human-gate-decisions")
	c.SetParamNames("task_id")
	c.SetParamValues("not-a-uuid")

	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHumanGateDecisionHandler_Create_MalformedBody(t *testing.T) {
	h, e := setupHumanGateDecisionTest(&MockCommentService{})
	req, rec := newHGDRequest(http.MethodPost, "/", `{"decided_by": not-json}`)
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/human-gate-decisions")
	c.SetParamNames("task_id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHumanGateDecisionHandler_Revoke_MalformedBody(t *testing.T) {
	h, e := setupHumanGateDecisionTest(&MockCommentService{})
	req, rec := newHGDRequest(http.MethodPost, "/", `{"reason": not-json}`)
	c := e.NewContext(req, rec)
	c.SetPath("/human-gate-decisions/:decision_id/revoke")
	c.SetParamNames("decision_id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.Revoke(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHumanGateDecisionHandler_List_InvalidTaskID(t *testing.T) {
	h, e := setupHumanGateDecisionTest(&MockCommentService{})
	req, rec := newHGDRequest(http.MethodGet, "/", "")
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/human-gate-decisions")
	c.SetParamNames("task_id")
	c.SetParamValues("not-a-uuid")

	require.NoError(t, h.List(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMapHumanGateDecisionError(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest,
		errStatusCode(mapHumanGateDecisionError(service.ErrHumanGateDecisionCannotRevokeRevocation)))
	other := assert.AnError
	assert.Equal(t, other, mapHumanGateDecisionError(other), "an unrecognized error passes through unchanged")
}

// errStatusCode extracts the HTTP status apierror attaches, so
// TestMapHumanGateDecisionError can assert on the mapped code without
// round-tripping through an echo context.
func errStatusCode(err error) int {
	type statusCoder interface{ StatusCode() int }
	if sc, ok := err.(statusCoder); ok {
		return sc.StatusCode()
	}
	return 0
}
