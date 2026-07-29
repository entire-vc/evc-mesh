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

// WorkspaceScopedParams is the set of route parameters WorkspaceRLS can resolve a
// tenant from. It is the contract between the resolvers below and
// RequireWorkspaceMemberScoped: a route carrying any of them names another
// tenant's data, so the guard must run on it.
//
// It is derived from workspaceParamResolvers rather than written out by hand, so
// a resolver cannot be added without the guard widening with it — that divergence
// is the exact shape of the bug this file exists to fix.
// TestWorkspaceScopedParams_CoversEveryResolver additionally reads this file and
// fails if WorkspaceRLS reads a route parameter that no resolver declares.
var WorkspaceScopedParams = workspaceResolverParams()

// workspaceObjectResolvers maps a route parameter naming a tenant-owned object to
// the query that says which workspace that object lives in.
//
// Every entry here is a route that used to answer a stranger. The API is full of
// "flat" routes — /events/:event_id, /webhooks/:webhook_id, /rules/:rule_id — that
// name an object directly instead of hanging off /workspaces/:ws_id or
// /projects/:proj_id. Nothing in the request path told the guard which tenant they
// belonged to, so RequireWorkspaceMemberScoped had nothing to check and let them
// through: GET /events/:event_id returned another tenant's task titles to any
// logged-in stranger, PATCH /integrations/:int_id echoed back their integration
// credentials, POST /recurring/:id/trigger created a task in their project.
//
// rbac() was not a second line of defence: on an agent key it short-circuits to a
// static capability map and never looks at the target object's workspace at all,
// and for a user JWT it can only read a workspace that a route parameter had
// already resolved — which on these routes was none.
//
// Each query takes the parameter's uuid as $1 and returns exactly one
// workspace_id, or no rows if the object does not exist.
var workspaceObjectResolvers = []struct{ param, query string }{
	{"event_id", `SELECT workspace_id FROM event_bus_messages WHERE id = $1`},
	{"comment_id", `SELECT p.workspace_id
	                  FROM comments c
	                  JOIN tasks t ON c.task_id = t.id AND t.deleted_at IS NULL
	                  JOIN projects p ON t.project_id = p.id
	                 WHERE c.id = $1`},
	{"view_id", `SELECT p.workspace_id
	               FROM saved_views v
	               JOIN projects p ON v.project_id = p.id
	              WHERE v.id = $1`},
	{"webhook_id", `SELECT workspace_id FROM webhook_configs WHERE id = $1`},
	{"int_id", `SELECT workspace_id FROM integration_configs WHERE id = $1`},
	{"tmpl_id", `SELECT p.workspace_id
	               FROM task_templates tt
	               JOIN projects p ON tt.project_id = p.id
	              WHERE tt.id = $1`},
	// :rule_id here is a governance rule. The project-level auto-transition rules
	// live in a different table and are spelled :auto_rule_id for that reason —
	// see the note on that entry below.
	{"rule_id", `SELECT workspace_id FROM rules WHERE id = $1`},
	{"link_id", `SELECT p.workspace_id
	               FROM vcs_links v
	               JOIN tasks t ON v.task_id = t.id AND t.deleted_at IS NULL
	               JOIN projects p ON t.project_id = p.id
	              WHERE v.id = $1`},
	{"recurring_id", `SELECT workspace_id FROM recurring_schedules WHERE id = $1`},

	// The child objects of a composite route. Each of these hangs off a parent
	// that is already resolvable (:proj_id, :task_id, :ws_id), which is precisely
	// why they were missed: the parent resolved first, satisfied the guard, and
	// the child id was never looked at. See resolveWorkspaceFromParams.
	{"status_id", `SELECT p.workspace_id
	                 FROM task_statuses s
	                 JOIN projects p ON s.project_id = p.id
	                WHERE s.id = $1`},
	{"dep_id", `SELECT p.workspace_id
	              FROM task_dependencies d
	              JOIN tasks t ON d.task_id = t.id AND t.deleted_at IS NULL
	              JOIN projects p ON t.project_id = p.id
	             WHERE d.id = $1`},
	// :auto_rule_id, not :rule_id. The parameter had to be renamed in the route
	// pattern (the public URL is unchanged) because one spelling cannot name two
	// tables: /rules/:rule_id is a row in `rules`, while
	// /projects/:proj_id/auto-transition-rules/:rule_id is a row in
	// `auto_transition_rules`. Resolving both through one entry would look up an
	// auto-transition rule's id in `rules`, find nothing, and refuse the request —
	// blocking the legitimate owner as surely as the intruder, while a test that
	// only asserts "the intruder gets 403" would still pass. The same rename was
	// applied to /recurring/:id for the same reason.
	{"auto_rule_id", `SELECT p.workspace_id
	                    FROM auto_transition_rules a
	                    JOIN projects p ON a.project_id = p.id
	                   WHERE a.id = $1`},
	{"invite_id", `SELECT workspace_id FROM user_invites WHERE id = $1`},
	{"member_agent_id", `SELECT workspace_id FROM agents WHERE id = $1 AND deleted_at IS NULL`},
}

