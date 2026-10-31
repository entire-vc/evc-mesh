package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// HierarchyReader reads the workspace hierarchy snapshot.
type HierarchyReader interface {
	Hierarchy(ctx context.Context, workspaceID uuid.UUID) (*domain.HierarchyExport, bool, error)
}

// WorkspaceExportHandler serves GET /workspaces/{ws}/export/hierarchy: the whole
// workspace tree (projects, members with roles, agents with active key metadata
// and sponsor) in one response, for the periodic full reconciliation of an
// extension that mirrors it (Keep). Incremental changes arrive as events; this is
// the check that nothing was missed.
type WorkspaceExportHandler struct {
	reader HierarchyReader
	// scope narrows the projects of a project-bound extension connection; nil
	// refuses the export to such a connection instead of returning everything.
	scope ExtensionBoundProjects
}

// ExtensionBoundProjects lists the projects an extension connection may reach
// (repository.ExtensionProjectScopeRepository implements it).
type ExtensionBoundProjects interface {
	BoundProjects(ctx context.Context, connectionID uuid.UUID) (all bool, ids []uuid.UUID, err error)
}

// WithProjectScope narrows the export of a project-bound extension to its
// bound projects.
func (h *WorkspaceExportHandler) WithProjectScope(scope ExtensionBoundProjects) *WorkspaceExportHandler {
	h.scope = scope
	return h
}

// NewWorkspaceExportHandler creates a new WorkspaceExportHandler.
func NewWorkspaceExportHandler(reader HierarchyReader) *WorkspaceExportHandler {
	return &WorkspaceExportHandler{reader: reader}
}

// hierarchyExportCapabilities are all required of an extension actor: the export
// carries the data of four resources, so holding one of them is not enough. The
// route guard checks the first; the handler checks the rest.
var hierarchyExportCapabilities = []string{"workspaces:read", "projects:read", "members:read", "agents:read"}

// Hierarchy handles GET /workspaces/:ws_id/export/hierarchy.
//
// cursor is the hash of the exported data; the same value is the ETag. A caller
// that sends it back in If-None-Match gets 304 when nothing changed, so a full
// reconciliation every few minutes costs one query set and no payload.
func (h *WorkspaceExportHandler) Hierarchy(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}
	if p := middleware.GetExtensionPrincipal(c); p != nil {
		var missing []string
		for _, capability := range hierarchyExportCapabilities {
			if !p.HasAPI(capability) {
				missing = append(missing, capability)
			}
		}
		if len(missing) > 0 {
			return c.JSON(http.StatusForbidden, apierror.Forbidden(
				"the hierarchy export needs api:"+strings.Join(missing, ", api:")))
		}
	}

	snap, ok, err := h.reader.Hierarchy(c.Request().Context(), wsID)
	if err != nil {
		return handleError(c, err)
	}
	if !ok {
		return c.JSON(http.StatusNotFound, apierror.NotFound("Workspace"))
	}
	if p := middleware.GetExtensionPrincipal(c); p != nil && p.ProjectBound {
		if h.scope == nil {
			return c.JSON(http.StatusForbidden, apierror.Forbidden("the hierarchy export is not available to a project-bound extension"))
		}
		all, ids, serr := h.scope.BoundProjects(c.Request().Context(), p.ConnectionID)
		if serr != nil {
			return handleError(c, serr)
		}
		if !all {
			snap.Projects = boundProjectsOnly(snap.Projects, ids)
		}
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		return handleError(c, err)
	}
	sum := sha256.Sum256(raw)
	cursor := hex.EncodeToString(sum[:])
	etag := `"` + cursor + `"`
	c.Response().Header().Set("ETag", etag)
	c.Response().Header().Set("Cache-Control", "private, no-cache")
	if inm := c.Request().Header.Get("If-None-Match"); inm != "" && etagMatches(inm, etag) {
		return c.NoContent(http.StatusNotModified)
	}

	scheme, host := requestOrigin(c.Request())
	instance := ""
	if host != "" {
		instance = scheme + "://" + host
	}
	return c.JSON(http.StatusOK, map[string]any{
		"workspace":    snap.Workspace,
		"projects":     snap.Projects,
		"members":      snap.Members,
		"agents":       snap.Agents,
		"cursor":       cursor,
		"instance_url": instance,
		"counts": map[string]int{
			"projects": len(snap.Projects), "members": len(snap.Members), "agents": len(snap.Agents),
		},
	})
}

// etagMatches reports whether an If-None-Match value names etag: a list of
// entity tags, a weak prefix allowed, or "*".
func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "W/")
		if part == "*" || part == etag {
			return true
		}
	}
	return false
}

// boundProjectsOnly keeps the projects whose id is in ids, in their order.
func boundProjectsOnly(projects []domain.HierarchyProject, ids []uuid.UUID) []domain.HierarchyProject {
	keep := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	out := make([]domain.HierarchyProject, 0, len(ids))
	for _, pr := range projects {
		if keep[pr.ID] {
			out = append(out, pr)
		}
	}
	return out
}
