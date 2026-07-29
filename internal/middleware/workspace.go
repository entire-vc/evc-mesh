package middleware

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ContextKeyWorkspaceRole stores the workspace-level role of the current actor.
const ContextKeyWorkspaceRole = "workspace_role"

// ContextKeyWorkspaceSource records how WorkspaceRLS arrived at the workspace it
// put in ContextKeyWorkspaceID. The distinction is what makes the group-level
// guard safe: a workspace named by the request (a path parameter) is a claim by
// the caller and has to be checked, while a workspace taken from the caller's own
// credentials is already theirs and checking it would only be tautological.
const ContextKeyWorkspaceSource = "workspace_id_source"

// Values stored under ContextKeyWorkspaceSource.
const (
	// WorkspaceSourceParam — resolved from a route parameter (see WorkspaceScopedParams).
	WorkspaceSourceParam = "param"
	// WorkspaceSourceAuth — taken from the caller's credentials (agent key auth).
	WorkspaceSourceAuth = "auth"
)

// ContextKeyWorkspaceParamMismatch is set by WorkspaceRLS when the request named
// more than one tenant, or named an object no tenant owns. See the "every
// parameter, not any parameter" note on workspaceParamResolvers.
const ContextKeyWorkspaceParamMismatch = "workspace_id_param_mismatch"

// WorkspaceScopedParams is the set of route parameters that name tenant-owned
// data. It is the contract between the resolvers below and
// RequireWorkspaceMemberScoped: a route carrying any of them names another
// tenant's data, so the guard must run on it.
//
// It is derived, never written out by hand, so a lookup added to either table
// cannot be left unguarded. TestWorkspaceScopedParams_CoversEveryResolver holds
// the derivation in place and fails if WorkspaceRLS grows an ad-hoc c.Param read
// outside the tables — that read would widen the reach of the API without
// widening the guard, which is the exact shape of the bug this file exists to fix.
var WorkspaceScopedParams = scopedParamNames()

func scopedParamNames() []string {
	params := make([]string, 0, len(workspaceParamResolvers)+len(workspaceScopedPairParams))
	for _, r := range workspaceParamResolvers {
		params = append(params, r.param)
	}
	for _, p := range workspaceScopedPairParams {
		params = append(params, p.param)
	}
	return params
}

// workspaceParamResolver maps a route parameter to the workspace whose data the
// object it names belongs to. resolve reports false when the parameter names
// nothing this database owns — which is a refusal, not a fall-through.
type workspaceParamResolver struct {
	param   string
	resolve func(ctx context.Context, db *sqlx.DB, projectRepo repository.ProjectRepository, raw string) (uuid.UUID, bool)
}

