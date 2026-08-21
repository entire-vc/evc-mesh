package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// DefaultHumanGateSoftTimeout is how long a soft-classified gate stays armed before the
// sweeper releases it. Placeholder value — WHICH real questions are soft, and for how
// long, is Pavel's policy call (contract §5, out of scope for this mechanism); this
// constant only exists so the mechanism has something concrete to sweep against until
// that policy card configures it per-class or per-label.
const DefaultHumanGateSoftTimeout = 24 * time.Hour

// releaseSoftTimeoutSystemMark is a substring unique to this release path's system
// comment. Deliberately distinct from every other human_gate release comment's wording
// (releaseHumanGate's "Pavel прокомментировал…", releaseHumanGateOnWithdrawal's "автор
// запроса отозвал его сам…", RecordHumanGateDecision's decision-quote comment) and
// deliberately NOT containing any of pavel-digest.py's _RESOLVED phrases or its
// WITHDRAWAL_SYSTEM_MARK — this release answers nothing and withdraws nothing, so a
// digest reader must keep treating the underlying ask as open. See contract §5, AC1.
const releaseSoftTimeoutSystemMark = "снят по истечении мягкого таймаута"

// HumanGateSoftTimeoutService finds soft-classified human_gate arms past their timeout
// window and releases them. This is a release IN NAME ONLY as far as the question is
// concerned — nobody answered, so the released card must remain visible to Pavel (the
// digest picks it back up off the still-present `❓ Blocking @pavel` marker comment and
// the absence of any later user reply — see human_gate.py's is_human_gated, unchanged by
// this package).
type HumanGateSoftTimeoutService interface {
	SweepExpiredSoftGates(ctx context.Context) (int, error)
}

type humanGateSoftTimeoutService struct {
	taskRepo    repository.TaskRepository
	commentRepo repository.CommentRepository
	window      time.Duration
}

// NewHumanGateSoftTimeoutService constructs a HumanGateSoftTimeoutService. window is the
// arm age past which a soft gate is eligible for release; pass <= 0 to use
// DefaultHumanGateSoftTimeout.
func NewHumanGateSoftTimeoutService(
	taskRepo repository.TaskRepository,
	commentRepo repository.CommentRepository,
	window time.Duration,
) HumanGateSoftTimeoutService {
	if window <= 0 {
		window = DefaultHumanGateSoftTimeout
	}
	return &humanGateSoftTimeoutService{taskRepo: taskRepo, commentRepo: commentRepo, window: window}
}

// SweepExpiredSoftGates releases every soft-classified gate armed at or before
// now-window. Returns the number released. A hard-classified gate can never appear here:
// FindSoftTimedOutGates filters on class in SQL, not on this function's cutoff — see that
// function's doc for the structural argument (contract §5.1).
func (s *humanGateSoftTimeoutService) SweepExpiredSoftGates(ctx context.Context) (int, error) {
	cutoff := timeNow().Add(-s.window)
	candidates, err := s.taskRepo.FindSoftTimedOutGates(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	var released int
	for _, c := range candidates {
		if err := s.taskRepo.SetHumanGate(ctx, c.TaskID, false); err != nil {
			log.Printf("[human-gate-soft-timeout] WARNING: SetHumanGate(false) on task %s failed: %v", c.TaskID, err)
			continue
		}
		s.postReleaseComment(ctx, c)
		released++
	}
	return released, nil
}

func (s *humanGateSoftTimeoutService) postReleaseComment(ctx context.Context, c domain.HumanGateSoftTimeoutCandidate) {
	if s.commentRepo == nil {
		return
	}
	now := timeNow()
	comment := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     c.TaskID,
		AuthorID:   uuid.Nil,
		AuthorType: domain.ActorTypeSystem,
		Body: fmt.Sprintf(
			"⏳ Auto: human_gate %s (взведён %s, класс: мягкий) — фиде разморожен, "+
				"но вопрос НЕ отвечен: он остаётся открытым и ждёт Pavel'я в дайджесте.",
			releaseSoftTimeoutSystemMark, c.ArmedAt.Format(time.RFC3339),
		),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, comment); err != nil {
		log.Printf("[human-gate-soft-timeout] WARNING: failed to post release comment on task %s: %v", c.TaskID, err)
	}
}
