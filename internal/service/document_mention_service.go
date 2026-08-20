package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

type documentMentionService struct {
	mentionRepo repository.DocumentCommentMentionRepository
}

// NewDocumentMentionService returns a DocumentMentionService backed by the given
// repository.
func NewDocumentMentionService(mentionRepo repository.DocumentCommentMentionRepository) DocumentMentionService {
	return &documentMentionService{mentionRepo: mentionRepo}
}

func (s *documentMentionService) List(
	ctx context.Context,
	mentionedID uuid.UUID,
	mentionedKind string,
	filter repository.MentionFilter,
) ([]domain.DocumentCommentMentionView, error) {
	return s.mentionRepo.List(ctx, mentionedID, mentionedKind, filter)
}

func (s *documentMentionService) MarkSeen(ctx context.Context, commentID, mentionedID uuid.UUID) error {
	return s.mentionRepo.MarkSeen(ctx, commentID, mentionedID)
}

func (s *documentMentionService) CountUnseen(
	ctx context.Context,
	mentionedID uuid.UUID,
	mentionedKind string,
) (int64, error) {
	return s.mentionRepo.CountUnseen(ctx, mentionedID, mentionedKind)
}
