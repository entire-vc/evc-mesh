package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type workspaceMemberService struct {
	memberRepo        repository.WorkspaceMemberRepository
	userRepo          repository.UserRepository
	projectMemberRepo repository.ProjectMemberRepository
	activityRepo      repository.ActivityLogRepository
	// agentRepo backs generateUniqueUsername's agent-slug skip (task fee35355)
	// — optional, nil in tests that don't exercise it, in which case candidates
	// are only checked against other usernames, same as before.
	agentRepo repository.AgentRepository
}

// NewWorkspaceMemberService returns a new WorkspaceMemberService.
func NewWorkspaceMemberService(
	memberRepo repository.WorkspaceMemberRepository,
	userRepo repository.UserRepository,
	projectMemberRepo repository.ProjectMemberRepository,
	activityRepo repository.ActivityLogRepository,
	agentRepo repository.AgentRepository,
) WorkspaceMemberService {
	return &workspaceMemberService{
		memberRepo:        memberRepo,
		userRepo:          userRepo,
		projectMemberRepo: projectMemberRepo,
		activityRepo:      activityRepo,
		agentRepo:         agentRepo,
	}
}

// ListMembers returns all members of a workspace with user details.
func (s *workspaceMemberService) ListMembers(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
	return s.memberRepo.List(ctx, workspaceID)
}

// GetMember returns one membership with its user details, or (nil, nil) when
// the user is not a member of this workspace.
func (s *workspaceMemberService) GetMember(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMemberWithUser, error) {
	member, err := s.memberRepo.GetByWorkspaceAndUser(ctx, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("workspace_member_service.GetMember: %w", err)
	}
	if member == nil {
		return nil, nil
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("workspace_member_service.GetMember: %w", err)
	}
	if user == nil {
		return nil, apierror.NotFound("User")
	}
	return &domain.WorkspaceMemberWithUser{WorkspaceMember: *member, User: briefOf(user)}, nil
}

// AddMember looks up a user by email, validates there's no existing membership,
// creates the membership record, and returns the full member-with-user view.
func (s *workspaceMemberService) AddMember(ctx context.Context, workspaceID uuid.UUID, email, role string, invitedBy uuid.UUID) (*domain.WorkspaceMemberWithUser, error) {
	email = auth.NormalizeEmail(email)
	if email == "" {
		return nil, apierror.ValidationError(map[string]string{
			"email": "email is required",
		})
	}

	validRoles := map[string]bool{
		domain.RoleOwner:  true,
		domain.RoleAdmin:  true,
		domain.RoleMember: true,
		domain.RoleViewer: true,
	}
	if role == "" {
		role = domain.RoleMember
	}
	if !validRoles[role] {
		return nil, apierror.ValidationError(map[string]string{
			"role": "role must be one of: owner, admin, member, viewer",
		})
	}

	user, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("workspace_member_service.AddMember: %w", err)
	}
	if user == nil {
		// The bare apierror.NotFound("User") this used to return rendered as
		// "User not found", which reads as a failure rather than as the fork it
		// actually is: this address has no account yet, so it needs an invite or
		// a provisioned password. Naming the two exits is the difference between
		// a dead end and a next step.
		return nil, apierror.NotFoundWithDetails("User",
			"no account on this instance uses this address — send an invite link instead, or provide a password to create the account now")
	}

	existing, err := s.memberRepo.GetByWorkspaceAndUser(ctx, workspaceID, user.ID)
	if err != nil {
		return nil, fmt.Errorf("workspace_member_service.AddMember: %w", err)
	}
	if existing != nil {
		return nil, apierror.Conflict("user is already a member of this workspace")
	}

	now := time.Now()
	var invitedByPtr *uuid.UUID
	if invitedBy != uuid.Nil {
		invitedByPtr = &invitedBy
	}
	member := &domain.WorkspaceMember{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      user.ID,
		Role:        role,
		InvitedBy:   invitedByPtr,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.memberRepo.Create(ctx, member); err != nil {
		return nil, fmt.Errorf("workspace_member_service.AddMember: %w", err)
	}

	s.logMemberActivity(ctx, workspaceID, member.ID, "member.added", map[string]interface{}{
		"user_id": user.ID.String(),
		"email":   user.Email,
		"role":    role,
	})

	result := &domain.WorkspaceMemberWithUser{
		WorkspaceMember: *member,
		User:            briefOf(user),
	}
	return result, nil
}

