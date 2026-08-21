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
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Task #4c448e11. The 403 an agent gets when it PATCHes human_gate true→false
// is CORRECT and stays; what was wrong is that it named no alternative, so
// agents concluded the gate has no agent-reachable exit at all and said so in a
// P1 escalation — while the prod audit trail showed the agent-reachable exit
// (withdrawal) releasing MORE gates than the human one.
//
// Every case below asserts two things that must never come apart:
//   1. the outcome is still 403 and the task was NOT updated (the wall stands), and
//   2. the message names the exit that is actually reachable for THIS caller.
//
// (1) alone is the pre-existing behaviour; (2) alone would be a permission
// widening dressed as a message change. Only both together are the fix.

// gateRefusalCase drives one PATCH {"human_gate": false} as an agent.
func runGateRefusal(t *testing.T, actorID uuid.UUID, mockComment *MockCommentService) (status int, message string, taskUpdated bool) {
	t.Helper()
	taskID := uuid.New()
	existingTask := &domain.Task{ID: taskID, HumanGate: true}
	updateCalled := false
	mockSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, _ uuid.UUID) (*domain.Task, error) {
			return existingTask, nil
		},
		UpdateFunc: func(_ context.Context, _ *domain.Task) error {
			updateCalled = true
			return nil
		},
	}

	e := echo.New()
	h := NewTaskHandler(mockSvc).WithCommentService(mockComment)

	body, _ := json.Marshal(map[string]interface{}{"human_gate": false})
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req = req.WithContext(actorctx.WithActor(req.Context(), actorID, domain.ActorTypeAgent))

	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.Update(c))

	var apiErr apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	return rec.Code, apiErr.Message, updateCalled
}

func ownerInfo(owner uuid.UUID, name string, clearable bool, reason string) *MockCommentService {
	return &MockCommentService{
		GetHumanGateOwnerFunc: func(_ context.Context, _ uuid.UUID) (*domain.HumanGateInfo, error) {
			return &domain.HumanGateInfo{
				Gated:            true,
				OwnerAgentID:     &owner,
				OwnerName:        name,
				ClearableByOwner: clearable,
				ReasonIfNot:      reason,
			}, nil
		},
	}
}

// The caller owns the live ask and could withdraw it right now. This is the
// exact shape of #b2e6578a, where three agents in a row were told "clear it
// yourself" and the API told them only that they were not a user.
func TestGateRefusal_OwnerIsToldHowToWithdraw(t *testing.T) {
	me := uuid.New()
	code, msg, updated := runGateRefusal(t, me, ownerInfo(me, "Wally", true, ""))

	assert.Equal(t, http.StatusForbidden, code)
	assert.False(t, updated, "the wall must still stand — the task must not be updated")

	// Names the mechanism...
	assert.Contains(t, msg, "WITHDRAW")
	assert.Contains(t, msg, "LAST PARAGRAPH")
	// ...with words the server's negator list actually matches. An agent that
	// writes an English "not needed" gets silence, so the message must not
	// leave the vocabulary to guesswork.
	assert.Contains(t, msg, "не требуется")
	// ...and the trap that silently re-arms the gate instead of clearing it.
	assert.Contains(t, msg, "re-arms")
}

// A different agent owns the ask. Withdrawal is not available to this caller,
// and — the part that keeps costing days — telling the owner in a comment does
// not reach them, because a gated task is not fed to any lane.
func TestGateRefusal_ForeignOwner_NamesOwnerAndTheDeliveryTrap(t *testing.T) {
	code, msg, updated := runGateRefusal(t, uuid.New(), ownerInfo(uuid.New(), "Howard", true, ""))

	assert.Equal(t, http.StatusForbidden, code)
	assert.False(t, updated)
	assert.Contains(t, msg, "Howard")
	assert.Contains(t, msg, "will NOT wake them")
	assert.Contains(t, msg, "human-gate-decisions")
}

// Raw-armed / UI-armed gate: there is no author, so withdrawal genuinely does
// not exist here. The message must say so rather than sending the caller to
// hunt for a marker that was never written.
func TestGateRefusal_NoLiveMarker_SaysNoWithdrawalPathExists(t *testing.T) {
	mock := &MockCommentService{
		GetHumanGateOwnerFunc: func(_ context.Context, _ uuid.UUID) (*domain.HumanGateInfo, error) {
			return &domain.HumanGateInfo{Gated: true, ReasonIfNot: "no_live_marker"}, nil
		},
	}
	code, msg, updated := runGateRefusal(t, uuid.New(), mock)

	assert.Equal(t, http.StatusForbidden, code)
	assert.False(t, updated)
	assert.Contains(t, msg, "no_live_marker")
	assert.Contains(t, msg, "no withdrawal")
	assert.Contains(t, msg, "human-gate-decisions")
}

