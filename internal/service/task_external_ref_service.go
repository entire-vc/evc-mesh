package service

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// ErrExternalRefInvalid is returned for an empty or oversized ref/hash.
var ErrExternalRefInvalid = errors.New("invalid external ref")

// ErrExternalHashConflict means the card is already bound to a different hash.
var ErrExternalHashConflict = errors.New("external hash already set to a different value")

// TaskExternalRefService binds a card to an external object and pins its hash.
type TaskExternalRefService interface {
	Get(ctx context.Context, taskID uuid.UUID) (*domain.TaskExternalRef, error)
	Put(ctx context.Context, taskID uuid.UUID, ref, hash string, setBy *uuid.UUID) (*domain.TaskExternalRef, error)
}

type taskExternalRefService struct {
	repo repository.TaskExternalRefRepository
}

// NewTaskExternalRefService creates the service.
func NewTaskExternalRefService(repo repository.TaskExternalRefRepository) TaskExternalRefService {
	return &taskExternalRefService{repo: repo}
}

func (s *taskExternalRefService) Get(ctx context.Context, taskID uuid.UUID) (*domain.TaskExternalRef, error) {
	return s.repo.Get(ctx, taskID)
}

// Put validates and stores the binding. The hash is write-once: the same hash
// may be re-sent (ref is refreshed, the call is idempotent), a different one is
// ErrExternalHashConflict and nothing changes.
func (s *taskExternalRefService) Put(ctx context.Context, taskID uuid.UUID, ref, hash string, setBy *uuid.UUID) (*domain.TaskExternalRef, error) {
	ref, hash = strings.TrimSpace(ref), strings.TrimSpace(hash)
	if ref == "" || len(ref) > domain.MaxExternalRefLen || hash == "" || len(hash) > domain.MaxExternalHashLen {
		return nil, ErrExternalRefInvalid
	}
	stored, changed, err := s.repo.Put(ctx, &domain.TaskExternalRef{TaskID: taskID, Ref: ref, Hash: hash, SetBy: setBy})
	if err != nil {
		return nil, err
	}
	if !changed {
		return stored, ErrExternalHashConflict
	}
	return stored, nil
}
