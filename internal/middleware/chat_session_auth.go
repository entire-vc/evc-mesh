package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ContextKeyChatSession holds the *service.ChatSessionPrincipal of a request
// authenticated with a cst_ chat-session token.
const ContextKeyChatSession = "chat_session_principal"

// IsChatSession reports whether the request authenticated with a chat-session token.
func IsChatSession(c echo.Context) bool {
	return GetChatSession(c) != nil
}

// GetChatSession returns the verified chat-session principal, nil for any other request.
func GetChatSession(c echo.Context) *service.ChatSessionPrincipal {
	p, _ := c.Get(ContextKeyChatSession).(*service.ChatSessionPrincipal)
	return p
}

func chatSessionBearerToken(c echo.Context) (string, bool) {
	header := c.Request().Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if !strings.HasPrefix(token, service.ChatSessionTokenPrefix) {
		return "", false
	}
	return token, true
}

// ChatSessionAuth resolves a cst_ bearer ahead of DualAuth (which then skips the
// request). The principal is the AGENT, bound to the token's workspace, with the
// token's user recorded as the delegating user — so RequirePermission and the
// connector clamps compute agent ∩ user's CURRENT role on every call. An invalid,
// expired or revoked token is a 401 with no fall-through to another credential:
// a request that presented a chat token never silently becomes an agent-key or
// user request. A failure to verify (storage outage) is a 503, never an allow.
func ChatSessionAuth(svc service.ChatSessionService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			token, ok := chatSessionBearerToken(c)
			if !ok {
				return next(c)
			}
			if svc == nil {
				return unauthorizedJSON(c, "chat session tokens are not enabled")
			}
			p, err := svc.Authenticate(c.Request().Context(), token)
			if err != nil {
				var apiErr *apierror.Error
				if errors.As(err, &apiErr) && apiErr.Code == http.StatusUnauthorized {
					return unauthorizedJSON(c, apiErr.Message)
				}
				return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("cannot verify chat session token"))
			}
			setAgentAuthContext(c, p.Agent)
			c.Set(ContextKeyChatSession, p)
			goCtx := actorctx.WithChatAttribution(c.Request().Context(), actorctx.ChatAttribution{
				InitiatedByUserID: p.Token.UserID,
				ChatThreadID:      p.Token.ChatThreadID,
				ChatSessionID:     p.Token.ChatSessionID,
				ExecutionEpoch:    p.Token.ExecutionEpoch,
			})
			c.SetRequest(c.Request().WithContext(goCtx))
			return next(c)
		}
	}
}

// chatSessionRouteAllowlist are workspace/agent-level routes a chat token may call
// although they name no project: they touch only the calling agent itself.
var chatSessionRouteAllowlist = map[string]bool{
	"GET /api/v1/agents/me": true,
}

// chatSessionRouteDenylist are routes refused to a chat token even though their
// path names a project resource: they take a second project from the request body
// (the destination of a move), which the path-based scope check cannot see.
var chatSessionRouteDenylist = map[string]bool{
	"POST /api/v1/tasks/:task_id/move-to-project": true,
}