// AddMemberWithCreate is like AddMember but will create the user when a password is provided
// and the email does not yet exist in the system.
//
// name is the display name for an account created here. It is optional, and
// falling back to the address is a last resort rather than the normal path:
// that fallback is why every provisioned account on an existing instance shows
// up as its own address everywhere a name should be, with nothing in the product
// able to correct it afterwards.
func (s *workspaceMemberService) AddMemberWithCreate(ctx context.Context, workspaceID uuid.UUID, email, name, role, password string, invitedBy uuid.UUID) (*domain.WorkspaceMemberWithUser, error) {
	if password == "" {
		return s.AddMember(ctx, workspaceID, email, role, invitedBy)
	}

	email = auth.NormalizeEmail(email)
	if email == "" {
		return nil, apierror.ValidationError(map[string]string{"email": "email is required"})
	}
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 100 {
		return nil, apierror.ValidationError(map[string]string{"name": "name must be at most 100 characters"})
	}
	if !isValidRole(role) {
		role = domain.RoleMember
	}

	user, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("workspace_member_service.AddMemberWithCreate: %w", err)
	}

	if user == nil {
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(password), 10)
		if hashErr != nil {
			return nil, apierror.InternalError("failed to hash password")
		}
		username, unameErr := s.generateUniqueUsername(ctx, workspaceID, email)
		if unameErr != nil {
			return nil, fmt.Errorf("workspace_member_service.AddMemberWithCreate: %w", unameErr)
		}
		displayName := name
		if displayName == "" {
			displayName = email
		}
		now := time.Now()
		user = &domain.User{
			ID:           uuid.New(),
			Email:        email,
			Name:         displayName,
			Username:     username,
			PasswordHash: string(hash),
			IsActive:     true,
			// Provisioned by somebody else, so the name is not this person's
			// choice yet: an admin may still correct it, and it locks the first
			// time they edit their own profile.
			DisplayNameSelfSet: false,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		if createErr := s.userRepo.Create(ctx, user); createErr != nil {
			return nil, fmt.Errorf("workspace_member_service.AddMemberWithCreate: %w", createErr)
		}
	}

	existing, err := s.memberRepo.GetByWorkspaceAndUser(ctx, workspaceID, user.ID)
	if err != nil {
		return nil, fmt.Errorf("workspace_member_service.AddMemberWithCreate: %w", err)
	}
	if existing != nil {
		return nil, apierror.Conflict("user is already a member of this workspace")
	}

	now := time.Now()
	var invitedByPtr *uuid.UUID
	if invitedBy != uuid.Nil {
		invitedByPtr = &invitedBy
	}
	member := &domain.WorkspaceMember{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      user.ID,
		Role:        role,
		InvitedBy:   invitedByPtr,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.memberRepo.Create(ctx, member); err != nil {
		return nil, fmt.Errorf("workspace_member_service.AddMemberWithCreate: %w", err)
	}

	s.logMemberActivity(ctx, workspaceID, member.ID, "member.added", map[string]interface{}{
		"user_id": user.ID.String(),
		"email":   user.Email,
		"role":    role,
		"created": true,
	})

	return &domain.WorkspaceMemberWithUser{
		WorkspaceMember: *member,
		User:            briefOf(user),
	}, nil
}

