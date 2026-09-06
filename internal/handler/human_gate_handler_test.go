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
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// Task #4545660b, AC "API-тест на арм/снятие".
//
// Two properties are load-bearing here and are asserted separately on purpose:
//
//  1. gate_author comes from the AUTHENTICATED identity, never from the body. Every one
//     of the 21 implementations this replaces read a claim someone typed; this reads who
//     actually made the call. A test that only checked "the field is populated" would
//     pass on a handler that trusted a body field.
//  2. An incomplete ask is refused with 422 NAMING the field. An unnamed refusal is the
//     shape that made agents lose their first remember() write silently — it gets
//     retried verbatim or read as "I'm not allowed to do this at all".

// allowingPredicateBody is the four-answer set as it arrives over JSON: a genuine stop
// (not reversible), so these tests keep testing what they were written for rather than
// silently turning into predicate tests (task #5d3dc714 added the requirement).
func allowingPredicateBody() map[string]any {
	return map[string]any{
		"credential_exists":     true,
		"credential_reason":     "gateway token is in keys.env",
		"reversible":            false,
		"reversible_reason":     "an outbound payment cannot be un-sent",
		"blocked_by_other_task": false,
		"blocked_reason":        "no other card owns this",
		"customer_visible_now":  false,
		"customer_reason":       "gateway is inactive, nobody can be charged",
	}
}

func armRequest(t *testing.T, h *TaskHandler, taskID, actorID uuid.UUID,
	actorType domain.ActorType, body map[string]any,
) (*httptest.ResponseRecorder, error) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req = req.WithContext(actorctx.WithActor(req.Context(), actorID, actorType))

	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetPath("/tasks/:task_id/human-gate")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	return rec, h.ArmHumanGate(c)
}

func TestArmHumanGateEndpoint_AuthorComesFromIdentityNotBody(t *testing.T) {
	taskID, actorID := uuid.New(), uuid.New()
	bodyClaimedAuthor := uuid.New()

	var captured domain.ArmHumanGateInput
	mockSvc := &MockTaskService{
		ArmHumanGateFunc: func(_ context.Context, in domain.ArmHumanGateInput) error {
			captured = in
			return nil
		},
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
			return &domain.Task{ID: id, HumanGate: true}, nil
		},
	}

	rec, err := armRequest(t, NewTaskHandler(mockSvc), taskID, actorID, domain.ActorTypeAgent,
		map[string]any{
			"reason":              "мёржим сейчас или ждём?",
			"recommended_default": "жду ответа до дедлайна",
			// A caller trying to attribute the ask to somebody else. The field is not
			// in ArmHumanGateRequest at all, so it must be inert.
			"gate_author": bodyClaimedAuthor.String(),
			"predicate":   allowingPredicateBody(),
		})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, actorID, captured.Author, "author is the authenticated caller")
	assert.NotEqual(t, bodyClaimedAuthor, captured.Author,
		"a body-supplied author must be inert — otherwise attribution is forgeable prose again")
	assert.Equal(t, domain.ActorTypeAgent, captured.AuthorType)
	assert.Equal(t, domain.ArmHumanGateSourceAPI, captured.Source)
	assert.Equal(t, "жду ответа до дедлайна", captured.RecommendedDefault)
	assert.Equal(t, domain.HumanGateClassHard, captured.Class, "omitted class → hard, fail-closed")
}

func TestArmHumanGateEndpoint_IncompleteAskIs422NamingTheField(t *testing.T) {
	taskID, actorID := uuid.New(), uuid.New()
	armCalled := false
	mockSvc := &MockTaskService{
		ArmHumanGateFunc: func(_ context.Context, in domain.ArmHumanGateInput) error {
			armCalled = true
			// The real validation, exercised through the real input type — not a
			// stubbed error, which would prove only that the handler can map one.
			normalized := in.Normalized()
			return normalized.Validate()
		},
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
			return &domain.Task{ID: id}, nil
		},
	}

	rec, err := armRequest(t, NewTaskHandler(mockSvc), taskID, actorID, domain.ActorTypeAgent,
		map[string]any{"reason": "мёржим?", "predicate": allowingPredicateBody()}) // no recommended_default
	require.NoError(t, err)
	require.True(t, armCalled, "the request must reach validation, not be dropped earlier")

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "recommended_default", resp["field"],
		"the refusal must say WHICH field — an unnamed refusal gets retried verbatim")
	assert.Contains(t, resp["message"], "recommended_default")

	// POSITIVE CONTROL on the same handler and the same validator: adding the one
	// missing field turns the 422 into a 200. Without this, a handler that returned 422
	// unconditionally would pass the assertions above.
	rec2, err := armRequest(t, NewTaskHandler(mockSvc), taskID, actorID, domain.ActorTypeAgent,
		map[string]any{"reason": "мёржим?", "recommended_default": "жду",
			"predicate": allowingPredicateBody()})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec2.Code, "the same request WITH a default must be accepted")
}

// TestClearHumanGateEndpoint_AgentRefusedWithNamedExit: the new DELETE route must not be
// a weaker second door onto the same wall. It reuses the SAME user-only check and the
// SAME refusal text as PATCH {human_gate:false}, so an agent that lands here is told the
// exit it can actually reach instead of escalating "I'm blocked" to a human.
func TestClearHumanGateEndpoint_AgentRefusedWithNamedExit(t *testing.T) {
	taskID, agentID := uuid.New(), uuid.New()
	clearCalled := false
	mockSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
			return &domain.Task{ID: id, HumanGate: true}, nil
		},
		ClearHumanGateFunc: func(context.Context, uuid.UUID) error {
			clearCalled = true
			return nil
		},
	}
	h := NewTaskHandler(mockSvc).WithCommentService(&MockCommentService{})

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	req = req.WithContext(actorctx.WithActor(req.Context(), agentID, domain.ActorTypeAgent))
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetPath("/tasks/:task_id/human-gate")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.ClearHumanGate(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, clearCalled, "the wall stands: the gate was not cleared anyway")

	// POSITIVE CONTROL: a human user on the identical request clears it. Without this,
	// a handler that refused everyone would look identical to a correct one.
	req2 := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	req2 = req2.WithContext(actorctx.WithActor(req2.Context(), uuid.New(), domain.ActorTypeUser))
	rec2 := httptest.NewRecorder()
	c2 := echo.New().NewContext(req2, rec2)
	c2.SetPath("/tasks/:task_id/human-gate")
	c2.SetParamNames("task_id")
	c2.SetParamValues(taskID.String())

	require.NoError(t, h.ClearHumanGate(c2))
	assert.Equal(t, http.StatusOK, rec2.Code)
	assert.True(t, clearCalled, "a human on the same route does clear it")
}
