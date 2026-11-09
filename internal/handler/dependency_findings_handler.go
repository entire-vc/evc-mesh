package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// FindingsVersion is the schema version of the dependency-findings response.
const FindingsVersion = 1

// DependencyFindingsResponse is what GET /projects/:proj_id/dependency-findings returns.
// Revision is the graph revision the findings were computed from, so a consumer can tell a
// stale diagnosis from a current one.
type DependencyFindingsResponse struct {
	Version  int              `json:"version"`
	Revision string           `json:"revision"`
	Findings []domain.Finding `json:"findings"`
}

// DependencyFindings handles GET /projects/:proj_id/dependency-findings: the production
// caller of domain.GraphFindings. It reads the same actor-scoped snapshot as the dependency
// graph (restricted nodes keep identity and connectivity only), so a caller never learns of a
// task or owner they may not open. An edge counts as holding only when its full released
// verdict (evidence OR the blocker's own state for the condition) is false. Findings are
// advisory: nothing here deletes, retargets or satisfies an edge.
func (h *DependencyHandler) DependencyFindings(c echo.Context) error {
	projectID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return handleError(c, apierror.BadRequest("invalid proj_id"))
	}
	if h.graphReader == nil {
		return handleError(c, apierror.InternalError("dependency graph reader unavailable"))
	}
	scope := domain.GraphScope{Agent: middleware.IsAgent(c)}
	scope.WorkspaceID, err = middleware.GetWorkspaceID(c)
	if err != nil {
		return handleError(c, apierror.Forbidden("workspace context required"))
	}
	if scope.Agent {
		scope.ActorID, err = middleware.GetAgentID(c)
	} else {
		scope.ActorID, err = middleware.GetUserID(c)
		role, _ := c.Get(middleware.ContextKeyWorkspaceRole).(string)
		scope.Admin = role == domain.RoleAdmin || role == domain.RoleOwner
	}
	if err != nil {
		return handleError(c, apierror.Forbidden("actor context required"))
	}
	snapshot, err := h.graphReader.ReadGraph(c.Request().Context(), projectID, uuid.Nil, scope)
	if err != nil {
		return handleError(c, err)
	}
	owners := make(map[uuid.UUID]string, len(snapshot.Nodes))
	for _, n := range snapshot.Nodes {
		if n.AssigneeName != "" {
			owners[n.ID] = n.AssigneeName
		}
	}
	// ReadGraph gives only the evidence half of the verdict for an edge whose blocker the actor
	// may not open (the blocker's own state must not leak through the edge). That is not the
	// full released verdict, so such an edge is left out: a finding is never built on a state
	// that cannot be read.
	available := make(map[uuid.UUID]bool, len(snapshot.Nodes))
	for _, n := range snapshot.Nodes {
		available[n.ID] = n.Availability == "available"
	}
	readable := make([]domain.TaskDependency, 0, len(snapshot.Dependencies))
	for _, d := range snapshot.Dependencies {
		if available[d.TaskID] && available[d.DependsOnTaskID] {
			readable = append(readable, d)
		}
	}
	findings := domain.GraphFindings(readable, func(d domain.TaskDependency) bool {
		return d.ConditionSatisfied
	}, owners)
	if findings == nil {
		findings = []domain.Finding{}
	}
	return c.JSON(http.StatusOK, DependencyFindingsResponse{Version: FindingsVersion, Revision: snapshot.Revision, Findings: findings})
}
