package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// ListByTaskBothDirections returns (outgoing, incoming): the FIRST slot holds the
// edges where the task is the dependent (its requirements), as the service documents.
//
// AC5: a confirmed condition change is seen by the consumer as a new generation,
// prep proceeds, and the final gate stays blocked until real evidence.
func TestDependencyHandler_NextAction_ConditionChange(t *testing.T) {
	taskID, blocker, edgeID := uuid.New(), uuid.New(), uuid.New()
	cond := domain.DependencyConditionTaskDone
	svc := &MockTaskDependencyService{}
	svc.ListByTaskBothDirectionsFunc = func(context.Context, uuid.UUID) ([]domain.EnrichedTaskDependency, []domain.EnrichedTaskDependency, error) {
		d := domain.TaskDependency{ID: edgeID, TaskID: taskID, DependsOnTaskID: blocker, DependencyType: domain.DependencyTypeBlocks, Condition: cond}
		return []domain.EnrichedTaskDependency{{TaskDependency: d}}, nil, nil
	}
	h, e := setupDependencyTest(svc)
	get := func() domain.NextActionProjection {
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
		c.SetParamNames("task_id")
		c.SetParamValues(taskID.String())
		require.NoError(t, h.NextAction(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var p domain.NextActionProjection
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
		return p
	}
	before := get()
	assert.False(t, before.Stages[0].Ready)
	assert.Equal(t, "wait_requirement", before.NextAction)

	cond = domain.DependencyConditionDeployed // owner confirmed the real binding
	after := get()
	assert.NotEqual(t, before.Generation, after.Generation)
	assert.True(t, domain.ShouldWake(before.Generation, after.Generation))
	assert.True(t, after.Stages[0].Ready, "exactly one permitted continuation: prep")
	assert.False(t, after.Stages[1].Ready, "final gate stays")
	assert.Equal(t, "continue_prep", after.NextAction)
	assert.False(t, domain.ShouldWake(after.Generation, get().Generation), "no repeat wake on an unchanged tick")
}

// The live repository fills ConditionSatisfied with the full verdict
// (DependencyReleasedSQL, status-aware); the projection must honour it so a
// done blocker releases a task_done/accepted edge without a BlockerState.
func TestDependencyHandler_NextAction_UsesStoredVerdict(t *testing.T) {
	taskID := uuid.New()
	svc := &MockTaskDependencyService{}
	svc.ListByTaskBothDirectionsFunc = func(context.Context, uuid.UUID) ([]domain.EnrichedTaskDependency, []domain.EnrichedTaskDependency, error) {
		mk := func(c domain.DependencyCondition, ok bool) domain.EnrichedTaskDependency {
			return domain.EnrichedTaskDependency{TaskDependency: domain.TaskDependency{ID: uuid.New(), TaskID: taskID, DependsOnTaskID: uuid.New(),
				DependencyType: domain.DependencyTypeBlocks, Condition: c, ConditionSatisfied: ok}}
		}
		return []domain.EnrichedTaskDependency{mk(domain.DependencyConditionTaskDone, true), mk(domain.DependencyConditionAccepted, true)}, nil, nil
	}
	h, e := setupDependencyTest(svc)
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	require.NoError(t, h.NextAction(c))
	var p domain.NextActionProjection
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	assert.True(t, p.Stages[0].Ready && p.Stages[1].Ready)
	assert.Equal(t, "final_acceptance", p.NextAction)
	for _, r := range p.Requires {
		assert.True(t, r.Released)
	}
}

// AC4 on the live path: a done blocker whose recorded SHIP is for another head
// than its current head does not release the edge, even though the stored
// condition_satisfied says true. A live link with a head but no ship_sha (no SHIP
// recorded; DO-NOT-SHIP is not stored) holds the edge too; a closed link is ignored.
func TestDependencyHandler_NextAction_StaleShipHoldsEdge(t *testing.T) {
	taskID, blocker := uuid.New(), uuid.New()
	svc := &MockTaskDependencyService{}
	svc.ListByTaskBothDirectionsFunc = func(context.Context, uuid.UUID) ([]domain.EnrichedTaskDependency, []domain.EnrichedTaskDependency, error) {
		return []domain.EnrichedTaskDependency{{TaskDependency: domain.TaskDependency{ID: uuid.New(), TaskID: taskID, DependsOnTaskID: blocker,
			DependencyType: domain.DependencyTypeBlocks, Condition: domain.DependencyConditionAccepted, ConditionSatisfied: true}}}, nil, nil
	}
	meta := func(head, ship string) []domain.VCSLink {
		m := map[string]string{"head_sha": head}
		if ship != "" {
			m["ship_sha"] = ship
		}
		b, _ := json.Marshal(m)
		return []domain.VCSLink{{LinkType: domain.VCSLinkTypePR, Metadata: b}}
	}
	run := func(links []domain.VCSLink) domain.NextActionProjection {
		h, e := setupDependencyTest(svc)
		h.WithVCSLinks(func(context.Context, uuid.UUID) ([]domain.VCSLink, error) { return links, nil })
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
		mw.SetProjectAccess(c, mw.ProjectAccessCheck{Task: func(uuid.UUID) bool { return true }})
		c.SetParamNames("task_id")
		c.SetParamValues(taskID.String())
		require.NoError(t, h.NextAction(c))
		var p domain.NextActionProjection
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
		return p
	}
	assert.True(t, run(meta("aaa", "aaa")).Stages[1].Ready, "SHIP for current head releases")
	noShip := run(meta("aaa", ""))
	assert.False(t, noShip.Stages[1].Ready, "no SHIP recorded for a live PR must not release")
	assert.Contains(t, noShip.Requires[0].Reason, "NO-SHIP-RECORDED")
	closed := meta("aaa", "")
	closed[0].Status = domain.VCSLinkStatusClosed
	assert.True(t, run(closed).Stages[1].Ready, "an abandoned (closed) MR link is not evidence")
	stale := run(meta("bbb", "aaa"))
	assert.False(t, stale.Stages[1].Ready, "SHIP for another head must not release")
	assert.NotEmpty(t, stale.Requires[0].Reason)
}
