package service

import (
	"context"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

var secretNameRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

type secretService struct {
	repo repository.SecretRepository
}

// NewSecretService returns a new SecretService.
func NewSecretService(repo repository.SecretRepository) SecretService {
	return &secretService{repo: repo}
}

// validateCreateInput checks everything the DB's own CHECK constraints will
// also enforce, so a bad request fails with a field-level 400 instead of a
// generic constraint-violation 500. The DB constraints stay authoritative —
// this exists for a better error message, not as the actual guarantee.
func validateCreateInput(input domain.CreateSecretInput) error {
	fields := map[string]string{}
	if !secretNameRE.MatchString(input.Name) {
		fields["name"] = "must match ^[A-Z][A-Z0-9_]*$ (it becomes an env var name)"
	}
	if input.Value == "" {
		fields["value"] = "must not be empty"
	}
	switch input.Scope {
	case domain.SecretScopeWorkspace:
		if input.ProjectID != nil || input.AgentID != nil {
			fields["scope"] = "workspace scope takes no project_id or agent_id"
		}
	case domain.SecretScopeProject:
		if input.ProjectID == nil {
			fields["project_id"] = "required for project scope"
		}
		if input.AgentID != nil {
			fields["agent_id"] = "must not be set for project scope"
		}
	case domain.SecretScopeAgent:
		if input.AgentID == nil {
			fields["agent_id"] = "required for agent scope"
		}
		if input.ProjectID != nil {
			fields["project_id"] = "must not be set for agent scope"
		}
	default:
		fields["scope"] = "must be workspace, project, or agent"
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now()) {
		fields["expires_at"] = "must be in the future"
	}
	if len(fields) > 0 {
		return apierror.ValidationError(fields)
	}
	return nil
}

func (s *secretService) Create(ctx context.Context, input domain.CreateSecretInput) (domain.Secret, error) {
	if err := validateCreateInput(input); err != nil {
		return domain.Secret{}, err
	}
	// project_id and agent_id arrive in the request body, where no route
	// parameter names them and the workspace guard therefore never sees them.
	// The table's foreign keys accept another tenant's ids, so this check is
	// the only thing standing between a create and a row that references a
	// project the caller cannot see.
	if err := s.repo.AssertScopeRefInWorkspace(ctx, input.WorkspaceID, input.ProjectID, input.AgentID); err != nil {
		return domain.Secret{}, err
	}
	return s.repo.Create(ctx, input)
}

func (s *secretService) Rotate(ctx context.Context, workspaceID uuid.UUID, scope domain.SecretScope, projectID, agentID *uuid.UUID, name string, input domain.CreateSecretInput) (domain.Secret, error) {
	// Reuse the same field-level validation as Create — the input carries a
	// fresh value and must satisfy the same rules a new secret would,
	// scoped to the identity Rotate is being called for rather than
	// whatever (possibly stale) scope fields the caller put on input.
	input.WorkspaceID = workspaceID
	input.Scope = scope
	input.ProjectID = projectID
	input.AgentID = agentID
	input.Name = name
	if err := validateCreateInput(input); err != nil {
		return domain.Secret{}, err
	}
	// Rotate's scope refs normally come from a row this workspace already
	// owns, so this is belt-and-braces there — but the method is callable
	// with any refs, and a containment check that only runs on the path
	// someone remembered to route through it is not a containment check.
	if err := s.repo.AssertScopeRefInWorkspace(ctx, workspaceID, projectID, agentID); err != nil {
		return domain.Secret{}, err
	}
	return s.repo.Rotate(ctx, workspaceID, scope, projectID, agentID, name, input)
}

func (s *secretService) Delete(ctx context.Context, workspaceID uuid.UUID, scope domain.SecretScope, projectID, agentID *uuid.UUID, name string, deletedBy uuid.UUID, deletedByType domain.ActorType) error {
	return s.repo.Delete(ctx, workspaceID, scope, projectID, agentID, name, deletedBy, deletedByType)
}

func (s *secretService) List(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.Secret, error) {
	return s.repo.ListCurrent(ctx, workspaceID, projectID, agentID)
}

func (s *secretService) GetByID(ctx context.Context, workspaceID, id uuid.UUID) (domain.Secret, error) {
	return s.repo.GetByID(ctx, workspaceID, id)
}

var _ SecretService = (*secretService)(nil)

type secretMaterializationService struct {
	materializer repository.SecretMaterializer
}

// NewSecretMaterializationService returns a new SecretMaterializationService.
// Wire this ONLY into the spawn-hook handler (S4) — see the interface
// doc comment for why it is kept separate from SecretService.
func NewSecretMaterializationService(materializer repository.SecretMaterializer) SecretMaterializationService {
	return &secretMaterializationService{materializer: materializer}
}

func (s *secretMaterializationService) ResolveForSpawn(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.MaterializedSecret, error) {
	return s.materializer.ResolveCurrentValues(ctx, workspaceID, projectID, agentID)
}

var _ SecretMaterializationService = (*secretMaterializationService)(nil)