// workspaceParamResolver maps a route parameter to the workspace of the object it
// names. It is the single place a parameter turns into a tenant, so that the set
// of parameters the guard fires on cannot drift from the set the resolver reads.
type workspaceParamResolver struct {
	// params are the spellings this resolver answers to; the first one present in
	// the request wins. Only :proj_id / :project_id has more than one — they are
	// two names for the same thing, kept for backward compatibility.
	params []string
	// resolve returns the workspace the value names, or false if it names none.
	// False is always the safe answer: the caller refuses rather than guesses.
	resolve func(ctx context.Context, deps workspaceResolverDeps, raw string) (uuid.UUID, bool)
}

// workspaceResolverDeps carries what the resolvers need to reach the database.
type workspaceResolverDeps struct {
	db          *sqlx.DB
	projectRepo repository.ProjectRepository
}

// sqlWorkspaceResolver builds a resolver for a parameter holding a uuid whose
// workspace one query answers. The query takes the uuid as $1 and returns exactly
// one workspace_id, or no rows if the object does not exist.
func sqlWorkspaceResolver(query string) func(context.Context, workspaceResolverDeps, string) (uuid.UUID, bool) {
	return func(ctx context.Context, deps workspaceResolverDeps, raw string) (uuid.UUID, bool) {
		id, err := uuid.Parse(raw)
		if err != nil || deps.db == nil {
			return uuid.Nil, false
		}
		var wsID uuid.UUID
		if err := deps.db.QueryRowContext(ctx, query, id).Scan(&wsID); err != nil {
			return uuid.Nil, false
		}
		return wsID, true
	}
}

// workspaceParamResolvers is every way a request can name a tenant through its
// path, in one table. WorkspaceScopedParams is derived from it.
var workspaceParamResolvers = buildWorkspaceParamResolvers()

