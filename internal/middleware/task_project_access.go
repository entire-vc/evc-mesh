package middleware

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// taskProjectChildParams are route parameters naming an object that belongs to a
// task, mapped to the query that finds that task's project. They reuse the chat
// scope resolvers so the two guards cannot disagree about where an object lives.
var taskProjectChildParams = map[string]string{
	"dep_id":      chatSessionChildResolvers["dep_id"],
	"comment_id":  chatSessionChildResolvers["comment_id"],
	"artifact_id": chatSessionChildResolvers["artifact_id"],
	"link_id":     chatSessionChildResolvers["link_id"],
	// Decisions answer a question asked on a task; answering or revoking one
	// acts on that task.
	"decision_request_id": `SELECT t.project_id FROM decision_requests d JOIN tasks t ON t.id = d.task_id WHERE d.id = $1 AND t.deleted_at IS NULL`,
	"decision_id":         `SELECT t.project_id FROM human_gate_decisions d JOIN tasks t ON t.id = d.task_id WHERE d.id = $1 AND t.deleted_at IS NULL`,
	// An acceptance intent is recorded against a task; revoking one acts on it.
	"intent_id": `SELECT t.project_id FROM task_acceptance_intents i JOIN tasks t ON t.id = i.task_id WHERE i.id = $1 AND t.deleted_at IS NULL`,
}

// projectRecordParams are route parameters naming a record that belongs to a
// project rather than to a task: a document and its attachments and comments, a
// custom field, a view, a template, a recurring schedule, a rule, an event. Each
// maps to the query that finds the record's project, which is NULL for a record
// of the workspace itself (a workspace document, a workspace rule) — those stay
// governed by the workspace guard alone.
var projectRecordParams = map[string]string{
	// A deleted document, a deleted recurring schedule, and what hangs on a document, resolves to nothing: the handler
	// answers 404 for it, so a non-member must get that too and not a 403 that
	// tells them the id once existed.
	"doc_id":       `SELECT project_id FROM documents WHERE id = $1 AND deleted_at IS NULL`,
	"att_id":       `SELECT d.project_id FROM document_attachments a JOIN documents d ON d.id = a.document_id WHERE a.id = $1 AND a.deleted_at IS NULL AND d.deleted_at IS NULL`,
	"dcom_id":      `SELECT d.project_id FROM document_comments c JOIN documents d ON d.id = c.document_id WHERE c.id = $1 AND c.deleted_at IS NULL AND d.deleted_at IS NULL`,
	"field_id":     chatSessionChildResolvers["field_id"],
	"view_id":      chatSessionChildResolvers["view_id"],
	"tmpl_id":      chatSessionChildResolvers["tmpl_id"],
	"recurring_id": `SELECT project_id FROM recurring_schedules WHERE id = $1 AND deleted_at IS NULL`,
	// The chat resolver refuses a workspace rule outright; here it must come
	// back as NULL so that it passes, so the filter is dropped.
	"rule_id":  `SELECT project_id FROM rules WHERE id = $1`,
	"event_id": `SELECT project_id FROM event_bus_messages WHERE id = $1`,
}

// EnvProjectRecordAccess selects how RequireTaskProjectMember treats a refusal on
// a projectRecordParams route: "enforce" answers it, anything else (the default,
// "report") lets the request through and logs project_record_access_would_deny.
// These routes were open to the whole workspace for a long time and reads carry
// no actor in the access log, so the refusals are observed before they are
// switched on. Read on every request; rolling back is setting it to report.
const EnvProjectRecordAccess = "MESH_PROJECT_RECORD_ACCESS"

func projectRecordAccessEnforced() bool {
	return os.Getenv(EnvProjectRecordAccess) == "enforce"
}

