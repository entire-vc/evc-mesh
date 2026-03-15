package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// keySlugRegex matches valid memory keys: lowercase alphanumeric with hyphens,
// starting and ending with an alphanumeric character, at least two characters long.
var keySlugRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

type memoryService struct {
	memRepo repository.MemoryRepository
}

// NewMemoryService returns a new MemoryService backed by the given repository.
func NewMemoryService(memRepo repository.MemoryRepository) MemoryService {
	return &memoryService{memRepo: memRepo}
}

// Remember upserts a memory entry. It returns "created" if the key did not exist before,
// or "updated" if an existing entry was overwritten.
func (s *memoryService) Remember(ctx context.Context, mem *domain.Memory) (string, error) {
	if mem.Key == "" {
		return "", apierror.ValidationError(map[string]string{
			"key": "key is required",
		})
	}
	if !keySlugRegex.MatchString(mem.Key) {
		return "", apierror.ValidationError(map[string]string{
			"key": "key must match pattern ^[a-z0-9][a-z0-9-]*[a-z0-9]$ (lowercase alphanumeric with hyphens)",
		})
	}
	if mem.Content == "" {
		return "", apierror.ValidationError(map[string]string{
			"content": "content is required",
		})
	}
	if mem.WorkspaceID == uuid.Nil {
		return "", apierror.ValidationError(map[string]string{
			"workspace_id": "workspace_id is required",
		})
	}

	// Determine whether this is a create or update by checking for an existing entry.
	existing, err := s.memRepo.GetByKey(ctx, mem.WorkspaceID, mem.ProjectID, mem.AgentID, mem.Key, mem.Scope)
	if err != nil {
		return "", fmt.Errorf("memory remember: lookup existing: %w", err)
	}

	outcome := "created"
	if existing != nil {
		outcome = "updated"
		// Preserve the original ID so the upsert constraint matches.
		mem.ID = existing.ID
	}

	if err := s.memRepo.Upsert(ctx, mem); err != nil {
		return "", fmt.Errorf("memory remember: upsert: %w", err)
	}
	return outcome, nil
}

// Recall performs a full-text search and boosts the relevance of returned results.
func (s *memoryService) Recall(ctx context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, error) {
	if opts.Query == "" {
		return nil, apierror.ValidationError(map[string]string{
			"q": "search query is required",
		})
	}
	if opts.Limit <= 0 {
		opts.Limit = 20
	}

	var projID *uuid.UUID
	if opts.ProjectID != uuid.Nil {
		projID = &opts.ProjectID
	}

	results, err := s.memRepo.FullTextSearch(ctx, opts.Query, opts.WorkspaceID, projID, string(opts.Scope), opts.Tags, opts.Limit)
	if err != nil {
		return nil, fmt.Errorf("memory recall: full text search: %w", err)
	}

	if len(results) > 0 {
		ids := make([]uuid.UUID, len(results))
		for i, r := range results {
			ids[i] = r.ID
		}
		// Boost relevance as positive feedback — non-fatal if it fails.
		_ = s.memRepo.BoostRelevance(ctx, ids)
	}

	return results, nil
}

// GetProjectKnowledge returns all non-expired memories for a workspace (and optional project).
func (s *memoryService) GetProjectKnowledge(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID) ([]domain.Memory, error) {
	return s.memRepo.ListByWorkspaceProject(ctx, workspaceID, projectID)
}

// Forget deletes a memory by ID. Agents may only delete their own agent-scope memories.
// Admins (isAdmin=true) may delete any memory.
func (s *memoryService) Forget(ctx context.Context, id uuid.UUID, actorAgentID *uuid.UUID, isAdmin bool) error {
	mem, err := s.memRepo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("memory forget: get by id: %w", err)
	}
	if mem == nil {
		return apierror.NotFound("Memory")
	}

	if !isAdmin {
		// Non-admin agents may only delete their own agent-scope memories.
		if actorAgentID == nil {
			return apierror.Forbidden("only admins can delete memories created by other actors")
		}
		if mem.Scope != domain.ScopeAgent || mem.AgentID == nil || *mem.AgentID != *actorAgentID {
			return apierror.Forbidden("agents may only delete their own agent-scope memories")
		}
	}

	if err := s.memRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("memory forget: delete: %w", err)
	}
	return nil
}

