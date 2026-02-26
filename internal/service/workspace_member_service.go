package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

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
}

// NewWorkspaceMemberService returns a new WorkspaceMemberService.
func NewWorkspaceMemberService(
	memberRepo repository.WorkspaceMemberRepository,
	userRepo repository.UserRepository,
	projectMemberRepo repository.ProjectMemberRepository,
	activityRepo repository.ActivityLogRepository,
) WorkspaceMemberService {
	return &workspaceMemberService{
		memberRepo:        memberRepo,
		userRepo:          userRepo,
		projectMemberRepo: projectMemberRepo,
		activityRepo:      activityRepo,
	}
}

// ListMembers returns all members of a workspace with user details.
func (s *workspaceMemberService) ListMembers(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
	return s.memberRepo.List(ctx, workspaceID)
}

// AddMember looks up a user by email, validates there's no existing membership,
// creates the membership record, and returns the full member-with-user view.
func (s *workspaceMemberService) AddMember(ctx context.Context, workspaceID uuid.UUID, email, role string, invitedBy uuid.UUID) (*domain.WorkspaceMemberWithUser, error) {
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
		return nil, apierror.NotFound("User")
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
		User: domain.UserBrief{
			ID:        user.ID,
			Email:     user.Email,
			Name:      user.Name,
			AvatarURL: user.AvatarURL,
		},
	}
	return result, nil
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

// SearchUsers searches for users by email or name and annotates each with membership status.
func (s *workspaceMemberService) SearchUsers(ctx context.Context, workspaceID uuid.UUID, query string) ([]domain.UserWithMemberStatus, error) {
	if query == "" {
		return []domain.UserWithMemberStatus{}, nil
	}

	users, err := s.userRepo.SearchUsers(ctx, query, 20)
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
			UserBrief: domain.UserBrief{
				ID:        u.ID,
				Email:     u.Email,
				Name:      u.Name,
				AvatarURL: u.AvatarURL,
			},
			IsMember: m != nil,
		}
	}
	return result, nil
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
