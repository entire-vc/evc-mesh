package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ProjectReaderRole is agents.role of a project reader; informational only —
// the guard keys on agents.reader_project_id, never on this text.
const ProjectReaderRole = "project-reader"

// IssueProjectReaderInput is POST /workspaces/:ws_id/project-readers.
type IssueProjectReaderInput struct {
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	Name        string
	IssuedBy    *uuid.UUID
}

// ProjectReaderService issues project readers (docs/project-reader.md): a
// service agent that can only read the allowlisted routes of one project.
// Revocation is the ordinary DELETE /agents/:id.
type ProjectReaderService struct {
	workspaces repository.WorkspaceRepository
	users      repository.UserRepository
	agents     repository.AgentRepository
	readers    repository.ProjectReaderRepository
}

// NewProjectReaderService wires the issuer. users may be nil (no handle check).
func NewProjectReaderService(workspaces repository.WorkspaceRepository, users repository.UserRepository,
	agents repository.AgentRepository, readers repository.ProjectReaderRepository) *ProjectReaderService {
	return &ProjectReaderService{workspaces: workspaces, users: users, agents: agents, readers: readers}
}

// Issue creates the reader and returns it with its key. The key is shown
// only in this answer; only its hash and digest are stored.
func (s *ProjectReaderService) Issue(ctx context.Context, in IssueProjectReaderInput) (*RegisterAgentOutput, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, apierror.ValidationError(map[string]string{"name": "name is required"})
	}
	if in.ProjectID == uuid.Nil {
		return nil, apierror.ValidationError(map[string]string{"project_id": "project_id is required"})
	}
	ws, err := s.workspaces.GetByID(ctx, in.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if ws == nil {
		return nil, apierror.NotFound("Workspace")
	}

	slug := slugify(name)
	if s.users != nil {
		u, uerr := s.users.GetByUsername(ctx, in.WorkspaceID, slug)
		if uerr != nil {
			return nil, apierror.Wrap(uerr)
		}
		if u != nil {
			return nil, apierror.Conflict(fmt.Sprintf(
				"this handle is already used by user %q in this workspace — choose a different name", u.Username))
		}
	}
	if existing, gerr := s.agents.GetBySlug(ctx, in.WorkspaceID, slug); gerr != nil {
		return nil, apierror.Wrap(gerr)
	} else if existing != nil {
		return nil, apierror.Conflict(fmt.Sprintf("an agent with handle %q already exists in this workspace", slug))
	}

	rawKey, err := generateAPIKey(ws.Slug)
	if err != nil {
		return nil, apierror.InternalError("failed to generate API key")
	}
	hash, err := bcrypt.GenerateFromPassword(bcryptInput(rawKey), bcryptCost)
	if err != nil {
		return nil, apierror.InternalError("failed to hash API key")
	}
	now := timeNow()
	expires := now.Add(agentKeyDefaultTTL)
	projectID := in.ProjectID
	agent := &domain.Agent{
		ID:               uuid.New(),
		WorkspaceID:      in.WorkspaceID,
		SupervisorUserID: in.IssuedBy,
		Name:             name,
		Slug:             slug,
		AgentType:        domain.AgentTypeCustom,
		APIKeyHash:       string(hash),
		APIKeySHA256:     agentKeyDigest(rawKey),
		APIKeyPrefix:     extractPrefix(rawKey, ws.Slug),
		// No session behind it: never auto-assigned, never woken by a mention,
		// accepts no assignments (the assignee check also refuses on the column).
		Capabilities:       json.RawMessage(`{"no_lane":true}`),
		AcceptsFrom:        json.RawMessage(`[]`),
		MentionWakes:       false,
		MaxConcurrentTasks: 0,
		Role:               ProjectReaderRole,
		WorkingHours:       "24/7",
		ProfileDescription: "Read-only service reader of one project.",
		Status:             domain.AgentStatusOffline,
		ExpiresAt:          &expires,
		CreatedAt:          now,
		UpdatedAt:          now,
		ReaderProjectID:    &projectID,
	}
	if err := s.readers.CreateProjectReader(ctx, agent); err != nil {
		return nil, err
	}
	return &RegisterAgentOutput{Agent: agent, APIKey: rawKey}, nil
}
