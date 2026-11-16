package handler

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// GitLabFailedPipelinesWindow is how far back the failed-pipelines widget looks.
const GitLabFailedPipelinesWindow = 24 * time.Hour

const gitlabFailedPipelinesLimit = 50

type widgetAvailability interface {
	WidgetsAvailable(ctx context.Context, workspaceID uuid.UUID, extensionID string) (bool, error)
}

type failedPipelineReader interface {
	FailedMergeRequestPipelines(ctx context.Context, workspaceID, userID uuid.UUID, projectIDs []uuid.UUID, since time.Time, limit int) ([]postgres.FailedPipeline, error)
}

// GitLabWidgetHandler serves the data of the GitLab extension's dashboard
// widget "Failed pipelines of my MRs" from the VCS links tasks already carry.
type GitLabWidgetHandler struct {
	widgets  widgetAvailability
	projects extensions.WidgetProjects
	links    failedPipelineReader
	now      func() time.Time
}

// NewGitLabWidgetHandler creates the handler.
func NewGitLabWidgetHandler(widgets widgetAvailability, projects extensions.WidgetProjects, links failedPipelineReader) *GitLabWidgetHandler {
	return &GitLabWidgetHandler{widgets: widgets, projects: projects, links: links, now: time.Now}
}

type failedPipelinesResponse struct {
	Since time.Time                 `json:"since"`
	Items []postgres.FailedPipeline `json:"items"`
}

// FailedPipelines handles GET /workspaces/:ws_id/widgets/gitlab/failed-pipelines[?project_id=].
// The caller's tasks only, in projects bound to GitLab that the caller may
// open; project_id narrows to one of them. 404 widget_unavailable while the
// extension is off in the workspace (the dashboard shows the stub then).
func (h *GitLabWidgetHandler) FailedPipelines(c echo.Context) error {
	ws, apiErr := extWorkspaceID(c)
	if apiErr != nil {
		return c.JSON(apiErr.StatusCode(), apiErr)
	}
	user, ok := currentUserID(c)
	if !ok {
		return c.JSON(http.StatusForbidden, apierror.Forbidden("a signed-in person is required"))
	}
	ctx := c.Request().Context()
	available, err := h.widgets.WidgetsAvailable(ctx, ws, "gitlab")
	if err != nil {
		return handleError(c, err)
	}
	if !available {
		return c.JSON(http.StatusNotFound, &apierror.Error{Code: http.StatusNotFound, ErrorCode: "widget_unavailable",
			Message: "The GitLab extension is off in this workspace."})
	}
	projects, err := h.projects.WidgetProjects(ctx, ws, "gitlab", user)
	if err != nil {
		return handleError(c, err)
	}
	if raw := c.QueryParam("project_id"); raw != "" {
		pid, perr := uuid.Parse(raw)
		if perr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid project_id"))
		}
		if !slices.Contains(projects, pid) {
			// Same answer for a project that does not exist, is not bound or
			// is closed to the caller: nothing to learn from probing.
			return c.JSON(http.StatusNotFound, apierror.NotFound("project"))
		}
		projects = []uuid.UUID{pid}
	}
	since := h.now().Add(-GitLabFailedPipelinesWindow).UTC()
	items, err := h.links.FailedMergeRequestPipelines(ctx, ws, user, projects, since, gitlabFailedPipelinesLimit)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, failedPipelinesResponse{Since: since, Items: items})
}