func buildWorkspaceParamResolvers() []workspaceParamResolver {
	resolvers := []workspaceParamResolver{
		// A workspace names itself. There is no lookup: an id that exists but is
		// not the caller's is refused by the membership check, and one that does
		// not exist is refused the same way, which is also the answer that does
		// not confirm whether it exists.
		{params: []string{"ws_id"}, resolve: func(_ context.Context, _ workspaceResolverDeps, raw string) (uuid.UUID, bool) {
			id, err := uuid.Parse(raw)
			if err != nil {
				return uuid.Nil, false
			}
			return id, true
		}},
		{params: []string{"proj_id", "project_id"}, resolve: func(ctx context.Context, deps workspaceResolverDeps, raw string) (uuid.UUID, bool) {
			projID, err := uuid.Parse(raw)
			if err != nil || deps.projectRepo == nil {
				return uuid.Nil, false
			}
			proj, err := deps.projectRepo.GetByID(ctx, projID)
			if err != nil || proj == nil {
				return uuid.Nil, false
			}
			return proj.WorkspaceID, true
		}},
		// :task_id is a full uuid or a 6-12 hex character prefix of one:
		// resolveTaskID accepts both, so every /tasks/:task_id route does. The
		// prefix form has to resolve here too, or the guard would refuse a member
		// their own task whenever they addressed it the short way.
		{params: []string{"task_id"}, resolve: func(ctx context.Context, deps workspaceResolverDeps, raw string) (uuid.UUID, bool) {
			if _, err := uuid.Parse(raw); err != nil {
				return resolveWorkspaceByTaskPrefix(ctx, deps.db, raw)
			}
			return sqlWorkspaceResolver(
				`SELECT p.workspace_id
				   FROM tasks t
				   JOIN projects p ON t.project_id = p.id
				  WHERE t.id = $1 AND t.deleted_at IS NULL`)(ctx, deps, raw)
		}},
		// GET /tasks/by-short-id/:short takes a 6-12 hex character prefix of a
		// task uuid, matched with LIKE across every tenant's tasks. It carried no
		// guard at all, which made it both a cross-tenant read and an enumeration
		// oracle. An ambiguous prefix resolves to nothing on purpose: with two
		// tenants' tasks behind one prefix there is no single workspace to check
		// membership of, so the request is refused rather than guessed at.
		{params: []string{"short"}, resolve: func(ctx context.Context, deps workspaceResolverDeps, raw string) (uuid.UUID, bool) {
			return resolveWorkspaceByTaskPrefix(ctx, deps.db, raw)
		}},
		{params: []string{"artifact_id"}, resolve: sqlWorkspaceResolver(
			`SELECT p.workspace_id
			   FROM artifacts a
			   JOIN tasks t ON a.task_id = t.id AND t.deleted_at IS NULL
			   JOIN projects p ON t.project_id = p.id
			  WHERE a.id = $1`)},
		{params: []string{"agent_id"}, resolve: sqlWorkspaceResolver(
			`SELECT workspace_id FROM agents WHERE id = $1 AND deleted_at IS NULL`)},
		{params: []string{"field_id"}, resolve: sqlWorkspaceResolver(
			`SELECT p.workspace_id
			   FROM custom_field_definitions cf
			   JOIN projects p ON cf.project_id = p.id
			  WHERE cf.id = $1`)},
		{params: []string{"init_id"}, resolve: sqlWorkspaceResolver(
			`SELECT workspace_id FROM initiatives WHERE id = $1`)},
	}
	for _, r := range workspaceObjectResolvers {
		resolvers = append(resolvers, workspaceParamResolver{
			params:  []string{r.param},
			resolve: sqlWorkspaceResolver(r.query),
		})
	}
	return resolvers
}

// workspaceResolverParams flattens every parameter spelling the resolvers read.
func workspaceResolverParams() []string {
	var params []string
	for _, r := range workspaceParamResolvers {
		params = append(params, r.params...)
	}
	return params
}

// workspaceUnscopedParams names route parameters that do not identify a
// tenant-owned row, so requiring a workspace to be resolvable from one would be
// meaningless rather than safe.
//
// :user_id is a global user account id. The routes carrying it —
// /workspaces/:ws_id/members/:user_id, /projects/:proj_id/members/:user_id — name
// their tenant with the parameter in front of it, and the row they act on is the
// membership itself, which is keyed on both: workspaceMemberService.RemoveMember
// looks up GetByWorkspaceAndUser and answers 404 when there is no such row, so a
// user id belonging to another tenant selects nothing here.
//
// TestEveryIdentifiedRouteIsWorkspaceScoped still requires such a route to carry
// at least one parameter that DOES resolve a tenant — this list excuses a
// parameter from resolving, never a route from being scoped.
var workspaceUnscopedParams = map[string]bool{
	"user_id": true,
}