// workspaceParamResolvers is the whole of what a request path can say about which
// tenant it is addressing. WorkspaceRLS runs EVERY entry whose parameter the route
// carries and requires them all to name the same workspace.
//
// "Every, not any" is the fix for the composite routes. The previous version
// stopped at the first parameter that resolved, which on a two-id route was always
// the parent: /projects/:proj_id/statuses/:status_id resolved :proj_id, the guard
// checked the caller against their OWN project, and :status_id — the id that says
// which row actually gets written — was never looked at. So a member of any
// workspace could pass their own :proj_id together with a stranger's :status_id and
// rename another tenant's workflow status, and get 200 for it. The same shape let
// them delete another tenant's auto-transition rule, delete a dependency edge out
// of another tenant's task graph, and revoke or re-send another tenant's pending
// invite (the last one also mails a stranger's invitee on demand).
//
// The parent id resolving correctly is what made this invisible: every one of
// those routes satisfied "at least one parameter resolves", so both the guard and
// TestEveryIdentifiedRouteIsWorkspaceScoped were happy while the write went
// through. Requiring unanimity is what makes a composite route safe by
// construction rather than by somebody remembering to check the second id in the
// handler — which is exactly what auto_transition_handler.Update remembered and
// auto_transition_handler.Delete, four lines away, did not.
//
// Order matters only for which workspace becomes the RLS session value: the first
// entry that resolves wins, and the rest must agree with it.
var workspaceParamResolvers = []workspaceParamResolver{
	// The workspace names itself. There is no existence check here on purpose:
	// RequireWorkspaceMember does it by failing to find the caller in it.
	{"ws_id", func(_ context.Context, _ *sqlx.DB, _ repository.ProjectRepository, raw string) (uuid.UUID, bool) {
		id, err := uuid.Parse(raw)
		return id, err == nil
	}},
	{"proj_id", resolveProjectWorkspace},
	// project_id is the same thing under the spelling some older routes use.
	{"project_id", resolveProjectWorkspace},
	// A full uuid or a 6-12 hex character prefix of one: every /tasks/:task_id
	// route accepts both, so the prefix form has to resolve here too, or the guard
	// would refuse a member their own task whenever they addressed it the short way.
	{"task_id", func(ctx context.Context, db *sqlx.DB, _ repository.ProjectRepository, raw string) (uuid.UUID, bool) {
		if taskID, err := uuid.Parse(raw); err == nil {
			return queryWorkspace(ctx, db,
				`SELECT p.workspace_id
				   FROM tasks t
				   JOIN projects p ON t.project_id = p.id
				  WHERE t.id = $1 AND t.deleted_at IS NULL`, taskID)
		}
		return resolveWorkspaceByTaskPrefix(ctx, db, raw)
	}},
	{"artifact_id", uuidResolver(`SELECT p.workspace_id
	                                FROM artifacts a
	                                JOIN tasks t ON a.task_id = t.id AND t.deleted_at IS NULL
	                                JOIN projects p ON t.project_id = p.id
	                               WHERE a.id = $1`)},
	{"agent_id", uuidResolver(`SELECT workspace_id FROM agents WHERE id = $1 AND deleted_at IS NULL`)},
	{"field_id", uuidResolver(`SELECT p.workspace_id
	                             FROM custom_field_definitions cf
	                             JOIN projects p ON cf.project_id = p.id
	                            WHERE cf.id = $1`)},
	{"init_id", uuidResolver(`SELECT workspace_id FROM initiatives WHERE id = $1`)},

	// The "flat" object routes. The API is full of routes — /events/:event_id,
	// /webhooks/:webhook_id, /rules/:rule_id — that name an object directly instead
	// of hanging off /workspaces/:ws_id or /projects/:proj_id. Nothing in the
	// request path told the guard which tenant they belonged to, so
	// RequireWorkspaceMemberScoped had nothing to check and let them through: GET
	// /events/:event_id returned another tenant's task titles to any logged-in
	// stranger, PATCH /integrations/:int_id echoed back their integration
	// credentials, POST /recurring/:id/trigger created a task in their project.
	//
	// rbac() was not a second line of defence: on an agent key it short-circuits to
	// a static capability map and never looks at the target object's workspace at
	// all, and for a user JWT it can only read a workspace that a route parameter
	// had already resolved — which on these routes was none.
	{"event_id", uuidResolver(`SELECT workspace_id FROM event_bus_messages WHERE id = $1`)},
	{"comment_id", uuidResolver(`SELECT p.workspace_id
	                               FROM comments c
	                               JOIN tasks t ON c.task_id = t.id AND t.deleted_at IS NULL
	                               JOIN projects p ON t.project_id = p.id
	                              WHERE c.id = $1`)},
	{"view_id", uuidResolver(`SELECT p.workspace_id
	                            FROM saved_views v
	                            JOIN projects p ON v.project_id = p.id
	                           WHERE v.id = $1`)},
	{"webhook_id", uuidResolver(`SELECT workspace_id FROM webhook_configs WHERE id = $1`)},
	{"int_id", uuidResolver(`SELECT workspace_id FROM integration_configs WHERE id = $1`)},
	{"tmpl_id", uuidResolver(`SELECT p.workspace_id
	                            FROM task_templates tt
	                            JOIN projects p ON tt.project_id = p.id
	                           WHERE tt.id = $1`)},
	{"rule_id", uuidResolver(`SELECT workspace_id FROM rules WHERE id = $1`)},
	{"link_id", uuidResolver(`SELECT p.workspace_id
	                            FROM vcs_links v
	                            JOIN tasks t ON v.task_id = t.id AND t.deleted_at IS NULL
	                            JOIN projects p ON t.project_id = p.id
	                           WHERE v.id = $1`)},
	{"recurring_id", uuidResolver(`SELECT workspace_id FROM recurring_schedules WHERE id = $1`)},

	// The child half of the composite routes. Before these existed the parent id
	// alone satisfied the guard and these ids addressed any tenant's row.
	{"status_id", uuidResolver(`SELECT p.workspace_id
	                              FROM task_statuses s
	                              JOIN projects p ON s.project_id = p.id
	                             WHERE s.id = $1`)},
	// Spelled :atr_id, not :rule_id, and the rename is load-bearing: the route is
	// /projects/:proj_id/auto-transition-rules/:rule_id but the row lives in
	// auto_transition_rules, while :rule_id above reads the unrelated rules table.
	// Two route families sharing one parameter name would have sent this id to the
	// wrong table and refused every legitimate request.
	// TestScopedParamNamesAreUnambiguous keeps that collision from coming back.
	{"atr_id", uuidResolver(`SELECT p.workspace_id
	                           FROM auto_transition_rules r
	                           JOIN projects p ON r.project_id = p.id
	                          WHERE r.id = $1`)},
	{"dep_id", uuidResolver(`SELECT p.workspace_id
	                           FROM task_dependencies d
	                           JOIN tasks t ON d.task_id = t.id AND t.deleted_at IS NULL
	                           JOIN projects p ON t.project_id = p.id
	                          WHERE d.id = $1`)},
	{"invite_id", uuidResolver(`SELECT workspace_id FROM user_invites WHERE id = $1`)},
	// A project's agent members are always agents of the project's own workspace —
	// projectMemberService.AddAgentMember refuses anything else — so the agent's
	// own workspace is the right thing to compare the path's :proj_id against.
	{"member_agent_id", uuidResolver(`SELECT workspace_id FROM agents WHERE id = $1 AND deleted_at IS NULL`)},

	// GET /tasks/by-short-id/:short takes a 6-12 hex character prefix of a task
	// uuid, matched with LIKE across every tenant's tasks. It carried no guard at
	// all, which made it both a cross-tenant read and an enumeration oracle — a
	// 6-character prefix is 16.7M values and the handler distinguishes "no such
	// task" from "several tasks match".
	{"short", func(ctx context.Context, db *sqlx.DB, _ repository.ProjectRepository, raw string) (uuid.UUID, bool) {
		return resolveWorkspaceByTaskPrefix(ctx, db, raw)
	}},
}