// usernameBaseFromEmail derives a slug from the email local-part, matching the
// backfill logic in migration 20260520046: lowercase, non-slug chars to '-',
// collapse repeats, trim hyphens, clamp to 38 (leaving room for a numeric
// suffix within the 40-char limit), and pad to the 2-char minimum. The result
// satisfies chk_users_username (^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$).
func usernameBaseFromEmail(email string) string {
	local := email
	if i := strings.IndexByte(email, '@'); i >= 0 {
		local = email[:i]
	}
	local = strings.ToLower(local)

	var b strings.Builder
	for _, r := range local {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := b.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if len(s) > 38 {
		s = strings.Trim(s[:38], "-")
	}
	for len(s) < 2 {
		s += "0"
	}
	return s
}

// generateUniqueUsername returns a username derived from the email that is not
// already taken (case-insensitive, global) AND does not collide with an
// existing agent's slug in workspaceID (task fee35355 — this is the "заведение
// пользователя" entry point that used to have no such check at all, silently
// creating exactly the ambiguous-@-mention state task f4f47938 had to cope
// with after the fact). On collision it appends an incrementing numeric
// suffix, mirroring the migration backfill; an agent-slug collision is treated
// the same way as a username-taken collision — just another reason to try the
// next candidate, since this path never surfaces the name to a human anyway.
func (s *workspaceMemberService) generateUniqueUsername(ctx context.Context, workspaceID uuid.UUID, email string) (string, error) {
	base := usernameBaseFromEmail(email)
	candidate := base
	for i := 1; i <= 10000; i++ {
		exists, err := s.userRepo.UsernameExists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !exists && s.agentRepo != nil {
			agent, err := s.agentRepo.GetBySlug(ctx, workspaceID, candidate)
			if err != nil {
				return "", err
			}
			exists = agent != nil
		}
		if !exists {
			return candidate, nil
		}
		candidate = base + strconv.Itoa(i)
	}
	return "", apierror.InternalError("could not allocate a unique username")
}

// UpdateMemberRole changes a member's role, preventing removal of the last owner.
func (s *workspaceMemberService) UpdateMemberRole(ctx context.Context, workspaceID, targetUserID uuid.UUID, newRole string) error {
	validRoles := map[string]bool{
		domain.RoleAdmin:  true,
		domain.RoleMember: true,
		domain.RoleViewer: true,
	}
	if !validRoles[newRole] {
		return apierror.ValidationError(map[string]string{
			"role": "role must be one of: admin, member, viewer (owner cannot be set via this endpoint)",
		})
	}

	existing, err := s.memberRepo.GetByWorkspaceAndUser(ctx, workspaceID, targetUserID)
	if err != nil {
		return fmt.Errorf("workspace_member_service.UpdateMemberRole: %w", err)
	}
	if existing == nil {
		return apierror.NotFound("WorkspaceMember")
	}

	// Prevent removing the last owner.
	if existing.Role == domain.RoleOwner {
		count, err := s.memberRepo.CountOwners(ctx, workspaceID)
		if err != nil {
			return fmt.Errorf("workspace_member_service.UpdateMemberRole: %w", err)
		}
		if count <= 1 {
			return apierror.BadRequest("cannot change the role of the last owner")
		}
	}

	if err := s.memberRepo.UpdateRole(ctx, workspaceID, targetUserID, newRole); err != nil {
		return fmt.Errorf("workspace_member_service.UpdateMemberRole: %w", err)
	}

	s.logMemberActivity(ctx, workspaceID, existing.ID, "member.role_changed", map[string]interface{}{
		"user_id":  targetUserID.String(),
		"old_role": existing.Role,
		"new_role": newRole,
	})
	return nil
}

// RemoveMember removes a user from the workspace and all its projects.
// The last owner cannot be removed.
func (s *workspaceMemberService) RemoveMember(ctx context.Context, workspaceID, targetUserID uuid.UUID) error {
	existing, err := s.memberRepo.GetByWorkspaceAndUser(ctx, workspaceID, targetUserID)
	if err != nil {
		return fmt.Errorf("workspace_member_service.RemoveMember: %w", err)
	}
	if existing == nil {
		return apierror.NotFound("WorkspaceMember")
	}

	// Prevent removing the last owner.
	if existing.Role == domain.RoleOwner {
		count, err := s.memberRepo.CountOwners(ctx, workspaceID)
		if err != nil {
			return fmt.Errorf("workspace_member_service.RemoveMember: %w", err)
		}
		if count <= 1 {
			return apierror.BadRequest("cannot remove the last owner from the workspace")
		}
	}

	// Remove from all projects within the workspace.
	if err := s.projectMemberRepo.DeleteByWorkspaceAndUser(ctx, workspaceID, targetUserID); err != nil {
		return fmt.Errorf("workspace_member_service.RemoveMember: %w", err)
	}

	// Remove from workspace.
	if err := s.memberRepo.Delete(ctx, workspaceID, targetUserID); err != nil {
		return fmt.Errorf("workspace_member_service.RemoveMember: %w", err)
	}

	s.logMemberActivity(ctx, workspaceID, existing.ID, "member.removed", map[string]interface{}{
		"user_id": targetUserID.String(),
	})
	return nil
}

// GetMyRole returns the role of the given user in the workspace.
func (s *workspaceMemberService) GetMyRole(ctx context.Context, workspaceID, userID uuid.UUID) (string, error) {
	role, err := s.memberRepo.GetRole(ctx, workspaceID, userID)
	if err != nil {
		return "", apierror.NotFound("WorkspaceMember")
	}
	return role, nil
}

// SearchUsers finds existing accounts the caller may add to workspaceID and
// annotates each with whether it is already a member.
//
// callerID is the acting user, and it is what bounds the result: see
// UserRepository.SearchAddableUsers. It is not the same as "a member of
// workspaceID" — the whole point of this endpoint is to surface people who are
// not in this workspace yet, so the boundary has to come from the caller's own
// memberships rather than from the workspace being added to.
func (s *workspaceMemberService) SearchUsers(ctx context.Context, workspaceID, callerID uuid.UUID, query string) ([]domain.UserWithMemberStatus, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []domain.UserWithMemberStatus{}, nil
	}

	users, err := s.userRepo.SearchAddableUsers(ctx, callerID, query, 20)
	if err != nil {
		return nil, fmt.Errorf("workspace_member_service.SearchUsers: %w", err)
	}

	result := make([]domain.UserWithMemberStatus, len(users))
	for i, u := range users {
		m, err := s.memberRepo.GetByWorkspaceAndUser(ctx, workspaceID, u.ID)
		if err != nil {
			return nil, fmt.Errorf("workspace_member_service.SearchUsers: %w", err)
		}
		result[i] = domain.UserWithMemberStatus{
			UserBrief: briefOf(&u),
			IsMember:  m != nil,
		}
	}
	return result, nil
}

