package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type projectMemberService struct {
	memberRepo          repository.ProjectMemberRepository
	workspaceMemberRepo repository.WorkspaceMemberRepository
	projectRepo         repository.ProjectRepository
}

// NewProjectMemberService returns a new ProjectMemberService.
func NewProjectMemberService(
	memberRepo repository.ProjectMemberRepository,
	workspaceMemberRepo repository.WorkspaceMemberRepository,
	projectRepo repository.ProjectRepository,
) ProjectMemberService {
	return &projectMemberService{
		memberRepo:          memberRepo,
		workspaceMemberRepo: workspaceMemberRepo,
		projectRepo:         projectRepo,
	}
}

// ListMembers returns all members of a project with user details.
func (s *projectMemberService) ListMembers(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectMemberWithUser, error) {
	return s.memberRepo.List(ctx, projectID)
}

// AddMember adds a user to a project. The user must already be a workspace member.
func (s *projectMemberService) AddMember(ctx context.Context, projectID, userID uuid.UUID, role string) (*domain.ProjectMemberWithUser, error) {
	validRoles := map[string]bool{
		domain.ProjectRoleAdmin:  true,
		domain.ProjectRoleMember: true,
		domain.ProjectRoleViewer: true,
	}
	if role == "" {
		role = domain.ProjectRoleMember
	}
	if !validRoles[role] {
		return nil, apierror.ValidationError(map[string]string{
			"role": "role must be one of: admin, member, viewer",
		})
	}

	// Verify the project exists and get the workspace_id for membership check.
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("project_member_service.AddMember: %w", err)
	}
	if project == nil {
		return nil, apierror.NotFound("Project")
	}

	// User must already be a workspace member.
	wsMember, err := s.workspaceMemberRepo.GetByWorkspaceAndUser(ctx, project.WorkspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("project_member_service.AddMember: %w", err)
	}
	if wsMember == nil {
		return nil, apierror.BadRequest("user is not a member of the workspace")
	}

	// Check for duplicate project membership.
	existing, err := s.memberRepo.GetByProjectAndUser(ctx, projectID, userID)
	if err != nil {
		return nil, fmt.Errorf("project_member_service.AddMember: %w", err)
	}
	if existing != nil {
		return nil, apierror.Conflict("user is already a member of this project")
	}

	now := time.Now()
	member := &domain.ProjectMember{
		ID:        uuid.New(),
		ProjectID: projectID,
		UserID:    userID,
		Role:      role,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.memberRepo.Create(ctx, member); err != nil {
		return nil, fmt.Errorf("project_member_service.AddMember: %w", err)
	}

	// Fetch the full member-with-user record.
	members, err := s.memberRepo.List(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("project_member_service.AddMember: %w", err)
	}
	for i := range members {
		if members[i].UserID == userID {
			return &members[i], nil
		}
	}
	return nil, fmt.Errorf("project_member_service.AddMember: could not retrieve created member")
}

// UpdateMemberRole changes a member's project-level role.
func (s *projectMemberService) UpdateMemberRole(ctx context.Context, projectID, userID uuid.UUID, newRole string) error {
	validRoles := map[string]bool{
		domain.ProjectRoleAdmin:  true,
		domain.ProjectRoleMember: true,
		domain.ProjectRoleViewer: true,
	}
	if !validRoles[newRole] {
		return apierror.ValidationError(map[string]string{
			"role": "role must be one of: admin, member, viewer",
		})
	}

	existing, err := s.memberRepo.GetByProjectAndUser(ctx, projectID, userID)
	if err != nil {
		return fmt.Errorf("project_member_service.UpdateMemberRole: %w", err)
	}
	if existing == nil {
		return apierror.NotFound("ProjectMember")
	}

	if err := s.memberRepo.UpdateRole(ctx, projectID, userID, newRole); err != nil {
		return fmt.Errorf("project_member_service.UpdateMemberRole: %w", err)
	}
	return nil
}

// RemoveMember removes a user's project membership.
func (s *projectMemberService) RemoveMember(ctx context.Context, projectID, userID uuid.UUID) error {
	existing, err := s.memberRepo.GetByProjectAndUser(ctx, projectID, userID)
	if err != nil {
		return fmt.Errorf("project_member_service.RemoveMember: %w", err)
	}
	if existing == nil {
		return apierror.NotFound("ProjectMember")
	}

	if err := s.memberRepo.Delete(ctx, projectID, userID); err != nil {
		return fmt.Errorf("project_member_service.RemoveMember: %w", err)
	}
	return nil
}