// workspaceScopedPairParams covers the ids that name something a workspace does
// not own outright, so no query can map them to one workspace. A user belongs to
// as many workspaces as they were invited to; asking "which workspace is this user
// in" has no answer. What can be checked is containment: does this id belong to
// the tenant the rest of the path resolved to.
//
// Each query takes the resolved workspace as $1 and the parameter's uuid as $2 and
// returns at least one row when the id is part of that tenant.
//
// The handlers behind /workspaces/:ws_id/members/:user_id and
// /projects/:proj_id/members/:user_id happen to be safe already — both address the
// membership row by its compound key and answer 404 when there is none, so a
// foreign :user_id changes nothing. This runs anyway, because "safe because the
// handler remembered" is the property that failed on the four routes above.
var workspaceScopedPairParams = []struct{ param, query string }{
	{"user_id", `SELECT 1 FROM workspace_members WHERE workspace_id = $1 AND user_id = $2
	              UNION ALL
	             SELECT 1 FROM workspaces WHERE id = $1 AND owner_id = $2
	              UNION ALL
	             SELECT 1
	               FROM project_members pm
	               JOIN projects p ON pm.project_id = p.id
	              WHERE p.workspace_id = $1 AND pm.user_id = $2
	              LIMIT 1`},
}

// uuidResolver turns a "give me this object's workspace_id" query into a resolver.
// The query takes the parameter's uuid as $1 and returns exactly one workspace_id,
// or no rows if the object does not exist.
func uuidResolver(query string) func(context.Context, *sqlx.DB, repository.ProjectRepository, string) (uuid.UUID, bool) {
	return func(ctx context.Context, db *sqlx.DB, _ repository.ProjectRepository, raw string) (uuid.UUID, bool) {
		id, err := uuid.Parse(raw)
		if err != nil {
			return uuid.Nil, false
		}
		return queryWorkspace(ctx, db, query, id)
	}
}

func queryWorkspace(ctx context.Context, db *sqlx.DB, query string, arg uuid.UUID) (uuid.UUID, bool) {
	if db == nil {
		return uuid.Nil, false
	}
	var wsID uuid.UUID
	if err := db.QueryRowContext(ctx, query, arg).Scan(&wsID); err != nil {
		return uuid.Nil, false
	}
	return wsID, true
}