// GetByID returns a single memory by primary key.
func (s *memoryService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Memory, error) {
	mem, err := s.memRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("memory get by id: %w", err)
	}
	if mem == nil {
		return nil, apierror.NotFound("Memory")
	}
	return mem, nil
}

// ExtractFromEvent inspects an event and its optional hint to decide whether to
// persist a memory. When hint.Persist is true, memory fields come from the hint.
// For auto-extract: context_update events whose payload contains context_type of
// "decision", "instruction", or "reference" are automatically stored.
func (s *memoryService) ExtractFromEvent(ctx context.Context, event *domain.EventBusMessage, hint *domain.MemoryHint) error {
	if event == nil {
		return nil
	}

	var mem *domain.Memory

	if hint != nil && hint.Persist {
		// Explicit persist request from hint.
		expiresAt, _ := parseDuration(hint.ExpiresIn)

		mem = &domain.Memory{
			WorkspaceID:   event.WorkspaceID,
			ProjectID:     &event.ProjectID,
			AgentID:       event.AgentID,
			Key:           hint.Key,
			Content:       event.Subject,
			Scope:         hint.Scope,
			Tags:          hint.Tags,
			SourceType:    domain.SourceAgent,
			SourceEventID: &event.ID,
			Relevance:     0.5,
			ExpiresAt:     expiresAt,
		}

		// Auto-generate key from subject if not provided.
		if mem.Key == "" {
			mem.Key = memorySlugify(event.Subject)
		}
	} else if string(event.EventType) == "context_update" {
		// Auto-extract from context_update events.
		mem = s.autoExtractFromContextUpdate(event)
	}

	if mem == nil {
		return nil
	}

	if mem.Key == "" {
		mem.Key = memorySlugify(event.Subject)
	}
	if mem.Key == "" || !keySlugRegex.MatchString(mem.Key) {
		// Cannot produce a valid key — skip silently.
		return nil
	}

	if err := s.memRepo.Upsert(ctx, mem); err != nil {
		return fmt.Errorf("memory extract from event: upsert: %w", err)
	}
	return nil
}

// autoExtractFromContextUpdate auto-creates a memory when a context_update event
// has a payload with context_type = decision | instruction | reference.
func (s *memoryService) autoExtractFromContextUpdate(event *domain.EventBusMessage) *domain.Memory {
	if len(event.Payload) == 0 {
		return nil
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return nil
	}

	contextType, _ := payload["context_type"].(string)
	switch contextType {
	case "decision", "instruction", "reference":
		// Valid auto-extract type.
	default:
		return nil
	}

	key := memorySlugify(event.Subject)
	if key == "" || !keySlugRegex.MatchString(key) {
		return nil
	}

	return &domain.Memory{
		WorkspaceID:   event.WorkspaceID,
		ProjectID:     &event.ProjectID,
		AgentID:       event.AgentID,
		Key:           key,
		Content:       event.Subject,
		Scope:         domain.ScopeProject,
		SourceType:    domain.SourceSystem,
		SourceEventID: &event.ID,
		Relevance:     0.3,
	}
}

// memorySlugify converts an arbitrary string into a valid memory key slug.
// It lowercases the input, replaces non-alphanumeric runs with a single hyphen,
// trims leading/trailing hyphens, and truncates to 100 characters.
// This is distinct from the agent-service slugify which does not collapse runs.
func memorySlugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))

	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevHyphen = false
		} else {
			if !prevHyphen {
				b.WriteRune('-')
				prevHyphen = true
			}
		}
	}

	result := strings.Trim(b.String(), "-")
	if len(result) > 100 {
		result = result[:100]
		result = strings.TrimRight(result, "-")
	}
	return result
}

// parseDuration parses a Go duration string (e.g. "72h") and returns an expiry time pointer.
// Returns nil when input is empty. Returns an error for invalid formats.
func parseDuration(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return nil, err
	}
	t := time.Now().Add(d)
	return &t, nil
}