// Both non-empty reasons are time-based: the withdrawal unlocks on its own.
// The message must say "wait", because the natural reading of a refusal is
// "do something else", and doing something else here means pinging Pavel.
func TestGateRefusal_ReaffirmPending_TellsOwnerToRetryNotEscalate(t *testing.T) {
	me := uuid.New()
	code, msg, updated := runGateRefusal(t, me, ownerInfo(me, "Garfield", false, "reaffirm_pending"))

	assert.Equal(t, http.StatusForbidden, code)
	assert.False(t, updated)
	assert.Contains(t, msg, "reaffirm_pending")
	assert.Contains(t, msg, "retry later")
	assert.Contains(t, msg, "no action needed")
}

func TestGateRefusal_RawArmed_TellsOwnerToRetryNotEscalate(t *testing.T) {
	me := uuid.New()
	code, msg, updated := runGateRefusal(t, me, ownerInfo(me, "Garfield", false, "raw_armed"))

	assert.Equal(t, http.StatusForbidden, code)
	assert.False(t, updated)
	assert.Contains(t, msg, "raw_armed")
	assert.Contains(t, msg, "retry later")
}

// Fail-safe. A hint is a nicety; a lookup failure must not turn the 403 into a
// 500, and must not produce a refusal that names nothing at all.
func TestGateRefusal_LookupFailure_StillRefusesAndStillNamesBothExits(t *testing.T) {
	mock := &MockCommentService{
		GetHumanGateOwnerFunc: func(_ context.Context, _ uuid.UUID) (*domain.HumanGateInfo, error) {
			return nil, errors.New("db down")
		},
	}
	code, msg, updated := runGateRefusal(t, uuid.New(), mock)

	assert.Equal(t, http.StatusForbidden, code, "a failed hint lookup must not change the outcome")
	assert.False(t, updated)
	assert.Contains(t, msg, "withdraw")
	assert.Contains(t, msg, "human-gate-decisions")
}

// Same fail-safe for the (nil, nil) shape — a comment service wired but with
// nothing to say. Returning nil info must not dereference.
func TestGateRefusal_NilInfo_FallsBackWithoutPanicking(t *testing.T) {
	code, msg, updated := runGateRefusal(t, uuid.New(), &MockCommentService{})

	assert.Equal(t, http.StatusForbidden, code)
	assert.False(t, updated)
	assert.Contains(t, msg, "human-gate-decisions")
}

// NEGATIVE CONTROL (task #4c448e11 AC4). Withdrawing an ask and APPROVING it
// are different things, and the second must stay with a human. Whatever the
// gate's ownership state, an agent's PATCH is refused and the task is never
// written. If this test ever goes green while asserting 200, the message
// change has silently become a permission change.
func TestGateRefusal_AgentCanNeverActuallyClearTheGate(t *testing.T) {
	me := uuid.New()
	other := uuid.New()
	cases := map[string]*MockCommentService{
		"owner, clearable":     ownerInfo(me, "Me", true, ""),
		"owner, not clearable": ownerInfo(me, "Me", false, "raw_armed"),
		"foreign owner":        ownerInfo(other, "Other", true, ""),
		"no live marker": {GetHumanGateOwnerFunc: func(_ context.Context, _ uuid.UUID) (*domain.HumanGateInfo, error) {
			return &domain.HumanGateInfo{Gated: true, ReasonIfNot: "no_live_marker"}, nil
		}},
		"lookup failed": {GetHumanGateOwnerFunc: func(_ context.Context, _ uuid.UUID) (*domain.HumanGateInfo, error) {
			return nil, errors.New("boom")
		}},
	}
	for name, mock := range cases {
		t.Run(name, func(t *testing.T) {
			code, _, updated := runGateRefusal(t, me, mock)
			assert.Equal(t, http.StatusForbidden, code, "agent must never clear a gate by PATCH")
			assert.False(t, updated, "task must not be written on a refused gate clear")
		})
	}
}