func resolveProjectWorkspace(ctx context.Context, _ *sqlx.DB, projectRepo repository.ProjectRepository, raw string) (uuid.UUID, bool) {
	projID, err := uuid.Parse(raw)
	if err != nil || projectRepo == nil {
		return uuid.Nil, false
	}
	proj, err := projectRepo.GetByID(ctx, projID)
	if err != nil || proj == nil {
		return uuid.Nil, false
	}
	return proj.WorkspaceID, true
}

// workspaceScopeHandlerCheckedRoutes lists routes that name a tenant-owned object
// by its own id but are checked inside the handler instead of by this guard.
//
// TestEveryIdentifiedRouteIsWorkspaceScoped refuses any other route whose path
// carries a parameter the guard cannot resolve a workspace from, so an entry here
// is a deliberate, reviewed exception — not a place to park a new route.
//
// /memories/:id predates this guard and already does the right thing:
// MemoryHandler.workspaceAllowed checks the caller against the memory's own
// workspace and answers 404 rather than 403 so the id is not confirmed to exist.
// Its parameter is spelled :id, which several unrelated routes would also match if
// it were resolved centrally.
//
// /notifications/preferences/:pref_id cancels a subscription, and the row it names
// belongs to a person rather than to a workspace: the DELETE carries the caller's
// own user_id in its WHERE clause, so somebody else's preference id removes
// nothing and answers 404. That is a stricter test than workspace membership, and
// it has to be — the whole point of the route is that a subscriber who was never
// in the workspace can still get themselves out of it, which a membership guard
// would forbid.
var workspaceScopeHandlerCheckedRoutes = map[string]bool{
	"/api/v1/memories/:id":                       true,
	"/api/v1/memories/:id/related":               true,
	"/api/v1/notifications/preferences/:pref_id": true,
}

// workspaceScopeExemptRoutes lists routes that carry one of WorkspaceScopedParams
// in their path but whose parameter is not an identifier in this database, so no
// workspace can be resolved from it and requiring one would simply break them.
//
// Keys are Echo route paths (c.Path()), so an entry cannot silently match more
// than the one route it names.
//
// Spark's :agent_id is a catalog id in the external Spark marketplace, not an
// agents.id here. GET returns a public catalog manifest. POST .../install does
// touch a local workspace, but it takes it from the request body, so the guard
// could not see it either way — SparkHandler.Install checks membership and the
// register-agent permission itself.
var workspaceScopeExemptRoutes = map[string]bool{
	"/api/v1/spark/agents/:agent_id":         true,
	"/api/v1/spark/agents/:agent_id/install": true,
}

