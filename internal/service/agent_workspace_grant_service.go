package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// grantValidRoles mirrors workspaceMemberService's validRoles — duplicated
// rather than shared because that's the existing convention in this file
// (workspace_member_service.go itself has two independent copies for
// AddMember and UpdateMemberRole).
var grantValidRoles = map[string]bool{
	domain.RoleOwner:  true,
	domain.RoleAdmin:  true,
	domain.RoleMember: true,
	domain.RoleViewer: true,
}

type agentWorkspaceGrantService struct {
	grantRepo     repository.AgentWorkspaceGrantRepository
	agentRepo     repository.AgentRepository
	workspaceRepo repository.WorkspaceRepository
	activityRepo  repository.ActivityLogRepository
}

// NewAgentWorkspaceGrantService returns a new AgentWorkspaceGrantService.
// activityRepo may be nil — logging becomes a no-op, same convention as
// workspaceMemberService.logMemberActivity.
func NewAgentWorkspaceGrantService(
	grantRepo repository.AgentWorkspaceGrantRepository,
	agentRepo repository.AgentRepository,
	workspaceRepo repository.WorkspaceRepository,
	activityRepo repository.ActivityLogRepository,
) AgentWorkspaceGrantService {
	return &agentWorkspaceGrantService{
		grantRepo:     grantRepo,
		agentRepo:     agentRepo,
		workspaceRepo: workspaceRepo,
		activityRepo:  activityRepo,
	}
}

