package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Permission represents a specific action that may be restricted by role.
type Permission string

const (
	PermDeleteWorkspace Permission = "delete_workspace"
	PermManageMembers   Permission = "manage_members"
	PermCreateProject   Permission = "create_project"
	PermDeleteProject   Permission = "delete_project"
	// PermRegisterAgent gates POST /workspaces/:ws_id/agents. It is
	// deliberately absent from agentPerms — checked six independent ways on
	// 21.08 (#85fd1ef2), no agent self-registration path has ever existed on
	// prod, and workspace role does not change that: RequirePermission's
	// agent branch checks agentPerms by ACTOR TYPE (IsAgent(c)), never by the
	// role a grant carries, so an admin-role grant is irrelevant to this
	// check — same shape as PermManageSecrets below. Registering a new agent
	// identity is a human-only action: an agent that could register agents
	// could mint itself siblings holding whatever grants it chose.
	PermRegisterAgent  Permission = "register_agent"
	PermDeleteAgent    Permission = "delete_agent"
	PermCreateTask     Permission = "create_task"
	PermUpdateTask     Permission = "update_task"
	PermDeleteTask     Permission = "delete_task"
	PermAddComment     Permission = "add_comment"
	PermUploadArtifact Permission = "upload_artifact"
	PermPublishEvent   Permission = "publish_event"
	PermManageCF       Permission = "manage_custom_fields"
	PermExportAuditLog Permission = "export_audit_log"
	PermManageWebhooks Permission = "manage_webhooks"
	PermManageRules    Permission = "manage_rules"
	// PermManageSecrets gates the write-only secret store (task #64e84eb1).
	// It is deliberately absent from agentPerms: the whole point of the store
	// is that a human hands over a credential and no agent identity can read
	// it back, and an agent able to rotate or delete a secret could replace a
	// value with one it chose and then materialize it — a read of its own
	// plaintext by another route. Members and viewers are excluded for the
	// same reason the masked list is gated: sha256[:8] plus length plus
	// character class is a fingerprint, and confirming a guess against it is
	// cheaper than not having it.
	PermManageSecrets Permission = "manage_secrets"

	// The next three exist for RequireConnectorPermission only: they are the
	// role bar for routes that carry no rbac() (and must not gain one, because
	// that would narrow what humans and trusted X-Agent-Key agents can do
	// there). They are deliberately not in agentPerms — nothing reads them for
	// an agk_ agent.
	//
	// PermManageProject: rename/re-describe a project and change its status
	// set. Owner/admin only — a member creates projects but does not
	// restructure them through a connector.
	PermManageProject Permission = "manage_project"
	// PermWriteMemory: write or delete workspace/project memory and project
	// knowledge. Owner/admin/member; a viewer is read-only.
	PermWriteMemory Permission = "write_memory"
	// PermMemoryIndex: rebuild the memory search index (re-embed, backfill
	// chunks). Each call spends embedding quota, so owner/admin only.
	PermMemoryIndex Permission = "memory_index"
)