// WorkspaceRLS returns middleware that sets the PostgreSQL session variable
// app.current_workspace_id based on the request context. This enables
// Row-Level Security (RLS) policies at the database level.
//
// Resolution order:
//  1. every route parameter in workspaceParamResolvers that the path carries —
//     all of them, and they must agree (see that table for why "all")
//  2. workspace_id from auth context (agent key auth sets this)
//
// NOTE: set_config('app.current_workspace_id', $1, true) is used with the
// is_local flag set to true, which makes the value transaction-scoped (equivalent
// to SET LOCAL). Echo handlers do not run inside an explicit transaction, so the
// value effectively lasts until the end of the connection's implicit transaction.
// We set it on every request so connections reused from the pool always have a
// fresh value before handler execution.
func WorkspaceRLS(db *sqlx.DB, projectRepo repository.ProjectRepository) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			var wsID uuid.UUID
			var resolved bool
			// Everything the resolver table reads is a route parameter, so
			// anything it resolves is a workspace the request named; the auth
			// fallback below overrides this.
			source := WorkspaceSourceParam

			// 1. Resolve every workspace-scoping parameter the path carries.
			//
			// The first one that resolves becomes the RLS session value; the rest
			// have to agree with it. A parameter that resolves to a different
			// workspace, or to none at all, sets the mismatch flag and
			// RequireWorkspaceMemberScoped refuses the request — a path that names
			// two tenants must not be checked against whichever of them is more
			// convenient. Not resolving is the safe outcome either way: the guard
			// refuses a scoped route that produced no workspace.
			mismatch := false
			ctx := c.Request().Context()
			for _, r := range workspaceParamResolvers {
				raw := c.Param(r.param)
				if raw == "" {
					continue
				}
				got, ok := r.resolve(ctx, db, projectRepo, raw)
				if !ok {
					mismatch = true
					continue
				}
				if !resolved {
					wsID, resolved = got, true
					continue
				}
				if got != wsID {
					mismatch = true
				}
			}
			if mismatch {
				c.Set(ContextKeyWorkspaceParamMismatch, true)
			}

			// 2. Try workspace_id from auth context (set by agent key auth).
			if !resolved {
				if ctxWsID, err := GetWorkspaceID(c); err == nil {
					wsID = ctxWsID
					resolved = true
					source = WorkspaceSourceAuth
				}
			}

			// Set the session variable if we resolved a workspace ID.
			// Use set_config with the transaction-local flag (third arg = true) so the
			// value is scoped to the current transaction. The parameterized form prevents
			// any SQL injection through the workspace ID value.
			if resolved {
				var setCfgResult string
				if err := db.QueryRowContext(c.Request().Context(),
					"SELECT set_config('app.current_workspace_id', $1, true)",
					wsID.String(),
				).Scan(&setCfgResult); err != nil {
					log.Printf("ERROR: failed to set app.current_workspace_id for workspace %s: %v", wsID, err)
					return c.JSON(http.StatusInternalServerError, apierror.InternalError("workspace context unavailable"))
				}
				// Also store in Echo context so RBAC middleware (and handlers) can read it.
				c.Set(ContextKeyWorkspaceID, wsID)
				c.Set(ContextKeyWorkspaceSource, source)

				// Resolve workspace role for the current actor so downstream middleware
				// (e.g., RequireProjectMember) can check it without a second DB query.
				if !IsAgent(c) {
					if userID, err := GetUserID(c); err == nil {
						var role string
						err := db.QueryRowContext(c.Request().Context(),
							"SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2",
							wsID, userID,
						).Scan(&role)
						if err == nil {
							c.Set(ContextKeyWorkspaceRole, role)
						}
					}
				}
			}

			return next(c)
		}
	}
}

// RequireWorkspaceMember returns middleware that enforces workspace membership.
//
// For users: reads workspace_role from Echo context (populated by WorkspaceRLS with no
// extra DB query) — non-empty role means the user is a member.
// For agents: verifies agents.workspace_id matches the requested workspace (one SELECT).
//
// Must run after DualAuth and WorkspaceRLS.
func RequireWorkspaceMember(db *sqlx.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			wsID, err := GetWorkspaceID(c)
			if err != nil {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
			}

			if IsAgent(c) {
				agentID, err := GetAgentID(c)
				if err != nil {
					return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
				}
				if !AgentIsInWorkspace(c.Request().Context(), db, wsID, agentID) {
					return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
				}
				return next(c)
			}

			// Users: WorkspaceRLS sets workspace_role only when the user is a member.
			role, ok := c.Get(ContextKeyWorkspaceRole).(string)
			if ok && role != "" {
				return next(c)
			}

			// Fallback: the workspace owner whose workspace_members row is missing.
			//
			// The owner membership row is inserted best-effort after the workspace
			// itself is created (workspaceService.Create, auth.Service.Register), so a
			// partial failure leaves a workspace whose owner is not one of its members.
			// Such rows exist in the wild — without this branch, turning the guard on
			// would lock those owners out of their own workspace, turning a read-leak
			// fix into a data-loss-shaped outage. One extra query, only on the path
			// that was about to 403 anyway.
			userID, idErr := GetUserID(c)
			if idErr != nil || !UserOwnsWorkspace(c.Request().Context(), db, wsID, userID) {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
			}
			return next(c)
		}
	}
}

// shortTaskID matches the 6-12 hex character task id prefix the API accepts in
// place of a full uuid. Validating the shape here also keeps the LIKE pattern
// below free of wildcards the caller could have supplied themselves.
var shortTaskID = regexp.MustCompile(`^[0-9a-fA-F]{6,12}$`)

