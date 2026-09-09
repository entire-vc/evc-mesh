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
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Task #f933dc05 — DELETE /tasks/:id/human-gate opens for exactly ONE new caller: the
// agent that armed this gate through the API and that therefore owns the ask nobody
// else could take down.
//
// Read the file as a matched set, not as a list. The first test is the new capability;
// every other test is a wall that must still stand, and they are what makes the first
// one mean anything. A change that simply dropped the user-only check would pass
// TestClearHumanGate_OwnAPIArm_AgentMayClear and fail everything after it.

// clearEnv drives one DELETE and reports what happened.
type clearEnv struct {
	rec           *httptest.ResponseRecorder
	clearCalled   bool
	comments      []*domain.Comment
	refusalReason string
}

func runClear(t *testing.T, info *domain.HumanGateInfo, ownerErr error, actorID uuid.UUID,
	actorType domain.ActorType, wireCommentService bool,
) clearEnv {
	t.Helper()
	taskID := uuid.New()
	env := clearEnv{}
	mockSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
			return &domain.Task{ID: id, HumanGate: true}, nil
		},
		ClearHumanGateFunc: func(context.Context, uuid.UUID) error {
			env.clearCalled = true
			return nil
		},
	}

	h := NewTaskHandler(mockSvc)
	if wireCommentService {
		h = h.WithCommentService(&MockCommentService{
			GetHumanGateOwnerFunc: func(context.Context, uuid.UUID) (*domain.HumanGateInfo, error) {
				return info, ownerErr
			},
			CreateFunc: func(_ context.Context, c *domain.Comment) error {
				env.comments = append(env.comments, c)
				return nil
			},
		})
	}

	req := httptest.NewRequest(http.MethodDelete, "/", http.NoBody)
	req = req.WithContext(actorctx.WithActor(req.Context(), actorID, actorType))
	env.rec = httptest.NewRecorder()
	c := echo.New().NewContext(req, env.rec)
	c.SetPath("/tasks/:task_id/human-gate")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.ClearHumanGate(c))
	if env.rec.Code == http.StatusForbidden {
		var apiErr apierror.Error
		require.NoError(t, json.Unmarshal(env.rec.Body.Bytes(), &apiErr))
		env.refusalReason = apiErr.Message
	}
	return env
}

func apiArmInfo(owner uuid.UUID) *domain.HumanGateInfo {
	return &domain.HumanGateInfo{
		Gated: true, OwnerAgentID: &owner, OwnerName: "some-agent",
		ClearableByOwner: true, ClearPath: domain.HumanGateClearPathClearEndpoint,
	}
}

// TestClearHumanGate_OwnAPIArm_AgentMayClear — the capability this card adds.
func TestClearHumanGate_OwnAPIArm_AgentMayClear(t *testing.T) {
	owner := uuid.New()
	env := runClear(t, apiArmInfo(owner), nil, owner, domain.ActorTypeAgent, true)

	assert.Equal(t, http.StatusOK, env.rec.Code)
	assert.True(t, env.clearCalled, "the gate its own author armed comes down for that author")

	// The release must leave a trace. A marker withdrawal is itself a comment, so it
	// is self-documenting; a DELETE is not, and a release nobody can see is how "the
	// gate came down and nobody knows why" becomes unanswerable weeks later.
	require.Len(t, env.comments, 1, "clearing your own API arm must be recorded on the thread")
	audit := env.comments[0]
	assert.Equal(t, domain.ActorTypeSystem, audit.AuthorType,
		"system-authored: the one authorship no caller can forge through the comment API")
	assert.Equal(t, uuid.Nil, audit.AuthorID)
	assert.Contains(t, audit.Body, owner.String(), "the record must name who released it")

	// The audit comment must not be readable as an arm or as a withdrawal by the
	// scans that walk this thread — it is neither.
	assert.NotContains(t, audit.Body, "Blocking @")
	for _, negator := range []string{"не нужен", "не требуется", "resolved", "withdrawn"} {
		assert.NotContains(t, strings.ToLower(audit.Body), negator,
			"the audit comment must not read as a withdrawal negator")
	}
	assert.NotContains(t, audit.Body, "human_gate взведён напрямую",
		"and must not read as a raw-arm marker either")
}

// TestClearHumanGate_ForeignAPIArm_Refused — ownership is the whole permission. An
// agent that did not arm this gate is refused exactly as before.
func TestClearHumanGate_ForeignAPIArm_Refused(t *testing.T) {
	env := runClear(t, apiArmInfo(uuid.New()), nil, uuid.New(), domain.ActorTypeAgent, true)

	assert.Equal(t, http.StatusForbidden, env.rec.Code)
	assert.False(t, env.clearCalled)
	assert.Empty(t, env.comments, "a refused clear leaves no release record")
}