// permissionMatrix maps a role name to the set of permissions it holds.
// owner has all permissions.
// admin mirrors owner for now (same as owner); add distinctions as product evolves.
// member can create/update/delete tasks, comments, artifacts, events, and manage CF.
// viewer has no write permissions (read-only access is handled at the route level).
// agent can perform task/comment/artifact/event operations only.
var permissionMatrix = map[string]map[Permission]bool{
	domain.RoleOwner: {
		PermDeleteWorkspace: true,
		PermManageMembers:   true,
		PermCreateProject:   true,
		PermDeleteProject:   true,
		PermRegisterAgent:   true,
		PermDeleteAgent:     true,
		PermCreateTask:      true,
		PermUpdateTask:      true,
		PermDeleteTask:      true,
		PermAddComment:      true,
		PermUploadArtifact:  true,
		PermPublishEvent:    true,
		PermManageCF:        true,
		PermExportAuditLog:  true,
		PermManageWebhooks:  true,
		PermManageRules:     true,
		PermManageSecrets:   true,
		PermManageProject:   true,
		PermWriteMemory:     true,
		PermMemoryIndex:     true,
	},
	domain.RoleAdmin: {
		// Admin has the same permissions as owner.
		PermDeleteWorkspace: true,
		PermManageMembers:   true,
		PermCreateProject:   true,
		PermDeleteProject:   true,
		PermRegisterAgent:   true,
		PermDeleteAgent:     true,
		PermCreateTask:      true,
		PermUpdateTask:      true,
		PermDeleteTask:      true,
		PermAddComment:      true,
		PermUploadArtifact:  true,
		PermPublishEvent:    true,
		PermManageCF:        true,
		PermExportAuditLog:  true,
		PermManageWebhooks:  true,
		PermManageRules:     true,
		PermManageSecrets:   true,
		PermManageProject:   true,
		PermWriteMemory:     true,
		PermMemoryIndex:     true,
	},
	domain.RoleMember: {
		PermCreateProject:  true,
		PermCreateTask:     true,
		PermUpdateTask:     true,
		PermDeleteTask:     true,
		PermAddComment:     true,
		PermUploadArtifact: true,
		PermPublishEvent:   true,
		PermManageCF:       true,
		PermWriteMemory:    true,
	},
	domain.RoleViewer: {
		// Viewer: read-only, no write actions.
	},
}

// agentPerms defines which permissions agents (authenticated via X-Agent-Key) hold.
// Agents can perform task/comment/artifact/event operations and manage workspace rules.
// PermManageRules is included so that designated lead agents (e.g. Garfield) can apply
// gate rules (capacity_limit, transition_gate) without requiring a human JWT.
var agentPerms = map[Permission]bool{
	PermCreateTask:     true,
	PermUpdateTask:     true,
	PermDeleteTask:     true,
	PermAddComment:     true,
	PermUploadArtifact: true,
	PermPublishEvent:   true,
	PermManageRules:    true,
}

// RequirePermission returns Echo middleware that enforces a specific permission.
//
// For agents (authenticated via X-Agent-Key): checks agentPerms map — no DB lookup.
// For users (authenticated via JWT): looks up the workspace role from workspace_members
// and checks the permissionMatrix — one SELECT per request.
//
// The workspace_id is resolved from the Echo context (set by WorkspaceRLS middleware
// or by AgentKeyAuth). Routes that do not have a workspace in context will return 403.
func RequirePermission(perm Permission, memberRepo repository.WorkspaceMemberRepository) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// --- Agents: fast-path, no DB lookup — EXCEPT an OAuth connector
			// (mot_ token, MCP-OAuth 1/5), which costs one extra SELECT to
			// clamp its permissions to the consenting user's CURRENT
			// workspace role. A connector must never hold more than the
			// human who authorized it holds right now — not at consent
			// time, on every request — otherwise a viewer/member who
			// consents once ends up granting a fixed-permission agent
			// identity (agentPerms, meant for trusted lead agents) that
			// outlives their own actual access, including PermManageRules.
			//
			// Scope of that guarantee: this clamp only runs on routes registered
			// behind rbac(...). A route without it is not covered by THIS function,
			// so the same guarantee is delivered there by
			// RequireConnectorPermission / RequireConnectorSelfOrPermission (same
			// lookup, same 403, but a no-op for humans and X-Agent-Key agents, whose
			// access on those routes is deliberately unchanged). A route with none of
			// the three must be in the reviewed allow-list of
			// cmd/api/rbac_routes_audit_test.go, which fails the build otherwise — that
			// test, not this comment, is what keeps "a connector never holds more
			// than its user" true as routes are added. Read routes are outside that
			// promise by design: a viewer's connector reads what the viewer reads,
			// and the tenant boundary on them is group-wide (WorkspaceRLS).
			if IsAgent(c) {
				if !agentPerms[perm] {
					// PermRegisterAgent gets its own message: the generic
					// text leaves whoever granted this agent an admin role
					// wondering why admin didn't help. Naming the actual
					// boundary (actor type, not role — see PermRegisterAgent's
					// doc comment) answers that without them having to read
					// the source.
					if perm == PermRegisterAgent {
						return c.JSON(http.StatusForbidden, apierror.Forbidden("agent registration requires a human credential — no workspace role lets an agent register another agent"))
					}
					return c.JSON(http.StatusForbidden, apierror.Forbidden("agents cannot perform this action"))
				}

				// nil owner check: rbac() routes keep the membership-row-only
				// lookup they always had; the owner fallback is for the routes
				// this change newly guards (see WorkspaceOwnerCheck).
				if denied, err := clampConnector(c, perm, memberRepo, nil); denied {
					return err
				}

				return next(c)
			}

			// --- Users: resolve workspace_id and look up role. ---
			wsID, err := GetWorkspaceID(c)
			if err != nil {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("workspace context required"))
			}

			userID, err := GetUserID(c)
			if err != nil {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("user context required"))
			}

			role, err := memberRepo.GetRole(c.Request().Context(), wsID, userID)
			if err != nil {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("not a workspace member"))
			}

			if !hasPermission(role, perm) {
				return c.JSON(http.StatusForbidden, apierror.Forbidden("insufficient permissions"))
			}

			return next(c)
		}
	}
}