// resolveWorkspaceByTaskPrefix maps a short task id to the workspace of the one
// task it names, if it names exactly one.
//
// It reports failure for an ambiguous prefix rather than picking a task: a prefix
// shared by two tenants has no single workspace to check membership of, and
// answering from either one would be the leak this exists to close.
func resolveWorkspaceByTaskPrefix(ctx context.Context, db *sqlx.DB, short string) (uuid.UUID, bool) {
	if db == nil || !shortTaskID.MatchString(short) {
		return uuid.Nil, false
	}
	rows, err := db.QueryContext(ctx,
		`SELECT p.workspace_id
		   FROM tasks t
		   JOIN projects p ON t.project_id = p.id
		  WHERE t.id::text LIKE $1 AND t.deleted_at IS NULL
		  LIMIT 2`,
		strings.ToLower(short)+"%",
	)
	if err != nil {
		return uuid.Nil, false
	}
	defer func() { _ = rows.Close() }()

	var wsID uuid.UUID
	if !rows.Next() {
		return uuid.Nil, false
	}
	if err := rows.Scan(&wsID); err != nil {
		return uuid.Nil, false
	}
	if rows.Next() {
		return uuid.Nil, false // ambiguous prefix
	}
	return wsID, rows.Err() == nil
}

// UserOwnsWorkspace reports whether userID is the recorded owner of wsID.
//
// It exists as the owner-fallback half of the membership rule (see
// RequireWorkspaceMember) in a form that callers outside the Echo middleware
// chain — the WebSocket endpoint — can use, so the rule is written once.
func UserOwnsWorkspace(ctx context.Context, db *sqlx.DB, wsID, userID uuid.UUID) bool {
	if db == nil {
		return false
	}
	var ownerID uuid.UUID
	if err := db.QueryRowContext(ctx,
		"SELECT owner_id FROM workspaces WHERE id = $1 AND deleted_at IS NULL",
		wsID,
	).Scan(&ownerID); err != nil {
		return false
	}
	return ownerID == userID
}

// UserIsWorkspaceMember reports whether userID may act inside wsID: a
// workspace_members row, or the owner fallback for the workspaces whose owner row
// was never written (see RequireWorkspaceMember for why those exist).
//
// RequireWorkspaceMember does not call this directly because WorkspaceRLS has
// already done the workspace_members lookup for it and stores the result in the
// Echo context; this is the same rule for callers that have no Echo context —
// today, the WebSocket handshake.
func UserIsWorkspaceMember(ctx context.Context, db *sqlx.DB, wsID, userID uuid.UUID) bool {
	if db == nil {
		return false
	}
	var role string
	if err := db.QueryRowContext(ctx,
		"SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2",
		wsID, userID,
	).Scan(&role); err == nil && role != "" {
		return true
	}
	return UserOwnsWorkspace(ctx, db, wsID, userID)
}

// AgentIsInWorkspace reports whether the agent belongs to wsID.
func AgentIsInWorkspace(ctx context.Context, db *sqlx.DB, wsID, agentID uuid.UUID) bool {
	if db == nil {
		return false
	}
	var agentWsID uuid.UUID
	if err := db.QueryRowContext(ctx,
		"SELECT workspace_id FROM agents WHERE id = $1 AND deleted_at IS NULL",
		agentID,
	).Scan(&agentWsID); err != nil {
		return false
	}
	return agentWsID == wsID
}