// resolveRecordProject runs a projectRecordParams query. found is false when no
// such record exists (or the lookup failed); a found record with a NULL project
// returns found and a nil project.
func resolveRecordProject(c echo.Context, db *sqlx.DB, query, raw string) (projID *uuid.UUID, found bool) {
	id, err := uuid.Parse(raw)
	if err != nil || db == nil {
		return nil, false
	}
	var p uuid.NullUUID
	if err := db.QueryRowContext(c.Request().Context(), query, id).Scan(&p); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("project record lookup failed", "error", err, "path", c.Path())
		}
		return nil, false
	}
	if !p.Valid {
		return nil, true
	}
	return &p.UUID, true
}

// reportWouldDeny logs a refusal that report mode did not apply.
func reportWouldDeny(c echo.Context, param string, projID *uuid.UUID, reason string) {
	attrs := []any{"param", param, "path", c.Path(), "reason", reason}
	if projID != nil {
		attrs = append(attrs, "project", projID.String())
	}
	if IsAgent(c) {
		if id, err := GetAgentID(c); err == nil {
			attrs = append(attrs, "agent", id.String())
		}
	} else if id, err := GetUserID(c); err == nil {
		attrs = append(attrs, "user", id.String())
	}
	slog.Warn("project_record_access_would_deny", attrs...)
}

// contextKeyProjectAccess carries the request's project access check, set by
// RequireTaskProjectMember for handlers that find tasks or projects in a
// request body, where no route parameter shows them to the guard.
const contextKeyProjectAccess = "project_access"

// ProjectAccessCheck answers, for the request's actor, whether a task's project
// and a project may be reached. An unresolvable task is not reachable.
type ProjectAccessCheck struct {
	Task    func(taskID uuid.UUID) bool
	Project func(projectID uuid.UUID) bool
}

// SetProjectAccess puts check on the request. RequireTaskProjectMember does it
// for every API request; tests use it to stand in for the database.
func SetProjectAccess(c echo.Context, check ProjectAccessCheck) {
	c.Set(contextKeyProjectAccess, check)
}

// MayAccessTask applies the project access rule to a task named in a request
// body. Without the check on the request it refuses: a handler mounted outside
// the guarded group must not read as open.
func MayAccessTask(c echo.Context, taskID uuid.UUID) bool {
	check, ok := c.Get(contextKeyProjectAccess).(ProjectAccessCheck)
	return ok && check.Task != nil && check.Task(taskID)
}

// MayAccessProject is MayAccessTask for a project named in a request body.
func MayAccessProject(c echo.Context, projectID uuid.UUID) bool {
	check, ok := c.Get(contextKeyProjectAccess).(ProjectAccessCheck)
	return ok && check.Project != nil && check.Project(projectID)
}

// dbProjectAccess is the rule for ids that arrive in a body. The workspace guard
// never saw them, and projectAccessDenial assumes it did: the owner/admin bypass
// and the extension actor pass any project without a membership lookup. So the
// project is first held to the workspace the request was admitted to (read when
// the handler asks, after any route-level workspace guard has set it); no
// workspace on the request refuses.
func dbProjectAccess(c echo.Context, db *sqlx.DB, members ProjectMembership) ProjectAccessCheck {
	allowed := func(projID uuid.UUID) bool {
		if db == nil {
			return false
		}
		wsID, err := GetWorkspaceID(c)
		if err != nil {
			return false
		}
		var projWS uuid.UUID
		if err := db.QueryRowContext(c.Request().Context(),
			`SELECT workspace_id FROM projects WHERE id = $1 AND deleted_at IS NULL`, projID).Scan(&projWS); err != nil {
			return false
		}
		return projWS == wsID && projectAccessDenial(c, members, projID) == ""
	}
	return ProjectAccessCheck{
		Task: func(taskID uuid.UUID) bool {
			projID, ok := resolveTaskProject(c.Request().Context(), db, taskID.String())
			return ok && allowed(projID)
		},
		Project: allowed,
	}
}

// taskProjectExemptRoutes name a task or document object only to act on the caller's own
// record of it: marking a mention seen changes the caller's inbox, not the
// comment, and a caller who was mentioned outside their projects must still be
// able to clear it.
var taskProjectExemptRoutes = map[string]bool{
	"/api/v1/me/mentions/:comment_id/seen":       true,
	"/api/v1/me/document-mentions/:dcom_id/seen": true,
}

