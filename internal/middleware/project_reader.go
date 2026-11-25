package middleware

import (
	"context"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ProjectReaderMessage is the 403 text of every refusal ProjectReaderGuard
// gives. Exported so tests assert the guard that fired. The same text for a
// route outside the allowlist, a foreign project and an id that resolves to
// nothing: the answer must not confirm that a row exists elsewhere.
const ProjectReaderMessage = "project reader: this key reads only the allowlisted routes of its own project"

// projectReaderRouteKind says how an allowlisted route is checked.
type projectReaderRouteKind int

const (
	// readerRouteSelf addresses only the caller itself (GET /agents/me).
	readerRouteSelf projectReaderRouteKind = iota + 1
	// readerRouteProject must address exactly the reader's project through
	// its URL (RequestProjects); found=false is a refusal.
	readerRouteProject
)

// projectReaderRoutes is the whole surface of a project reader. Everything
// else — every write, every /memories* path, /projects/:proj_id/knowledge,
// /canonical_updates, workspace lists, search, export — is 403 by default.
//
// Adding a route here is a security change: read its handler first and make
// sure the answer contains nothing from another project (embedded tasks,
// attachments, links, workspace-tier rows) and that it resolves its object the
// same way RequestProjects does. TestProjectReader_AllowlistIsPinned fails
// until the new entry is also pinned there.
var projectReaderRoutes = map[string]projectReaderRouteKind{
	"GET /api/v1/agents/me":               readerRouteSelf,
	"GET /api/v1/tasks/:task_id/comments": readerRouteProject,
}

// ProjectReaderRoutes returns a copy of the allowlist, for the route tests.
func ProjectReaderRoutes() map[string]bool {
	out := make(map[string]bool, len(projectReaderRoutes))
	for k := range projectReaderRoutes {
		out[k] = true
	}
	return out
}

// ReaderProjectID returns the project a project reader is bound to, or false
// for every other principal.
func ReaderProjectID(c echo.Context) (uuid.UUID, bool) {
	if !IsAgent(c) {
		return uuid.Nil, false
	}
	id, ok := c.Get(ContextKeyReaderProjectID).(uuid.UUID)
	return id, ok && id != uuid.Nil
}

// ProjectResolver returns the projects a matched route addresses; see
// RequestProjects for the contract (found=false: no project parameter;
// errNoProject: a parameter that names no single live project).
type ProjectResolver func(ctx context.Context, c echo.Context) (projects []uuid.UUID, found bool, err error)

// ProjectReaderGuard confines a project reader (agents.reader_project_id set)
// to projectReaderRoutes on its own project: deny by default, whatever role
// its grant carries. It keys on the column only, never on role or
// capabilities. Inert for every other principal.
//
// Mounted on the authenticated group after AgentReadOnlyGuard; it must run
// after routing (c.Path() is the matched pattern), which group middleware does.
func ProjectReaderGuard(resolve ProjectResolver) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			readerProject, ok := ReaderProjectID(c)
			if !ok {
				return next(c)
			}
			deny := func() error {
				return c.JSON(http.StatusForbidden, apierror.Forbidden(ProjectReaderMessage))
			}
			method := c.Request().Method
			if method == http.MethodHead {
				method = http.MethodGet
			}
			kind := projectReaderRoutes[method+" "+c.Path()]
			switch kind {
			case readerRouteSelf:
				return next(c)
			case readerRouteProject:
				projects, found, err := resolve(c.Request().Context(), c)
				if err != nil {
					if isNoProject(err) {
						return deny()
					}
					log.Printf("[project-reader] resolve project for %s %s: %v", method, c.Path(), err)
					return c.JSON(http.StatusInternalServerError, apierror.InternalError("failed to check the project reader scope"))
				}
				if !found || len(projects) == 0 {
					return deny()
				}
				for _, pid := range projects {
					if pid != readerProject {
						return deny()
					}
				}
				return next(c)
			default:
				return deny()
			}
		}
	}
}
