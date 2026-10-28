package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Account edits by a workspace admin: email, password and username of a member.
//
// Unlike the display name these are sign-in identity, and they live on the
// account, not on the membership. Rewriting another person's address or
// password from inside one workspace is a takeover of every other workspace
// they belong to, so each write here first proves the actor administers ALL of
// the target's workspaces, not just this one (authorizeAccountEdit).

// ErrSessionsNotRevoked is returned by SetMemberPassword when the new password
// is stored and audited but the member's sessions could not be ended. The
// password HAS changed: callers finish what follows a change (clearing the
// login lockout) and still report the failure.
var ErrSessionsNotRevoked = errors.New("password changed but sessions not revoked")

// RefreshTokenRevoker ends a user's sessions (repository.RefreshTokenRepository).
type RefreshTokenRevoker interface {
	RevokeByUserID(ctx context.Context, userID uuid.UUID) error
}

// SetAccountEditDeps wires the workspace lookup (for the cross-workspace
// authority check) and the refresh-token store (to end the target's sessions
// after a password change). They come through a setter rather than the
// constructor so the existing NewWorkspaceMemberService call sites stay as they
// are; without the workspace lookup every account edit is refused rather than
// run with a partial check.
func (s *workspaceMemberService) SetAccountEditDeps(workspaces repository.WorkspaceRepository, tokens RefreshTokenRevoker) {
	s.workspaceRepo = workspaces
	s.refreshTokens = tokens
}

// memberRoleRank orders workspace roles so "actor is at least target" is a
// comparison. Unknown roles rank 0 and therefore never outrank anyone.
func memberRoleRank(role string) int {
	switch role {
	case domain.RoleOwner:
		return 4
	case domain.RoleAdmin:
		return 3
	case domain.RoleMember:
		return 2
	case domain.RoleViewer:
		return 1
	}
	return 0
}

// accountEditTarget is what authorizeAccountEdit proved and loaded.
type accountEditTarget struct {
	member     *domain.WorkspaceMember
	user       *domain.User
	workspaces []domain.Workspace
}

// authorizeAccountEdit decides whether the acting person may change the sign-in
// identity of targetUserID. The rbac middleware already limited the route to
// owners and admins of workspaceID; this adds what a role bar cannot express:
//
//   - only a person acts here, never an agent key;
//   - not on yourself — your own account goes through the profile flow, which
//     asks for what it should (400);
//   - not above your own role: an admin cannot rewrite an owner (403);
//   - not a service account that speaks for an agent (403);
//   - and in every OTHER workspace the target belongs to, the actor must be an
//     owner or admin at least as senior as the target there (403). Otherwise an
//     admin of a small workspace could reset the password of someone who owns a
//     large one and sign in as them.
func (s *workspaceMemberService) authorizeAccountEdit(ctx context.Context, workspaceID, targetUserID uuid.UUID) (*accountEditTarget, error) {
	actorID, actorType := actorctx.FromContext(ctx)
	if actorType != domain.ActorTypeUser || actorID == uuid.Nil {
		return nil, apierror.Forbidden("only a workspace owner or admin signed in as a person can change another member's account")
	}
	if actorID == targetUserID {
		return nil, apierror.BadRequest("change your own email, username or password in your profile settings")
	}
	if s.workspaceRepo == nil {
		return nil, apierror.InternalError("account editing is not configured on this instance")
	}

	member, err := s.memberRepo.GetByWorkspaceAndUser(ctx, workspaceID, targetUserID)
	if err != nil {
		return nil, fmt.Errorf("workspace_member_service.authorizeAccountEdit: %w", err)
	}
	if member == nil {
		return nil, apierror.NotFound("WorkspaceMember")
	}
	if rankErr := s.actorOutranks(ctx, workspaceID, actorID, member.Role); rankErr != nil {
		return nil, rankErr
	}

	user, err := s.userRepo.GetByID(ctx, targetUserID)
	if err != nil {
		return nil, fmt.Errorf("workspace_member_service.authorizeAccountEdit: %w", err)
	}
	if user == nil {
		return nil, apierror.NotFound("User")
	}
	if user.ServiceAgentID != nil {
		return nil, apierror.Forbidden("this is a service account of an agent — manage it through the agent, not as a member")
	}

	workspaces, err := s.workspaceRepo.ListForUser(ctx, targetUserID)
	if err != nil {
		return nil, fmt.Errorf("workspace_member_service.authorizeAccountEdit: %w", err)
	}
	for _, ws := range workspaces {
		if ws.ID == workspaceID {
			continue
		}
		targetRole := ""
		other, err := s.memberRepo.GetByWorkspaceAndUser(ctx, ws.ID, targetUserID)
		if err != nil {
			return nil, fmt.Errorf("workspace_member_service.authorizeAccountEdit: %w", err)
		}
		if other != nil {
			targetRole = other.Role
		}
		if ws.OwnerID == targetUserID {
			targetRole = domain.RoleOwner
		}
		if err := s.actorOutranks(ctx, ws.ID, actorID, targetRole); err != nil {
			var apiErr *apierror.Error
			if errors.As(err, &apiErr) && apiErr.StatusCode() == http.StatusForbidden {
				return nil, apierror.Forbidden("this person also belongs to a workspace you do not administer — only someone who administers all of their workspaces can change their sign-in details")
			}
			return nil, err
		}
	}

	return &accountEditTarget{member: member, user: user, workspaces: workspaces}, nil
}

