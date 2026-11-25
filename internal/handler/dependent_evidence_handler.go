package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// DependentEdgeScopeReader tells the blocker side of an edge whether the edge is
// its own, crosses workspaces and was declared by a typed card wait.
type DependentEdgeScopeReader interface {
	DependentEdgeScope(ctx context.Context, blockerID, edgeID uuid.UUID) (domain.DependentEdgeScope, error)
}

// WithDependentEdgeScope wires the edge scope read behind UpdateEvidenceFromBlocker;
// without it the route answers 404 for every edge.
func (h *DependencyHandler) WithDependentEdgeScope(r DependentEdgeScopeReader) *DependencyHandler {
	h.edgeScope = r
	return h
}

// dependentEvidenceRequest is the blocker side's body: evidence only. Condition
// and criterion are decoded raw so that their mere presence — even null — is
// refused: what the edge waits for belongs to the waiter.
type dependentEvidenceRequest struct {
	EvidenceURL        *string         `json:"evidence_url"`
	EvidenceCriterion  string          `json:"evidence_criterion"`
	Attest             bool            `json:"attest"`
	HeadSHA            string          `json:"head_sha"`
	Environment        string          `json:"environment"`
	ExpectedGeneration *int64          `json:"expected_generation"`
	Condition          json.RawMessage `json:"condition"`
	Criterion          json.RawMessage `json:"criterion"`
}

// dependentEvidenceResponse is the edge as the blocker side may see it: the edge
// id, its criterion and evidence state. The dependent's title, status and owning
// wait are not part of it.
type dependentEvidenceResponse struct {
	ID                  uuid.UUID                  `json:"id"`
	TaskID              uuid.UUID                  `json:"task_id"`
	DependsOnTaskID     uuid.UUID                  `json:"depends_on_task_id"`
	Condition           domain.DependencyCondition `json:"condition"`
	Criterion           string                     `json:"criterion"`
	Generation          int64                      `json:"generation"`
	EvidenceURL         *string                    `json:"evidence_url,omitempty"`
	EvidenceStatus      domain.EvidenceStatus      `json:"evidence_status"`
	EvidenceReason      *string                    `json:"evidence_reason,omitempty"`
	EvidenceHeadSHA     *string                    `json:"evidence_head_sha,omitempty"`
	EvidenceEnvironment *string                    `json:"evidence_environment,omitempty"`
	ConditionMetAt      *time.Time                 `json:"condition_met_at,omitempty"`
	ConditionSatisfied  bool                       `json:"condition_satisfied"`
	ReleasedBy          string                     `json:"released_by,omitempty"`
}

// UpdateEvidenceFromBlocker handles
// PATCH /tasks/:task_id/dependents/:dependent_edge_id/evidence, where :task_id
// is the BLOCKER. It is how the owner of a blocker records the named result an
// edge waits for when the waiting card lives in another workspace: the
// ordinary PATCH /tasks/:waiter/dependencies/:dep_id is scoped to the waiter's
// workspace, which the blocker's key never passes.
//
// The entry is bound to the blocker: the route guards resolve both path ids to
// the blocker's workspace and project, and MayAccessTask(blocker) decides who
// attests. Across workspaces only an edge a typed card wait declared
// (wait_agent_id) is reachable; every other refusal answers the same 404, so
// the route is no oracle for edges of other tenants.
func (h *DependencyHandler) UpdateEvidenceFromBlocker(c echo.Context) error {
	notFound := func() error { return c.JSON(http.StatusNotFound, apierror.NotFound("TaskDependency")) }
	blockerID, err := uuid.Parse(c.Param("task_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}
	edgeID, err := uuid.Parse(c.Param("dependent_edge_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid dependent_edge_id"))
	}
	var req dependentEvidenceRequest
	if bindErr := json.NewDecoder(c.Request().Body).Decode(&req); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	if req.Condition != nil || req.Criterion != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest(
			"condition and criterion are set by the waiting card's side; the blocker side writes evidence only"))
	}
	if req.EvidenceURL == nil || strings.TrimSpace(*req.EvidenceURL) == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("evidence_url is required"))
	}
	if h.edgeScope == nil || !middleware.MayAccessTask(c, blockerID) {
		return notFound()
	}
	ctx := c.Request().Context()
	scope, err := h.edgeScope.DependentEdgeScope(ctx, blockerID, edgeID)
	if err != nil {
		return handleError(c, err)
	}
	if !scope.Found || (scope.CrossWorkspace && !scope.ServerObserved) {
		return notFound()
	}
	var actor *uuid.UUID
	if id, aerr := middleware.GetAgentID(c); aerr == nil {
		actor = &id
	} else if id, uerr := middleware.GetUserID(c); uerr == nil {
		actor = &id
	}
	if actor == nil {
		return notFound()
	}
	dep, err := h.depService.UpdateCondition(ctx, edgeID, service.UpdateDependencyConditionInput{
		EvidenceURL: req.EvidenceURL, EvidenceCriterion: req.EvidenceCriterion,
		HeadSHA: req.HeadSHA, Environment: req.Environment, Attest: req.Attest,
		ActorID: actor, ActorAuthorized: true, ExpectedGeneration: req.ExpectedGeneration,
		RequireVerifiable: true,
	})
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, dependentEvidenceResponse{
		ID: dep.ID, TaskID: dep.TaskID, DependsOnTaskID: dep.DependsOnTaskID,
		Condition: dep.Condition, Criterion: dep.EffectiveCriterion(), Generation: dep.Generation,
		EvidenceURL: dep.EvidenceURL, EvidenceStatus: dep.EvidenceStatus, EvidenceReason: dep.EvidenceReason,
		EvidenceHeadSHA: dep.EvidenceHeadSHA, EvidenceEnvironment: dep.EvidenceEnvironment,
		ConditionMetAt: dep.ConditionMetAt, ConditionSatisfied: dep.ConditionSatisfied, ReleasedBy: dep.ReleasedBy,
	})
}
