package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// MemoryArchiveService is the hand archive of a memory: take a stale
// entry out of default recall without deleting it, bring it back, and read a key
// exactly, archived and expired rows included on request. Additive, like
// FinalRecallService: the handler asserts it and answers 503 without it.
type MemoryArchiveService interface {
	SetMemoryArchived(ctx context.Context, id uuid.UUID, archived bool, reason string, actor MemoryArchiveActor) (*domain.MemoryArchiveResult, error)
	ListMemoriesByKey(ctx context.Context, lookup domain.MemoryKeyLookup) ([]domain.Memory, error)
}

// MemoryArchiveActor is who asks to archive, resolved by the handler.
type MemoryArchiveActor struct {
	// AgentID is set for an agent caller; nil for a user.
	AgentID *uuid.UUID
	// UserID is set for a user caller; recorded on the revision as its actor.
	UserID *uuid.UUID
	// AgentRole is the calling agent's team-directory role ("lead", "CPO", ...).
	AgentRole string
	// UserRole is the calling user's role in the memory's workspace.
	UserRole string
	// MayWriteProject reports whether the caller may write in a project. Nil
	// refuses every project.
	MayWriteProject func(uuid.UUID) bool
}

// workspaceMemoryCurators are the directory roles allowed to archive a
// workspace-scope memory: workspace memory is fleet-wide, so only the roles that
// curate it may take an entry out of everyone's recall.
var workspaceMemoryCurators = []string{"lead", "cpo", "coordinator"}

func hasCuratorRole(role string) bool {
	for _, part := range strings.Split(role, ",") {
		p := strings.ToLower(strings.TrimSpace(part))
		for _, c := range workspaceMemoryCurators {
			if p == c {
				return true
			}
		}
	}
	return false
}

// authorizeMemoryArchive applies the same bar as writing to the memory's scope:
//   - agent scope: only the owning agent;
//   - project scope: anyone who may write in that project, author or not
//     (users need a writing workspace role as well);
//   - workspace scope (and a project-scope row with no project): workspace
//     owner/admin users and lead/CPO/coordinator agents.
func authorizeMemoryArchive(mem *domain.Memory, actor MemoryArchiveActor) error {
	isUser := actor.AgentID == nil
	switch mem.Scope {
	case domain.ScopeAgent:
		if isUser || mem.AgentID == nil || *mem.AgentID != *actor.AgentID {
			return apierror.Forbidden("an agent-scope memory can be archived only by the agent that owns it")
		}
		return nil
	case domain.ScopeProject:
		if mem.ProjectID != nil {
			if isUser && actor.UserRole != domain.RoleOwner && actor.UserRole != domain.RoleAdmin && actor.UserRole != domain.RoleMember {
				return apierror.Forbidden("archiving a project memory needs write access to the project")
			}
			if actor.MayWriteProject == nil || !actor.MayWriteProject(*mem.ProjectID) {
				return apierror.Forbidden("archiving a project memory needs write access to the project")
			}
			return nil
		}
	}
	if isUser {
		if actor.UserRole == domain.RoleOwner || actor.UserRole == domain.RoleAdmin {
			return nil
		}
	} else if hasCuratorRole(actor.AgentRole) {
		return nil
	}
	return apierror.Forbidden("a workspace memory can be archived only by a workspace owner/admin or a lead agent")
}

// validateArchiveReason: the reason is what makes an archive reviewable later,
// so it is required and bounded.
func validateArchiveReason(reason string) (string, error) {
	r := strings.TrimSpace(reason)
	if r == "" {
		return "", apierror.ValidationError(map[string]string{"reason": "reason is required: say why this memory leaves (or returns to) recall"})
	}
	if utf8.RuneCountInString(r) > domain.MemoryArchiveReasonMaxLen {
		return "", apierror.ValidationError(map[string]string{"reason": fmt.Sprintf("reason is limited to %d characters", domain.MemoryArchiveReasonMaxLen)})
	}
	return r, nil
}

// SetMemoryArchived archives (archived=true) or restores a memory by hand,
// recording the reason on a new revision.
func (s *memoryService) SetMemoryArchived(ctx context.Context, id uuid.UUID, archived bool, reason string, actor MemoryArchiveActor) (*domain.MemoryArchiveResult, error) {
	r, err := validateArchiveReason(reason)
	if err != nil {
		return nil, err
	}
	mem, err := s.memRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("memory archive: get by id: %w", err)
	}
	if mem == nil {
		return nil, apierror.NotFound("Memory")
	}
	if aerr := authorizeMemoryArchive(mem, actor); aerr != nil {
		return nil, aerr
	}
	res, err := s.memRepo.SetArchivedWithRevision(ctx, id, archived, r, actor.AgentID, actor.UserID)
	if err != nil {
		return nil, fmt.Errorf("memory archive: %w", err)
	}
	if res == nil {
		return nil, apierror.NotFound("Memory")
	}
	return res, nil
}

// ListMemoriesByKey is the exact-key read behind get_memory.
func (s *memoryService) ListMemoriesByKey(ctx context.Context, lookup domain.MemoryKeyLookup) ([]domain.Memory, error) {
	if strings.TrimSpace(lookup.Key) == "" {
		return nil, apierror.ValidationError(map[string]string{"key": "key is required"})
	}
	rows, err := s.memRepo.ListByKey(ctx, lookup)
	if err != nil {
		return nil, fmt.Errorf("memory by key: %w", err)
	}
	if rows == nil {
		rows = []domain.Memory{}
	}
	return rows, nil
}