// resolveWorkspaceFromParams resolves EVERY workspace-scoping parameter the path
// carries and reports the one workspace they all name.
//
// It returns named (the path carried at least one such parameter) and agreed
// (every one of them resolved, and to the same workspace). The caller treats
// named && agreed as "the request named a tenant"; anything else falls through to
// the credentials-derived workspace, which RequireWorkspaceMemberScoped then
// refuses on a scoped route.
//
// Resolving all of them rather than the first that answers is the whole fix.
// Every one of these routes names a parent and a child:
//
//	PATCH  /projects/:proj_id/statuses/:status_id
//	DELETE /projects/:proj_id/auto-transition-rules/:auto_rule_id
//	DELETE /tasks/:task_id/dependencies/:dep_id
//	DELETE /workspaces/:ws_id/invites/:invite_id
//	GET    /tasks/:task_id/artifacts/:artifact_id/download
//	DELETE /initiatives/:init_id/projects/:proj_id
//
// The old resolver stopped at the first parameter that produced a workspace, and
// the parent comes first in all of them. So a caller supplied a parent they
// legitimately own together with a child id belonging to somebody else, the guard
// checked their membership of their own parent, passed, and the handler then acted
// on the child by its id alone. That renamed another tenant's status, deleted
// another tenant's auto-transition rule, and deleted another tenant's dependency
// edge — with a 200 and a real write behind it, from an ordinary account.
//
// A parameter that resolves to nothing makes the whole request unresolved rather
// than being skipped: an unknown child id is exactly what a probe looks like, and
// treating it as "nothing to check here" is how the first version of this guard
// was bypassed.
func resolveWorkspaceFromParams(c echo.Context, deps workspaceResolverDeps) (wsID uuid.UUID, named, agreed bool) {
	agreed = true
	have := false

	for _, r := range workspaceParamResolvers {
		raw := ""
		for _, name := range r.params {
			if v := c.Param(name); v != "" {
				raw = v
				break
			}
		}
		if raw == "" {
			continue
		}
		named = true

		got, ok := r.resolve(c.Request().Context(), deps, raw)
		if !ok {
			return uuid.Nil, true, false
		}
		if have && got != wsID {
			// The request named two tenants. There is no safe way to pick one:
			// checking membership of either would authorize an action on the
			// other.
			return uuid.Nil, true, false
		}
		wsID, have = got, true
	}

	return wsID, named, agreed
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
var workspaceScopeHandlerCheckedRoutes = map[string]bool{
	"/api/v1/memories/:id":         true,
	"/api/v1/memories/:id/related": true,
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
//  1. every workspace-scoping route parameter present in the path, via
//     workspaceParamResolvers — see resolveWorkspaceFromParams for why it is
//     every one of them and not the first that answers
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
			// 1. Resolve every workspace-scoping parameter the path carries.
			wsID, named, agreed := resolveWorkspaceFromParams(
				c, workspaceResolverDeps{db: db, projectRepo: projectRepo})
			resolved := named && agreed
			// Anything a parameter resolved is a workspace the request named; the
			// auth-context fallback below overrides this.
			source := WorkspaceSourceParam

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
// this guard had nothing to check — see workspaceObjectResolvers. The fix is a
// resolver per object rather than a membership check per handler, so that the
// invariant stays "the router names a tenant, the guard checks it" and
// TestEveryIdentifiedRouteIsWorkspaceScoped can hold the whole class shut.
//
// Three cases, in order:
//
//  1. No workspace-scoping parameter in the path (/auth/me, /notifications,
//     /memories/:id): nothing to check, pass through.
//  2. A parameter is present and WorkspaceRLS resolved a workspace *from it*:
//     require membership of that workspace.
//  3. A parameter is present and no workspace came out of it: refuse. The
//     workspace in context, if any, is the caller's own (agent key auth), which
//     says nothing about the object they asked for — treating that as permission
//     is how case 2 gets bypassed. This is already how the per-route guard behaved
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
		guarded := guard(next)
		return func(c echo.Context) error {
			if !routeIsWorkspaceScoped(c) {
				return next(c)
			}
			if workspaceScopeExemptRoutes[c.Path()] {
				return next(c)
			}
			if src, _ := c.Get(ContextKeyWorkspaceSource).(string); src != WorkspaceSourceParam {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace access denied"))
			}
			return guarded(c)
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