// actorOutranks returns nil when actorID is an owner or admin of workspaceID
// with a role at least as senior as targetRole, and a 403 otherwise.
func (s *workspaceMemberService) actorOutranks(ctx context.Context, workspaceID, actorID uuid.UUID, targetRole string) error {
	actor, err := s.memberRepo.GetByWorkspaceAndUser(ctx, workspaceID, actorID)
	if err != nil {
		return fmt.Errorf("workspace_member_service.actorOutranks: %w", err)
	}
	if actor == nil || memberRoleRank(actor.Role) < memberRoleRank(domain.RoleAdmin) {
		return apierror.Forbidden("only a workspace owner or admin can change another member's account")
	}
	if memberRoleRank(actor.Role) < memberRoleRank(targetRole) {
		return apierror.Forbidden("an admin cannot change the account of an owner")
	}
	return nil
}

// SetMemberEmail changes a member's login address.
func (s *workspaceMemberService) SetMemberEmail(ctx context.Context, workspaceID, targetUserID uuid.UUID, email string) error {
	email = auth.NormalizeEmail(email)
	if email == "" || !auth.ValidEmail(email) {
		return apierror.ValidationError(map[string]string{"email": "a valid email address is required"})
	}

	t, err := s.authorizeAccountEdit(ctx, workspaceID, targetUserID)
	if err != nil {
		return err
	}
	previous := t.user.Email
	if auth.NormalizeEmail(previous) == email {
		return nil
	}

	existing, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("workspace_member_service.SetMemberEmail: %w", err)
	}
	if existing != nil && existing.ID != targetUserID {
		return apierror.Conflict("a user with this email already exists")
	}
	if err := s.userRepo.UpdateEmail(ctx, targetUserID, email); err != nil {
		if errors.Is(err, repository.ErrEmailTaken) {
			return apierror.Conflict("a user with this email already exists")
		}
		return fmt.Errorf("workspace_member_service.SetMemberEmail: %w", err)
	}

	s.logMemberActivity(ctx, workspaceID, t.member.ID, "member.email_changed", map[string]interface{}{
		"user_id":   targetUserID.String(),
		"old_email": previous,
		"new_email": email,
	})
	return nil
}

// SetMemberPassword sets a new password for a member and ends every session
// they had: whoever held the old password, or a token minted with it, is out.
// The password itself is never logged, stored in the activity entry or
// returned — the audit row records only who reset whose password.
func (s *workspaceMemberService) SetMemberPassword(ctx context.Context, workspaceID, targetUserID uuid.UUID, password string) error {
	t, err := s.authorizeAccountEdit(ctx, workspaceID, targetUserID)
	if err != nil {
		return err
	}
	if policyErr := auth.ValidatePassword(password); policyErr != nil {
		return policyErr
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), userPasswordBcryptCost)
	if err != nil {
		return apierror.InternalError("failed to hash password")
	}
	// The audit row goes in first and must succeed: a password reset without a
	// record is refused rather than done silently. (A failed hash write after
	// it leaves a row for a reset that did not land — the safer of the two.)
	if err := s.recordMemberActivity(ctx, workspaceID, t.member.ID, "member.password_reset", map[string]interface{}{
		"user_id": targetUserID.String(),
	}); err != nil {
		return fmt.Errorf("workspace_member_service.SetMemberPassword: audit not written, password unchanged: %w", err)
	}
	if err := s.userRepo.UpdatePasswordHash(ctx, targetUserID, string(hash)); err != nil {
		return fmt.Errorf("workspace_member_service.SetMemberPassword: %w", err)
	}
	if s.refreshTokens != nil {
		if err := s.refreshTokens.RevokeByUserID(ctx, targetUserID); err != nil {
			return fmt.Errorf("workspace_member_service.SetMemberPassword: %w: %w", ErrSessionsNotRevoked, err)
		}
	}
	return nil
}

// SetMemberUsername changes a member's @-handle. The handle is global and must
// not shadow an agent slug in any workspace the member belongs to — the same
// rule the profile flow enforces (auth.Service.UpdateProfile).
func (s *workspaceMemberService) SetMemberUsername(ctx context.Context, workspaceID, targetUserID uuid.UUID, username string) error {
	username = strings.TrimSpace(username)
	if !auth.ValidUsername(username) {
		return auth.ErrInvalidUsername
	}

	t, err := s.authorizeAccountEdit(ctx, workspaceID, targetUserID)
	if err != nil {
		return err
	}
	previous := t.user.Username
	if previous == username {
		return nil
	}

	existing, err := s.userRepo.GetByUsernameGlobal(ctx, username)
	if err != nil {
		return fmt.Errorf("workspace_member_service.SetMemberUsername: %w", err)
	}
	if existing != nil && existing.ID != targetUserID {
		return auth.ErrUsernameConflict
	}
	if s.agentRepo != nil {
		for _, ws := range t.workspaces {
			agent, err := s.agentRepo.GetBySlug(ctx, ws.ID, username)
			if err != nil {
				return fmt.Errorf("workspace_member_service.SetMemberUsername: %w", err)
			}
			if agent != nil {
				return apierror.Conflict(fmt.Sprintf(
					"this handle is already used by agent %q in workspace %q — choose a different username",
					agent.Slug, ws.Name))
			}
		}
	}

	t.user.Username = username
	if err := s.userRepo.Update(ctx, t.user); err != nil {
		return fmt.Errorf("workspace_member_service.SetMemberUsername: %w", err)
	}

	s.logMemberActivity(ctx, workspaceID, t.member.ID, "member.username_changed", map[string]interface{}{
		"user_id":      targetUserID.String(),
		"old_username": previous,
		"new_username": username,
	})
	return nil
}
