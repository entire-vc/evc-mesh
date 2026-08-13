package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// This file closes the fourth channel by which a caller puts another tenant into
// a request.
//
// The first three were closed in July and each got an invariant of its own: the
// tenant in the path (TestEveryIdentifiedRouteIsWorkspaceScoped), the tenant in
// the request body (TestBodyTenantFieldsAreDeclared), the tenant in the query
// string. All three share a shape — the workspace id ARRIVES in the request, so a
// test that reads routes or request structs can find it and demand a guard.
//
// This one has no such shape. The caller names a PRINCIPAL by id, and the tenant
// is derived from the object behind that id. Nothing in the path, the body or the
// query names workspace B; the request is a perfectly ordinary
// "assign task T to agent A" from a member of workspace A. So none of the three
// existing invariants can see it, and none of them fired.
//
// What made it worse than a read: the principal directory is global
// (AgentRepo.GetByID has no workspace predicate), TaskRepo.ListByAssignee has no
// workspace predicate either, and the agent-notify path posts a task snapshot to
// the assignee's callback_url. So the system did not merely fail to hide the task
// — it delivered the task to a principal that should not have been able to name
// it. Postgres RLS is no backstop here and was never meant to be: the backend
// connects as the table owner and FORCE ROW LEVEL SECURITY is deliberately off
// (migrations/20260301030_enable_rls_policies.sql). The application check IS the
// boundary.
//
// TestEveryAssigneeWritePathIsWorkspaceChecked holds the class shut by refusing
// any new site that writes assignee_id without going through the funnel below.

// AssigneeNotInWorkspaceError reports an attempt to point a task at a principal
// that does not belong to the task's workspace.
//
// It is deliberately a distinct type rather than a generic 403: the CALLER is
// usually a legitimate member acting in their own workspace, and the thing being
// refused is the principal they named, not their own right to be here. Saying
// "forbidden" would send them looking at their own permissions.
type AssigneeNotInWorkspaceError struct {
	// PrincipalID, not AssigneeID: the assignee-write invariant scans for writes to
	// a field of that name, and an error carrying one would report this guard as a
	// path that needs guarding.
	PrincipalID  uuid.UUID
	AssigneeType domain.AssigneeType
	ProjectID    uuid.UUID
	// Reason is safe to return to the caller: it never names the workspace the
	// principal actually belongs to, because that would turn this refusal into an
	// oracle for probing which ids exist in which tenant.
	Reason string
}

func (e *AssigneeNotInWorkspaceError) Error() string {
	return fmt.Sprintf(
		"assignee %s (type %s) may not be assigned to a task in project %s: %s",
		e.PrincipalID, e.AssigneeType, e.ProjectID, e.Reason)
}

// AssigneeUnresolvedError reports an assignee_id that names no principal at all —
// neither an agent nor a user — and therefore could not be typed.
//
// It is separate from AssigneeNotInWorkspaceError because the two are different
// mistakes and want different corrections: that one means "this principal exists,
// but not here", this one means "this id is not anybody". Answering the second with
// the first sends the caller to compare workspaces for an id that has no workspace.
//
// Why this must be an error rather than a shrug: resolveAssigneeType falls back to
// the caller's declared type when the directory lookup finds nothing, so an
// unresolvable id keeps whatever type the caller sent. When that type is a
// principal type the tenancy guard already refuses it. When it is "unassigned" (or
// empty) the guard used to skip out — correctly, on its own terms, since nothing
// typed "unassigned" can be read back through ListByAssignee — and the row was
// written with a dangling assignee_id and no assignment.
//
// That silence is the whole defect. The card looks created and carries an id that
// reads like a real one; nobody is assigned, so no lane is ever fed, and the work
// simply does not happen. Two such rows are in this database (2026-03-26 and
// 2026-06-13), and the first names 7bcbd908-0000-0000-0000-000000000000 — the
// zero-padded form of agent howard's short id, i.e. exactly the failure this
// message is written to pre-empt.
type AssigneeUnresolvedError struct {
	PrincipalID  uuid.UUID
	AssigneeType domain.AssigneeType
	ProjectID    uuid.UUID
}

func (e *AssigneeUnresolvedError) Error() string {
	return fmt.Sprintf(
		"assignee_id %s names no agent and no user, so it could not be typed "+
			"(arrived as type %q) and cannot be assigned to a task in project %s",
		e.PrincipalID, e.AssigneeType, e.ProjectID)
}