// RequireSelfOrPermission returns Echo middleware for routes whose path carries
// an agent_id that the caller may always manage for themselves (e.g. an agent
// updating its own profile via X-Agent-Key), but which requires an explicit
// permission to manage on someone else's behalf.
//
// The path param named agentIDParam is compared against the authenticated
// agent's own ID (agent auth only — users have no agent identity to match
// against). On a match the request proceeds unconditionally, no permission
// lookup. On a mismatch, or for user auth, it falls back to the same check
// RequirePermission would perform — so a user still needs perm via their
// workspace role, and an agent acting on another agent's resource still needs
// perm via agentPerms (which today grants none of the management perms to any
// agent role — cross-agent management is JWT-owner/admin-only until a
// dedicated agent permission is introduced).
func RequireSelfOrPermission(agentIDParam string, perm Permission, memberRepo repository.WorkspaceMemberRepository) echo.MiddlewareFunc {
	fallback := RequirePermission(perm, memberRepo)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		guarded := fallback(next)
		return func(c echo.Context) error {
			if IsAgent(c) {
				if callerID, err := GetAgentID(c); err == nil {
					if targetID, perr := uuid.Parse(c.Param(agentIDParam)); perr == nil && targetID == callerID {
						return next(c)
					}
				}
			}
			return guarded(c)
		}
	}
}

// WorkspaceOwnerCheck reports whether userID owns workspace wsID. It is the
// owner fallback for a workspace whose owner has no workspace_members row (a
// documented possibility: see oauthService.resolveMemberRole). nil disables the
// fallback. In production it is UserOwnsWorkspace bound to the database.
type WorkspaceOwnerCheck func(ctx context.Context, wsID, userID uuid.UUID) bool

