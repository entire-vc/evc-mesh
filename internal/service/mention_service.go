package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

type mentionService struct {
	mentionRepo repository.CommentMentionRepository
}

// NewMentionService returns a MentionService backed by the given repository.
func NewMentionService(mentionRepo repository.CommentMentionRepository) MentionService {
	return &mentionService{mentionRepo: mentionRepo}
}

func (s *mentionService) List(ctx context.Context, mentionedID uuid.UUID, mentionedKind string, filter repository.MentionFilter) ([]domain.CommentMentionView, error) {
	return s.mentionRepo.List(ctx, mentionedID, mentionedKind, filter)
}

func (s *mentionService) MarkSeen(ctx context.Context, commentID, mentionedID uuid.UUID) error {
	return s.mentionRepo.MarkSeen(ctx, commentID, mentionedID)
}

func (s *mentionService) CountUnseen(ctx context.Context, mentionedID uuid.UUID, mentionedKind string) (int64, error) {
	return s.mentionRepo.CountUnseen(ctx, mentionedID, mentionedKind)
}