// SetMemberDisplayName fills in the display name of a member of workspaceID.
//
// The name lives on the account, not on the membership, so this is a write that
// every other workspace the person belongs to will see. That is only acceptable
// while the name is unowned — provisioned by an operator, or left as the address
// by a path that never asked. Once the person has set their own name
// (display_name_self_set), an admin of one workspace does not get to rewrite how
// they appear in another, and this refuses with 403.
//
// Cross-tenant containment for the target itself is already structural: the
// route is /workspaces/:ws_id/members/:user_id and :user_id is a scoped pair
// param, so the middleware has proven the target is a member of this workspace
// before the handler runs. The membership lookup below is the service-level
// restatement of that, not a substitute for it.
func (s *workspaceMemberService) SetMemberDisplayName(ctx context.Context, workspaceID, targetUserID uuid.UUID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return apierror.ValidationError(map[string]string{"name": "name is required"})
	}
	if len([]rune(name)) > 100 {
		return apierror.ValidationError(map[string]string{"name": "name must be at most 100 characters"})
	}

	member, err := s.memberRepo.GetByWorkspaceAndUser(ctx, workspaceID, targetUserID)
	if err != nil {
		return fmt.Errorf("workspace_member_service.SetMemberDisplayName: %w", err)
	}
	if member == nil {
		return apierror.NotFound("WorkspaceMember")
	}

	user, err := s.userRepo.GetByID(ctx, targetUserID)
	if err != nil {
		return fmt.Errorf("workspace_member_service.SetMemberDisplayName: %w", err)
	}
	if user == nil {
		return apierror.NotFound("User")
	}
	if user.DisplayNameSelfSet {
		return apierror.Forbidden("this member set their own display name — only they can change it")
	}

	previous := user.Name
	user.Name = name
	if err := s.userRepo.Update(ctx, user); err != nil {
		return fmt.Errorf("workspace_member_service.SetMemberDisplayName: %w", err)
	}

	s.logMemberActivity(ctx, workspaceID, member.ID, "member.renamed", map[string]interface{}{
		"user_id":  targetUserID.String(),
		"old_name": previous,
		"new_name": name,
	})
	return nil
}

// briefOf projects a user onto the shape embedded in member and search
// responses. username rides along so the UI has a non-address way to tell two
// people with the same display name apart.
func briefOf(u *domain.User) domain.UserBrief {
	return domain.UserBrief{
		ID:        u.ID,
		Email:     u.Email,
		Name:      u.Name,
		Username:  u.Username,
		AvatarURL: u.AvatarURL,
	}
}

// logMemberActivity writes an activity log entry. Failures are logged but not propagated.
func (s *workspaceMemberService) logMemberActivity(ctx context.Context, workspaceID, entityID uuid.UUID, action string, changes map[string]interface{}) {
	if s.activityRepo == nil {
		return
	}
	actorID, actorType := actorctx.FromContext(ctx)
	changesJSON, _ := json.Marshal(changes)
	entry := &domain.ActivityLog{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		EntityType:  "workspace_member",
		EntityID:    entityID,
		Action:      action,
		ActorID:     actorID,
		ActorType:   actorType,
		Changes:     changesJSON,
		CreatedAt:   time.Now(),
	}
	if err := s.activityRepo.Create(ctx, entry); err != nil {
		log.Printf("[activity] WARNING: failed to log %s: %v", action, err)
	}
}