// clampConnector applies the OAuth-connector role clamp. denied is false when
// the caller is not a connector (nothing to clamp) or its consenting user's
// CURRENT workspace role holds perm; when true the 403 has already been written
// and the caller must return err WITHOUT calling next. One SELECT, and only for
// connectors.
//
// The two-value shape is deliberate: c.JSON returns nil on a successful write,
// so returning only its error would make "denied" indistinguishable from
// "allowed" and let the handler run after the 403 was sent — the response would
// look right and the write would still happen. (The first version of this
// helper did exactly that; only a real request against a real handler showed it.)
//
// Fail-closed: a missing workspace, a role lookup error and a role that is no
// longer a member all deny. Shared by RequirePermission and the connector-only
// middlewares below so the two cannot drift.
func clampConnector(c echo.Context, perm Permission, memberRepo repository.WorkspaceMemberRepository, ownsWorkspace WorkspaceOwnerCheck) (denied bool, err error) {
	connectorUserID, ok := GetOAuthConnectorUserID(c)
	if !ok {
		return false, nil
	}
	wsID, wsErr := GetAgentAuthWorkspaceID(c)
	if wsErr != nil {
		return true, c.JSON(http.StatusForbidden, apierror.Forbidden("workspace context required"))
	}
	role, roleErr := memberRepo.GetRole(c.Request().Context(), wsID, connectorUserID)
	if roleErr != nil && ownsWorkspace != nil && ownsWorkspace(c.Request().Context(), wsID, connectorUserID) {
		// The owner of a workspace whose own membership row was never written.
		// The OAuth service admits such an owner at consent (resolveMemberRole)
		// and the workspace guard admits them on every other route, so the clamp
		// must not turn a connector they legitimately authorised into a 403 on
		// the routes it now guards.
		role, roleErr = domain.RoleOwner, nil
	}
	if roleErr != nil || !hasPermission(role, perm) {
		return true, c.JSON(http.StatusForbidden, apierror.Forbidden("insufficient permissions — the connected user's workspace role no longer grants this"))
	}
	return false, nil
}

// RequireConnectorPermission is the role bar for a route that has no rbac():
// it clamps an OAuth connector (mot_) to what its consenting user's current
// workspace role holds, and does nothing for anyone else.
//
// Why not just rbac() on those routes: rbac() also applies to humans and to
// trusted X-Agent-Key agents. Adding it would silently narrow what a member can
// do in the web app on the same route, and agentPerms is shorter than the
// matrix, so agents such as the lead lanes would start getting 403 on routes
// they use today. The connector is new and has no such history; a role bar is
// safe to introduce there, and only there.
//
// Runs after DualAuth and WorkspaceRLS (needs the workspace the token is bound
// to). Unlike RequirePermission it does NOT consult agentPerms.
func RequireConnectorPermission(perm Permission, memberRepo repository.WorkspaceMemberRepository, ownsWorkspace WorkspaceOwnerCheck) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if IsAgent(c) {
				if denied, err := clampConnector(c, perm, memberRepo, ownsWorkspace); denied {
					return err
				}
			}
			return next(c)
		}
	}
}

// RequireConnectorSelfOrPermission is RequireConnectorPermission for a route
// whose :agentIDParam names an agent: a connector may always act on its own
// identity, and on another agent's only if its user's role holds perm.
// A malformed id falls through to the permission check (never to "self").
func RequireConnectorSelfOrPermission(agentIDParam string, perm Permission, memberRepo repository.WorkspaceMemberRepository, ownsWorkspace WorkspaceOwnerCheck) echo.MiddlewareFunc {
	bar := RequireConnectorPermission(perm, memberRepo, ownsWorkspace)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		guarded := bar(next)
		return func(c echo.Context) error {
			if _, isConnector := GetOAuthConnectorUserID(c); isConnector {
				if callerID, err := GetAgentID(c); err == nil {
					if targetID, perr := uuid.Parse(c.Param(agentIDParam)); perr == nil && targetID == callerID {
						return next(c)
					}
				}
			}
			return guarded(c)
		}
	}
}

// RoleHasPermission reports whether a workspace role holds a permission, using
// the same matrix as RequirePermission.
//
// It is exported for the one caller that cannot use the middleware: Spark's
// install route takes its target workspace from the request body rather than the
// path, so no middleware can see which workspace to check. Copying the matrix
// into that handler would let the two drift; borrowing it cannot.
func RoleHasPermission(role string, perm Permission) bool {
	return hasPermission(role, perm)
}

// hasPermission returns true if the given role holds the given permission.
func hasPermission(role string, perm Permission) bool {
	perms, ok := permissionMatrix[role]
	if !ok {
		return false
	}
	return perms[perm]
}