// RequireTaskProjectMember applies project membership to every route that names
// a task — /tasks/:task_id/*, /tasks/by-short-id/:short — or an object of one
// (a comment, an artifact, a dependency edge, a VCS link, a decision), and to
// every route that names a project's record by id (projectRecordParams), the
// latter under EnvProjectRecordAccess.
//
// Those routes carry no :proj_id, so RequireProjectMember cannot sit on them, and
// for a long time they had only the workspace guard: any member of the workspace
// could read and edit the tasks of a project they were not in, by id, while the
// same project's task list under /projects/:proj_id/tasks refused them. The rule
// here is the one RequireProjectMember applies (projectAccessDenial), only the
// project comes from the task.
//
// It also puts the same rule on the request (MayAccessTask, MayAccessProject)
// for handlers that are handed a task or a project in the body.
//
// Registered once on the authenticated API group, after RequireWorkspaceMemberScoped,
// so that a new task route is covered by construction rather than by the author
// remembering a per-route guard. Inert on routes that name no task.
//
// A parameter whose project cannot be found answers 404: the workspace guard has
// already answered 404 for a task that does not exist, so reaching here unresolved
// means it vanished in between, and failing open would be the wrong way to lose
// that race.
func RequireTaskProjectMember(db *sqlx.DB) echo.MiddlewareFunc {
	members := dbProjectMembership{db}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			SetProjectAccess(c, dbProjectAccess(c, db, members))
			if taskProjectExemptRoutes[c.Path()] {
				return next(c)
			}
			ctx := c.Request().Context()
			for _, name := range c.ParamNames() {
				raw := c.Param(name)
				if raw == "" {
					continue
				}
				switch name {
				case "task_id", "short":
					projID, ok := resolveTaskProject(ctx, db, raw)
					if !ok {
						return c.JSON(http.StatusNotFound, apierror.NotFound("Task"))
					}
					if msg := projectAccessDenial(c, members, projID); msg != "" {
						return c.JSON(http.StatusForbidden, apierror.Forbidden(msg))
					}
				default:
					if query, isRecord := projectRecordParams[name]; isRecord {
						if refused, err := checkProjectRecord(c, db, members, name, query, raw); refused {
							return err
						}
						continue
					}
					query, isChild := taskProjectChildParams[name]
					if !isChild {
						continue
					}
					projID, ok := resolveChildProject(ctx, db, query, raw)
					if !ok {
						return c.JSON(http.StatusNotFound, apierror.NotFound("resource"))
					}
					if msg := projectAccessDenial(c, members, projID); msg != "" {
						return c.JSON(http.StatusForbidden, apierror.Forbidden(msg))
					}
				}
			}
			return next(c)
		}
	}
}

// checkProjectRecord applies the project rule to one projectRecordParams value.
// refused is true when the request was answered here (enforce mode); in report
// mode a refusal is only logged. A record of the workspace (NULL project) is not
// checked.
func checkProjectRecord(c echo.Context, db *sqlx.DB, members ProjectMembership, name, query, raw string) (refused bool, err error) {
	enforce := projectRecordAccessEnforced()
	projID, found := resolveRecordProject(c, db, query, raw)
	if !found {
		// The workspace guard has already answered 404 for a record that does
		// not exist; see RequireTaskProjectMember on why that is not a pass.
		if enforce {
			return true, c.JSON(http.StatusNotFound, apierror.NotFound("resource"))
		}
		return false, nil
	}
	if projID == nil {
		return false, nil
	}
	msg := projectAccessDenial(c, members, *projID)
	if msg == "" {
		return false, nil
	}
	if enforce {
		return true, c.JSON(http.StatusForbidden, apierror.Forbidden(msg))
	}
	reportWouldDeny(c, name, projID, msg)
	return false, nil
}