// TestClearHumanGate_MarkerArmedGate_StillRefusedEvenForItsOwner is the control the
// card names as load-bearing (AC3): a real "❓ Blocking @pavel" ask must NOT become
// clearable by an agent key. Its owner is the actor here — the most favourable case a
// widening would exploit — and the refusal must still stand, because that ask has its
// own exit and this endpoint is not it.
func TestClearHumanGate_MarkerArmedGate_StillRefusedEvenForItsOwner(t *testing.T) {
	owner := uuid.New()
	markerInfo := &domain.HumanGateInfo{
		Gated: true, OwnerAgentID: &owner, OwnerName: "some-agent",
		ClearableByOwner: true, ClearPath: domain.HumanGateClearPathWithdrawMarker,
	}
	env := runClear(t, markerInfo, nil, owner, domain.ActorTypeAgent, true)

	assert.Equal(t, http.StatusForbidden, env.rec.Code,
		"a marker-armed ask comes down by withdrawal, not by this endpoint")
	assert.False(t, env.clearCalled)
	assert.Contains(t, env.refusalReason, "WITHDRAWING",
		"and the refusal must point at the door that IS open to this caller")
}

// TestClearHumanGate_NoLiveMarkerNoAuthor_Refused — the raw PATCH/UI arm, the one
// shape for which "no withdrawal path by construction" was always literally true.
func TestClearHumanGate_NoLiveMarkerNoAuthor_Refused(t *testing.T) {
	env := runClear(t, &domain.HumanGateInfo{Gated: true, ReasonIfNot: "no_live_marker"},
		nil, uuid.New(), domain.ActorTypeAgent, true)

	assert.Equal(t, http.StatusForbidden, env.rec.Code)
	assert.False(t, env.clearCalled)
	assert.Contains(t, env.refusalReason, "no live marker")
}

// TestClearHumanGate_OwnershipLookupFails_Refused — "I could not check" must never
// read as "checked, go ahead".
func TestClearHumanGate_OwnershipLookupFails_Refused(t *testing.T) {
	owner := uuid.New()
	env := runClear(t, nil, assert.AnError, owner, domain.ActorTypeAgent, true)

	assert.Equal(t, http.StatusForbidden, env.rec.Code)
	assert.False(t, env.clearCalled)
}

// TestClearHumanGate_NoCommentServiceWired_Refused — same fail-closed posture when the
// dependency that answers "who owns this" is absent entirely.
func TestClearHumanGate_NoCommentServiceWired_Refused(t *testing.T) {
	owner := uuid.New()
	env := runClear(t, apiArmInfo(owner), nil, owner, domain.ActorTypeAgent, false)

	assert.Equal(t, http.StatusForbidden, env.rec.Code)
	assert.False(t, env.clearCalled)
}

// TestClearHumanGate_User_ClearsWithoutAuditComment — a user could always clear, and
// still does. No release record is written on this path: the PATCH route already posts
// its own, and the audit comment added here is specifically about an AGENT releasing a
// gate, which is the new thing worth seeing on the thread.
func TestClearHumanGate_User_ClearsWithoutAuditComment(t *testing.T) {
	env := runClear(t, apiArmInfo(uuid.New()), nil, uuid.New(), domain.ActorTypeUser, true)

	assert.Equal(t, http.StatusOK, env.rec.Code)
	assert.True(t, env.clearCalled)
	assert.Empty(t, env.comments)
}

// TestGateClearRefusal_APIArmOwner_NamesTheEndpointNotTheNegator — the PATCH refusal
// must not send an API arm's owner to the withdrawal path. That path returns early
// unless the comment scan found a marker, so following the advice would produce a
// comment, no error, and no release: the silent no-op that has cost the fleet a gate
// at least five times.
func TestGateClearRefusal_APIArmOwner_NamesTheEndpointNotTheNegator(t *testing.T) {
	owner := uuid.New()
	h := NewTaskHandler(&MockTaskService{}).WithCommentService(&MockCommentService{
		GetHumanGateOwnerFunc: func(context.Context, uuid.UUID) (*domain.HumanGateInfo, error) {
			return apiArmInfo(owner), nil
		},
	})

	msg := h.gateClearRefusal(context.Background(), &domain.Task{ID: uuid.New(), HumanGate: true}, owner)

	assert.Contains(t, msg, "clear_human_gate")
	assert.Contains(t, msg, "silent no-op")
	assert.NotContains(t, msg, "WITHDRAWING",
		"the negator advice belongs to marker-armed gates only")
}
