package service

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

type mentionablesService struct {
	agentRepo repository.AgentRepository
	userRepo  repository.UserRepository
}

// NewMentionablesService creates a new MentionablesService.
func NewMentionablesService(agentRepo repository.AgentRepository, userRepo repository.UserRepository) MentionablesService {
	return &mentionablesService{agentRepo: agentRepo, userRepo: userRepo}
}

func (s *mentionablesService) Search(ctx context.Context, workspaceID uuid.UUID, query string, limit int) ([]domain.Mentionable, error) {
	agents, err := s.agentRepo.SearchByPrefix(ctx, workspaceID, query, limit)
	if err != nil {
		return nil, err
	}

	users, err := s.userRepo.SearchInWorkspace(ctx, workspaceID, query, limit)
	if err != nil {
		return nil, err
	}

	results := make([]domain.Mentionable, 0, len(agents)+len(users))
	lower := strings.ToLower(query)

	for _, a := range agents {
		var avatarURL *string
		m := domain.Mentionable{
			ID:          a.ID,
			Kind:        "agent",
			Slug:        a.Slug,
			DisplayName: a.Name,
			AvatarURL:   avatarURL,
		}
		_ = lower
		results = append(results, m)
	}
	for _, u := range users {
		slug := u.Username
		if slug == "" {
			slug = strings.ToLower(strings.ReplaceAll(u.Name, " ", "."))
		}
		var avatarURL *string
		if u.AvatarURL != "" {
			avatarURL = &u.AvatarURL
		}
		results = append(results, domain.Mentionable{
			ID:          u.ID,
			Kind:        "user",
			Slug:        slug,
			DisplayName: u.Name,
			AvatarURL:   avatarURL,
		})
	}

	// Sort: exact prefix match first, then alphabetical by display_name.
	sort.SliceStable(results, func(i, j int) bool {
		iPrefix := strings.HasPrefix(strings.ToLower(results[i].Slug), lower) ||
			strings.HasPrefix(strings.ToLower(results[i].DisplayName), lower)
		jPrefix := strings.HasPrefix(strings.ToLower(results[j].Slug), lower) ||
			strings.HasPrefix(strings.ToLower(results[j].DisplayName), lower)
		if iPrefix != jPrefix {
			return iPrefix
		}
		return strings.ToLower(results[i].DisplayName) < strings.ToLower(results[j].DisplayName)
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