// assertAssigneeIsTyped refuses an assignee_id that survived type resolution
// without becoming a principal.
//
// The invariant: assignee_id is non-null IF AND ONLY IF assignee_type is a
// principal type. The pair is the pointer; half of it is not a weaker assignment,
// it is a dangling one. Clearing an assignee is expressed by sending no id at all,
// which this function permits (see the nil checks), so nothing legitimate needs the
// contradictory shape.
func assertAssigneeIsTyped(projectID uuid.UUID, assigneeID *uuid.UUID, assigneeType domain.AssigneeType) error {
	if assigneeID == nil || *assigneeID == uuid.Nil {
		return nil
	}
	if assigneeType == domain.AssigneeTypeAgent || assigneeType == domain.AssigneeTypeUser {
		return nil
	}
	return &AssigneeUnresolvedError{
		PrincipalID:  *assigneeID,
		AssigneeType: assigneeType,
		ProjectID:    projectID,
	}
}

// WorkspaceMembershipReader reports whether a human principal may act inside a
// workspace — a workspace_members row, or the owner fallback for the workspaces
// whose owner row was never written.
//
// It is a one-method interface rather than the full WorkspaceMemberRepository
// because this guard asks exactly one question, and a narrow port is a narrow
// mock. The production implementation is postgres.NewWorkspaceMembershipReader,
// which runs the same rule as middleware.UserIsWorkspaceMember.
type WorkspaceMembershipReader interface {
	IsWorkspaceMember(ctx context.Context, workspaceID, userID uuid.UUID) (bool, error)
}

// WithWorkspaceMembershipReader wires the human half of the assignee tenancy
// check. Without it the user path cannot be decided, and an undecidable check
// refuses (see assertAssigneeInProjectWorkspace).
func WithWorkspaceMembershipReader(r WorkspaceMembershipReader) TaskServiceOption {
	return func(s *taskService) {
		s.wsMembership = r
	}
}

// assertAssigneeInProjectWorkspace refuses an assignee that does not belong to
// the workspace owning projectID.
//
// FAIL-CLOSED, deliberately, and this is the one place in the assignee machinery
// where that is the right call. Its sibling ensureAgentProjectMember fails OPEN on
// a lookup error on purpose: it grants a membership, so a transient blip must not
// turn into a missing enrolment and strand a real assignee. This function REFUSES
// a grant, so the two failure directions are not symmetric — "I could not check"
// must never read as "I checked and it was fine". That equivalence is exactly the
// defect this file exists to close, one layer up.
//
// The cost of that choice is honest and worth stating: a database blip during an
// assignment surfaces as a failed assignment the caller retries, rather than as a
// silent cross-tenant grant nobody ever sees.
//
// Only the agent and user types are checked, matching ensureAssigneeProjectMember's
// own dispatch. A non-nil assignee id carrying any other type grants nothing that
// can be read back: TaskRepo.ListByAssignee filters on assignee_id AND
// assignee_type together, so a row typed "unassigned" never matches the
// principal's own GET /agents/me/tasks, and no enrolment row is written for it
// either.
func (s *taskService) assertAssigneeInProjectWorkspace(
	ctx context.Context,
	projectID uuid.UUID,
	assigneeID *uuid.UUID,
	assigneeType domain.AssigneeType,
) error {
	if assigneeID == nil || *assigneeID == uuid.Nil {
		return nil
	}
	if assigneeType != domain.AssigneeTypeAgent && assigneeType != domain.AssigneeTypeUser {
		return nil
	}

	refuse := func(reason string) error {
		return &AssigneeNotInWorkspaceError{
			PrincipalID:  *assigneeID,
			AssigneeType: assigneeType,
			ProjectID:    projectID,
			Reason:       reason,
		}
	}

	// The task's tenant is the project's workspace. Not being able to resolve it
	// is not a reason to proceed — it is the whole question.
	if s.projectRepo == nil {
		return refuse("project directory unavailable, cannot establish the task's workspace")
	}
	proj, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return refuse("could not read the task's project")
	}
	if proj == nil {
		return refuse("the task's project does not exist")
	}
	workspaceID := proj.WorkspaceID

	switch assigneeType {
	case domain.AssigneeTypeAgent:
		if s.agentRepo == nil {
			return refuse("agent directory unavailable")
		}
		agent, aerr := s.agentRepo.GetByID(ctx, *assigneeID)
		if aerr != nil {
			return refuse("could not read the agent directory")
		}
		if agent == nil {
			return refuse("no such agent")
		}
		if agent.WorkspaceID != workspaceID {
			return refuse("agent belongs to a different workspace")
		}

	case domain.AssigneeTypeUser:
		if s.wsMembership == nil {
			return refuse("workspace membership directory unavailable")
		}
		member, merr := s.wsMembership.IsWorkspaceMember(ctx, workspaceID, *assigneeID)
		if merr != nil {
			return refuse("could not read workspace membership")
		}
		if !member {
			return refuse("user is not a member of this workspace")
		}
	}

	return nil
}
