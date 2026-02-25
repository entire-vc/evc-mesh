package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// validViewTypes is the set of allowed view_type values.
var validViewTypes = map[string]bool{
	"board":    true,
	"list":     true,
	"timeline": true,
}

// savedViewService implements SavedViewService.
type savedViewService struct {
	repo repository.SavedViewRepository
}

// NewSavedViewService returns a new SavedViewService backed by the given repository.
func NewSavedViewService(repo repository.SavedViewRepository) SavedViewService {
	return &savedViewService{repo: repo}
}

// Create validates input and persists a new saved view.
func (s *savedViewService) Create(ctx context.Context, input domain.CreateSavedViewInput) (*domain.SavedView, error) {
	if strings.TrimSpace(input.Name) == "" {
		return nil, apierror.ValidationError(map[string]string{
			"name": "name is required",
		})
	}
	if input.ViewType == "" {
		input.ViewType = "board"
	}
	if !validViewTypes[input.ViewType] {
		return nil, apierror.ValidationError(map[string]string{
			"view_type": "must be one of: board, list, timeline",
		})
	}
	if input.Filters == nil {
		input.Filters = map[string]interface{}{}
	}

	now := time.Now()
	view := &domain.SavedView{
		ID:        uuid.New(),
		ProjectID: input.ProjectID,
		Name:      strings.TrimSpace(input.Name),
		ViewType:  input.ViewType,
		Filters:   input.Filters,
		SortBy:    input.SortBy,
		SortOrder: input.SortOrder,
		Columns:   input.Columns,
		IsShared:  input.IsShared,
		CreatedBy: input.CreatedBy,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.Create(ctx, view); err != nil {
		return nil, err
	}
	return view, nil
}

// GetByID retrieves a saved view by ID.
func (s *savedViewService) GetByID(ctx context.Context, id uuid.UUID) (*domain.SavedView, error) {
	view, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, apierror.NotFound("SavedView")
	}
	return view, nil
}

// Update applies a partial update to a saved view.
// Only the owner can update their own view.
func (s *savedViewService) Update(ctx context.Context, id uuid.UUID, input domain.UpdateSavedViewInput, callerID uuid.UUID) (*domain.SavedView, error) {
	view, err := s.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if view.CreatedBy != callerID {
		return nil, apierror.Forbidden("only the owner can update this view")
	}
	if input.Name != nil && strings.TrimSpace(*input.Name) == "" {
		return nil, apierror.ValidationError(map[string]string{
			"name": "name cannot be empty",
		})
	}
	if input.ViewType != nil && !validViewTypes[*input.ViewType] {
		return nil, apierror.ValidationError(map[string]string{
			"view_type": "must be one of: board, list, timeline",
		})
	}
	return s.repo.Update(ctx, id, input)
}

// Delete removes a saved view. Only the owner can delete their own view.
func (s *savedViewService) Delete(ctx context.Context, id uuid.UUID, callerID uuid.UUID) error {
	view, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if view.CreatedBy != callerID {
		return apierror.Forbidden("only the owner can delete this view")
	}
	return s.repo.Delete(ctx, id)
}

// ListByProject returns all visible saved views for a project (own + shared).
func (s *savedViewService) ListByProject(ctx context.Context, projectID uuid.UUID, userID uuid.UUID) ([]domain.SavedView, error) {
	return s.repo.ListByProject(ctx, projectID, userID)
}
