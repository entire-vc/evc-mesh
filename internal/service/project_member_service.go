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
	agentRepo           repository.AgentRepository
}

// NewProjectMemberService returns a new ProjectMemberService.
func NewProjectMemberService(
	memberRepo repository.ProjectMemberRepository,
	workspaceMemberRepo repository.WorkspaceMemberRepository,
	projectRepo repository.ProjectRepository,
	opts ...ProjectMemberServiceOption,
) ProjectMemberService {
	s := &projectMemberService{
		memberRepo:          memberRepo,
		workspaceMemberRepo: workspaceMemberRepo,
		projectRepo:         projectRepo,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ProjectMemberServiceOption is a functional option for projectMemberService.
type ProjectMemberServiceOption func(*projectMemberService)

// WithAgentRepo injects the agent repository for agent membership validation.
func WithAgentRepo(repo repository.AgentRepository) ProjectMemberServiceOption {
	return func(s *projectMemberService) { s.agentRepo = repo }
}

var validProjectRoles = map[string]bool{
	domain.ProjectRoleAdmin:  true,
	domain.ProjectRoleMember: true,
	domain.ProjectRoleViewer: true,
}

// ListMembers returns all members of a project with user/agent details.
func (s *projectMemberService) ListMembers(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectMemberWithUser, error) {
	return s.memberRepo.List(ctx, projectID)
}

// AddMember adds a user to a project. The user must already be a workspace member.
func (s *projectMemberService) AddMember(ctx context.Context, projectID, userID uuid.UUID, role string) (*domain.ProjectMemberWithUser, error) {
	if role == "" {
		role = domain.ProjectRoleMember
	}
	if !validProjectRoles[role] {
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
		UserID:    &userID,
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
		if members[i].UserID != nil && *members[i].UserID == userID {
			return &members[i], nil
		}
	}
	return nil, fmt.Errorf("project_member_service.AddMember: could not retrieve created member")
}

// AddAgentMember adds an agent to a project. The agent must belong to the same workspace.
func (s *projectMemberService) AddAgentMember(ctx context.Context, projectID, agentID uuid.UUID, role string) (*domain.ProjectMemberWithUser, error) {
	if role == "" {
		role = domain.ProjectRoleMember
	}
	if !validProjectRoles[role] {
		return nil, apierror.ValidationError(map[string]string{
			"role": "role must be one of: admin, member, viewer",
		})
	}

	// Verify the project exists.
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("project_member_service.AddAgentMember: %w", err)
	}
	if project == nil {
		return nil, apierror.NotFound("Project")
	}

	// Verify agent exists and belongs to the same workspace.
	if s.agentRepo != nil {
		agent, err := s.agentRepo.GetByID(ctx, agentID)
		if err != nil {
			return nil, fmt.Errorf("project_member_service.AddAgentMember: %w", err)
		}
		if agent == nil {
			return nil, apierror.NotFound("Agent")
		}
		if agent.WorkspaceID != project.WorkspaceID {
			return nil, apierror.BadRequest("agent does not belong to this workspace")
		}
	}

	// Check for duplicate.
	existing, err := s.memberRepo.GetByProjectAndAgent(ctx, projectID, agentID)
	if err != nil {
		return nil, fmt.Errorf("project_member_service.AddAgentMember: %w", err)
	}
	if existing != nil {
		return nil, apierror.Conflict("agent is already a member of this project")
	}

	now := time.Now()
	member := &domain.ProjectMember{
		ID:        uuid.New(),
		ProjectID: projectID,
		AgentID:   &agentID,
		Role:      role,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.memberRepo.Create(ctx, member); err != nil {
		return nil, fmt.Errorf("project_member_service.AddAgentMember: %w", err)
	}

	// Fetch the full member record.
	members, err := s.memberRepo.List(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("project_member_service.AddAgentMember: %w", err)
	}
	for i := range members {
		if members[i].AgentID != nil && *members[i].AgentID == agentID {
			return &members[i], nil
		}
	}
	return nil, fmt.Errorf("project_member_service.AddAgentMember: could not retrieve created member")
}

// UpdateMemberRole changes a member's project-level role.
func (s *projectMemberService) UpdateMemberRole(ctx context.Context, projectID, userID uuid.UUID, newRole string) error {
	if !validProjectRoles[newRole] {
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

// RemoveAgentMember removes an agent's project membership.
func (s *projectMemberService) RemoveAgentMember(ctx context.Context, projectID, agentID uuid.UUID) error {
	existing, err := s.memberRepo.GetByProjectAndAgent(ctx, projectID, agentID)
	if err != nil {
		return fmt.Errorf("project_member_service.RemoveAgentMember: %w", err)
	}
	if existing == nil {
		return apierror.NotFound("ProjectMember")
	}

	if err := s.memberRepo.DeleteAgent(ctx, projectID, agentID); err != nil {
		return fmt.Errorf("project_member_service.RemoveAgentMember: %w", err)
	}
	return nil
}
