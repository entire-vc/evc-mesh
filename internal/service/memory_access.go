package service

import (
	"context"

	"github.com/google/uuid"
)

// MemoryAccessService records an explicit read after the caller's access has
// been authorized. Repository lookups are also used by bulk reads and internal
// checks, so they must never record access implicitly.
// Additive to MemoryService, like FinalRecallService and MemoryArchiveService.
type MemoryAccessService interface {
	TouchMemoryAccess(ctx context.Context, ids []uuid.UUID)
}

// TouchMemoryAccess preserves the one-hour idempotency window. Feedback is
// best-effort and must not fail a successful read, as on the recall path.
func (s *memoryService) TouchMemoryAccess(ctx context.Context, ids []uuid.UUID) {
	_ = s.memRepo.TouchAccessed(ctx, ids)
}