// ChatSessionProjectScope confines a chat token to its one project (contract §2:
// "каждый ресурс операции принадлежит тому же workspace/project"). It resolves the
// project of every resource the route names and refuses anything that is not the
// token's project. Fail-closed: a route whose project cannot be determined — an
// unknown path parameter, or no project at all — is refused, never waved through.
// Inert for every other principal. Must run after DualAuth.
func ChatSessionProjectScope(db *sqlx.DB) echo.MiddlewareFunc {
	deny := func(c echo.Context, msg string) error {
		return c.JSON(http.StatusForbidden, apierror.ForbiddenWithDetails(msg, "chat_scope_denied"))
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			p := GetChatSession(c)
			if p == nil {
				return next(c)
			}
			if chatSessionRouteDenylist[c.Request().Method+" "+c.Path()] {
				return deny(c, "route is not available to a chat session")
			}
			ctx := c.Request().Context()
			names := c.ParamNames()
			seenProject := false

			check := func(got uuid.UUID, ok bool) error {
				if !ok || got != p.Token.ProjectID {
					return deny(c, "resource is outside the chat's project")
				}
				seenProject = true
				return nil
			}

			for _, name := range names {
				val := c.Param(name)
				switch name {
				case "ws_id":
					if id, err := uuid.Parse(val); err != nil || id != p.Token.WorkspaceID {
						return deny(c, "resource is outside the chat's workspace")
					}
				case "proj_id", "project_id":
					id, err := uuid.Parse(val)
					if err != nil {
						return deny(c, "resource is outside the chat's project")
					}
					if e := check(id, true); e != nil {
						return e
					}
				case "task_id":
					id, ok := resolveTaskProject(ctx, db, val)
					if e := check(id, ok); e != nil {
						return e
					}
				default:
					q, known := chatSessionChildResolvers[name]
					if !known {
						return deny(c, "route is not available to a chat session")
					}
					id, ok := resolveChildProject(ctx, db, q, val)
					if e := check(id, ok); e != nil {
						return e
					}
				}
			}
			// A second task named in the body (the far end of a dependency edge) is
			// a resource of the operation too: it must be the token's project as well.
			if c.Request().Method == http.MethodPost && c.Path() == chatSessionDependencyRoute {
				if !bodyTaskInProject(c, db, p.Token.ProjectID) {
					return deny(c, "resource is outside the chat's project")
				}
			}
			// The memory write names its project in the body: it is pinned to the
			// chat's project memory, and that makes the route project-scoped.
			if c.Request().Method == http.MethodPost && c.Path() == ChatSessionMemoryWriteRoute {
				if !pinMemoryWriteToProject(c, p.Token.WorkspaceID, p.Token.ProjectID) {
					return deny(c, "memory write is outside the chat's project")
				}
				seenProject = true
			}
			// A query project_id is a filter the caller controls, never proof that the
			// route is project-scoped: it may only agree with the token, and does not
			// lift the fail-closed refusal of a route that names no project itself.
			if q := c.QueryParam("project_id"); q != "" {
				id, err := uuid.Parse(q)
				if err != nil || id != p.Token.ProjectID {
					return deny(c, "resource is outside the chat's project")
				}
			}
			if !seenProject && !chatSessionRouteAllowlist[c.Request().Method+" "+c.Path()] {
				return deny(c, "route is not available to a chat session")
			}
			return next(c)
		}
	}
}

// chatSessionChildResolvers maps a child-resource path parameter to a query that
// returns the project owning that resource. The id is bound as $1. A parameter
// absent from this map (and a resource whose row yields no project, e.g. a
// workspace-wide rule with NULL project_id) stays refused.
var chatSessionChildResolvers = map[string]string{
	"status_id":    `SELECT project_id FROM task_statuses WHERE id = $1`,
	"field_id":     `SELECT project_id FROM custom_field_definitions WHERE id = $1`,
	"view_id":      `SELECT project_id FROM saved_views WHERE id = $1`,
	"atr_id":       `SELECT project_id FROM auto_transition_rules WHERE id = $1`,
	"tmpl_id":      `SELECT project_id FROM task_templates WHERE id = $1`,
	"recurring_id": `SELECT project_id FROM recurring_schedules WHERE id = $1`,
	"rule_id":      `SELECT project_id FROM rules WHERE id = $1 AND project_id IS NOT NULL`,
	"doc_id":       `SELECT project_id FROM documents WHERE id = $1`,
	"dep_id":       `SELECT t.project_id FROM task_dependencies d JOIN tasks t ON t.id = d.task_id WHERE d.id = $1 AND t.deleted_at IS NULL`,
	"comment_id":   `SELECT t.project_id FROM comments c JOIN tasks t ON t.id = c.task_id WHERE c.id = $1 AND t.deleted_at IS NULL`,
	"artifact_id":  `SELECT t.project_id FROM artifacts a JOIN tasks t ON t.id = a.task_id WHERE a.id = $1 AND t.deleted_at IS NULL`,
	"link_id":      `SELECT t.project_id FROM vcs_links v JOIN tasks t ON t.id = v.task_id WHERE v.id = $1 AND t.deleted_at IS NULL`,
	"att_id":       `SELECT d.project_id FROM document_attachments a JOIN documents d ON d.id = a.document_id WHERE a.id = $1`,
	"dcom_id":      `SELECT d.project_id FROM document_comments c JOIN documents d ON d.id = c.document_id WHERE c.id = $1`,
}