// RequireWorkspaceMemberScoped enforces workspace membership on every route whose
// path names another tenant's data — that is, every route carrying one of
// WorkspaceScopedParams — and does nothing on routes that carry none.
//
// It exists because the per-route form could be — and was — forgotten. Only 2 of
// the 46 :ws_id routes actually had the guard attached, which left any logged-in
// stranger able to read another tenant's member emails, team directory, analytics
// and full YAML config export, and — via PATCH /workspaces/:ws_id, which had
// neither this guard nor an RBAC check — to rename another tenant's workspace or
// change its slug, breaking every /w/<slug>/... link that team had.
//
// The first version of this guard keyed on the literal :ws_id parameter, which
// left the same hole one indirection further out: WorkspaceRLS resolves the tenant
// from :proj_id, :task_id, :artifact_id, :agent_id, :field_id and :init_id too, and
// those routes went unchecked. A stranger could read any agent by id and — via
// POST /agents/:agent_id/activity, which had no RBAC bar either — write a forged
// entry into another tenant's agent activity log. So the guard now keys on the
// workspace WorkspaceRLS actually resolved, not on the spelling of one parameter.
//
// The third round closed the rest of the same class: routes naming an object by
// its own id with no workspace or project anywhere in the path
// (/events/:event_id, /comments/:comment_id, /views/:view_id,
// /webhooks/:webhook_id, /integrations/:int_id, /templates/:tmpl_id,
// /rules/:rule_id, /vcs-links/:link_id, /recurring/:recurring_id,
// /tasks/by-short-id/:short). WorkspaceRLS could resolve nothing from them, so
// this guard had nothing to check — see workspaceParamResolvers. The fix is a
// resolver per object rather than a membership check per handler, so that the
// invariant stays "the router names a tenant, the guard checks it" and
// TestEveryIdentifiedRouteIsWorkspaceScoped can hold the whole class shut.
//
// The fourth round closed the composite routes, where the guard was running and
// still let a write through. /projects/:proj_id/statuses/:status_id resolved
// :proj_id — the caller's own project — and never looked at :status_id, so any
// member of any workspace could rename another tenant's workflow status and get
// 200 for it; likewise deleting their auto-transition rules, their dependency
// edges and their pending invites. Requiring one parameter to resolve was never
// the invariant; requiring all of them to resolve to the SAME tenant is.
//
// Four cases, in order:
//
//  1. No workspace-scoping parameter in the path (/auth/me, /notifications,
//     /memories/:id): nothing to check, pass through.
//  2. The path named more than one tenant, or named an object no tenant owns:
//     refuse. This is the composite-route case above.
//  3. A parameter is present and WorkspaceRLS resolved a workspace *from it*:
//     require membership of that workspace, then require every containment
//     parameter (:user_id) to name somebody inside it.
//  4. A parameter is present and no workspace came out of it: refuse. The
//     workspace in context, if any, is the caller's own (agent key auth), which
//     says nothing about the object they asked for — treating that as permission
//     is how case 3 gets bypassed. This is already how the per-route guard behaved
//     on /tasks/:task_id, so a soft-deleted or unknown id answers 403 rather than
//     404 — the same answer either way, which is also the answer that does not
//     confirm the id exists.
//
// Registered once on the authenticated API group, it covers every current and
// future workspace-scoped route by construction, so a new route cannot silently
// ship unguarded. Must run after DualAuth and WorkspaceRLS.
func RequireWorkspaceMemberScoped(db *sqlx.DB) echo.MiddlewareFunc {
	guard := RequireWorkspaceMember(db)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		guarded := guard(requireScopedPairParams(db)(next))
		return func(c echo.Context) error {
			if !routeIsWorkspaceScoped(c) {
				return next(c)
			}
			if workspaceScopeExemptRoutes[c.Path()] {
				return next(c)
			}
			if mismatch, _ := c.Get(ContextKeyWorkspaceParamMismatch).(bool); mismatch {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
			}
			if src, _ := c.Get(ContextKeyWorkspaceSource).(string); src != WorkspaceSourceParam {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
			}
			return guarded(c)
		}
	}
}

// requireScopedPairParams enforces workspaceScopedPairParams: an id that names
// something no single workspace owns has to at least be shown to belong to the
// tenant the rest of the path resolved to.
//
// It runs after RequireWorkspaceMember, so the workspace in context is one the
// caller is already a member of; what is left to establish is that the other id in
// the path is part of the same tenant and not somebody else's.
//
// A parameter that is not a uuid is left alone — /workspaces/:ws_id/members/me is
// a different registered route, and anything else that shape is the handler's 400
// to give, not a cross-tenant reach.
func requireScopedPairParams(db *sqlx.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			for _, p := range workspaceScopedPairParams {
				raw := c.Param(p.param)
				if raw == "" {
					continue
				}
				id, err := uuid.Parse(raw)
				if err != nil {
					continue
				}
				wsID, err := GetWorkspaceID(c)
				if err != nil || db == nil {
					return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
				}
				var found int
				if err := db.QueryRowContext(c.Request().Context(), p.query, wsID, id).Scan(&found); err != nil {
					return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
				}
			}
			return next(c)
		}
	}
}

// routeIsWorkspaceScoped reports whether the request names a tenant-owned object
// through one of the parameters WorkspaceRLS resolves a workspace from.
func routeIsWorkspaceScoped(c echo.Context) bool {
	for _, name := range WorkspaceScopedParams {
		if c.Param(name) != "" {
			return true
		}
	}
	return false
}