// InviteAgent connects agentID to workspaceID — see the interface doc for the
// three-way branch (insert / conflict / reactivate) this implements.
func (s *agentWorkspaceGrantService) InviteAgent(ctx context.Context, workspaceID, agentID uuid.UUID, role string, invitedBy uuid.UUID) (*InviteAgentResult, error) {
	if role == "" {
		role = domain.RoleMember
	}
	if !grantValidRoles[role] {
		return nil, apierror.ValidationError(map[string]string{
			"role": "role must be one of: owner, admin, member, viewer",
		})
	}

	agent, err := s.agentRepo.GetByID(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("agent_workspace_grant_service.InviteAgent: %w", err)
	}
	if agent == nil {
		return nil, apierror.NotFound("Agent")
	}

	ws, err := s.workspaceRepo.GetByID(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("agent_workspace_grant_service.InviteAgent: %w", err)
	}
	if ws == nil {
		return nil, apierror.NotFound("Workspace")
	}

	existing, err := s.grantRepo.GetByAgentAndWorkspace(ctx, agentID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("agent_workspace_grant_service.InviteAgent: %w", err)
	}
	if existing != nil && !existing.IsRevoked() {
		// Checked BEFORE generating a key: no point paying bcrypt's ~163ms
		// cost (see agentService's cache-wrapper comment) for a request that
		// is about to be refused.
		return nil, apierror.Conflict("agent already has an active connection to this workspace — revoke it first to reissue a key")
	}

	rawKey, err := generateAPIKey(ws.Slug)
	if err != nil {
		return nil, fmt.Errorf("agent_workspace_grant_service.InviteAgent: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword(bcryptInput(rawKey), bcryptCost)
	if err != nil {
		return nil, fmt.Errorf("agent_workspace_grant_service.InviteAgent: %w", err)
	}
	prefix := extractPrefix(rawKey, ws.Slug)

	var invitedByPtr *uuid.UUID
	if invitedBy != uuid.Nil {
		invitedByPtr = &invitedBy
	}

	if existing != nil {
		// Revoked — reactivate the same row (AC6: exactly one way to not
		// multiply rows; the unique index would reject a second INSERT anyway).
		if err := s.grantRepo.Reactivate(ctx, existing.ID, role, prefix, string(hash), invitedByPtr); err != nil {
			return nil, fmt.Errorf("agent_workspace_grant_service.InviteAgent: %w", err)
		}
		grant := &domain.AgentWorkspaceGrant{
			ID:           existing.ID,
			AgentID:      agentID,
			WorkspaceID:  workspaceID,
			Role:         role,
			APIKeyPrefix: prefix,
			APIKeyHash:   string(hash),
			InvitedBy:    invitedByPtr,
			CreatedAt:    existing.CreatedAt,
		}
		s.logGrantActivity(ctx, workspaceID, grant.ID, "agent_grant.reactivated", map[string]interface{}{
			"agent_id": agentID.String(),
			"role":     role,
		})
		return &InviteAgentResult{Grant: grant, APIKey: rawKey, Reactivated: true}, nil
	}

	grant := &domain.AgentWorkspaceGrant{
		ID:           uuid.New(),
		AgentID:      agentID,
		WorkspaceID:  workspaceID,
		Role:         role,
		APIKeyPrefix: prefix,
		APIKeyHash:   string(hash),
		InvitedBy:    invitedByPtr,
		CreatedAt:    time.Now(),
	}
	if err := s.grantRepo.Create(ctx, grant); err != nil {
		return nil, fmt.Errorf("agent_workspace_grant_service.InviteAgent: %w", err)
	}

	s.logGrantActivity(ctx, workspaceID, grant.ID, "agent_grant.created", map[string]interface{}{
		"agent_id": agentID.String(),
		"role":     role,
	})

	return &InviteAgentResult{Grant: grant, APIKey: rawKey, Reactivated: false}, nil
}

// RevokeGrant sets revoked_at on grantID, scoped to workspaceID.
//
// Deliberately does NOT cascade to tasks already assigned to the revoked
// agent in this workspace (#71627c5a AC4) — a task's assignee_id/
// assignee_type are left exactly as they were. Two things change instead:
// the agent can no longer authenticate a key scoped to this workspace
// (#7661fc5d), so it cannot see or act on that task through this workspace
// any more; and assertAssigneeInProjectWorkspace refuses any NEW attempt to
// (re-)assign that agent here, since GetByAgentAndWorkspace returns the
// revoked row rather than nil and the caller checks IsRevoked() explicitly.
// The existing assignment is not silently cleared or reassigned — that is a
// human/operator decision (reassign, or re-invite the agent to restore
// access), not something a revoke call should do as a side effect.
func (s *agentWorkspaceGrantService) RevokeGrant(ctx context.Context, workspaceID, grantID uuid.UUID) error {
	found, err := s.grantRepo.Revoke(ctx, grantID, workspaceID)
	if err != nil {
		return fmt.Errorf("agent_workspace_grant_service.RevokeGrant: %w", err)
	}
	if !found {
		return apierror.NotFound("AgentWorkspaceGrant")
	}

	s.logGrantActivity(ctx, workspaceID, grantID, "agent_grant.revoked", map[string]interface{}{})
	return nil
}

// ListWorkspaceAgents returns every agent with an active connection to workspaceID.
func (s *agentWorkspaceGrantService) ListWorkspaceAgents(ctx context.Context, workspaceID uuid.UUID) ([]domain.AgentWorkspaceGrantWithAgent, error) {
	return s.grantRepo.ListActiveByWorkspace(ctx, workspaceID)
}

// ListAgentWorkspaces returns every workspace agentID holds an active connection to.
func (s *agentWorkspaceGrantService) ListAgentWorkspaces(ctx context.Context, agentID uuid.UUID) ([]domain.AgentWorkspaceGrantWithWorkspace, error) {
	return s.grantRepo.ListActiveByAgent(ctx, agentID)
}

// logGrantActivity mirrors workspaceMemberService.logMemberActivity exactly
// (same no-op-on-nil-repo convention), entity type "agent_workspace_grant".
func (s *agentWorkspaceGrantService) logGrantActivity(ctx context.Context, workspaceID, entityID uuid.UUID, action string, changes map[string]interface{}) {
	if s.activityRepo == nil {
		return
	}
	actorID, actorType := actorctx.FromContext(ctx)
	changesJSON, _ := json.Marshal(changes)
	entry := &domain.ActivityLog{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		EntityType:  "agent_workspace_grant",
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