// resolveChildProject runs one of chatSessionChildResolvers for a UUID path value.
func resolveChildProject(ctx context.Context, db *sqlx.DB, query, raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	if err != nil || db == nil {
		return uuid.Nil, false
	}
	var projectID uuid.UUID
	if err := db.QueryRowContext(ctx, query, id).Scan(&projectID); err != nil || projectID == uuid.Nil {
		return uuid.Nil, false
	}
	return projectID, true
}

// resolveTaskProject maps a task id (full UUID or 6-12 hex prefix, as every
// /tasks/:task_id route accepts) to its project. A prefix matching more than one
// task is unresolved (and therefore refused).
func resolveTaskProject(ctx context.Context, db *sqlx.DB, raw string) (uuid.UUID, bool) {
	if db == nil {
		return uuid.Nil, false
	}
	var projectID uuid.UUID
	if id, err := uuid.Parse(raw); err == nil {
		if err := db.QueryRowContext(ctx, `SELECT project_id FROM tasks WHERE id = $1 AND deleted_at IS NULL`, id).Scan(&projectID); err != nil {
			return uuid.Nil, false
		}
		return projectID, true
	}
	if len(raw) < 6 || len(raw) > 12 {
		return uuid.Nil, false
	}
	for _, r := range raw {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return uuid.Nil, false
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT project_id FROM tasks WHERE id::text LIKE $1 AND deleted_at IS NULL LIMIT 2`, strings.ToLower(raw)+"%")
	if err != nil {
		return uuid.Nil, false
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		if err := rows.Scan(&projectID); err != nil {
			return uuid.Nil, false
		}
		n++
	}
	if n != 1 || rows.Err() != nil {
		return uuid.Nil, false
	}
	return projectID, true
}

// ChatSessionMemoryWriteRoute is the one memory route open to a chat token: a
// write to the chat's project memory. Reads, import and delete stay refused.
const ChatSessionMemoryWriteRoute = "/api/v1/memories"

// pinMemoryWriteToProject confines a memory write body to the chat's project:
// scope and project_id default to "project" and the token's project, and any
// other scope, project or workspace is false. An unreadable or non-object body
// is false too (fail-closed). The rewritten body is handed to the handler.
func pinMemoryWriteToProject(c echo.Context, workspaceID, projectID uuid.UUID) bool {
	req := c.Request()
	if req.Body == nil {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		return false
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return false
	}
	var fields struct {
		WorkspaceID *string `json:"workspace_id"`
		ProjectID   *string `json:"project_id"`
		Scope       *string `json:"scope"`
	}
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	if fields.Scope != nil && *fields.Scope != "" && *fields.Scope != "project" {
		return false
	}
	if fields.ProjectID != nil {
		if id, perr := uuid.Parse(*fields.ProjectID); perr != nil || id != projectID {
			return false
		}
	}
	if fields.WorkspaceID != nil && *fields.WorkspaceID != "" {
		if id, perr := uuid.Parse(*fields.WorkspaceID); perr != nil || id != workspaceID {
			return false
		}
	}
	body["scope"], _ = json.Marshal("project")
	body["project_id"], _ = json.Marshal(projectID)
	pinned, err := json.Marshal(body)
	if err != nil {
		return false
	}
	req.Body = io.NopCloser(bytes.NewReader(pinned))
	req.ContentLength = int64(len(pinned))
	return true
}

// chatSessionDependencyRoute takes depends_on_task_id from the body.
const chatSessionDependencyRoute = "/api/v1/tasks/:task_id/dependencies"

// bodyTaskInProject reports whether the task named by depends_on_task_id in the
// JSON body belongs to projectID. An unreadable body, a missing or malformed id
// and an unresolvable task are all false (fail-closed). The body is restored for
// the handler.
func bodyTaskInProject(c echo.Context, db *sqlx.DB, projectID uuid.UUID) bool {
	req := c.Request()
	if req.Body == nil {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		return false
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))
	var body struct {
		DependsOnTaskID string `json:"depends_on_task_id"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	id, err := uuid.Parse(body.DependsOnTaskID)
	if err != nil {
		return false
	}
	got, ok := resolveTaskProject(req.Context(), db, id.String())
	return ok && got == projectID
}
