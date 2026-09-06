package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// HumanGateSoftTimeoutService tests (task #b1d5c742, contract
// docs/human-gate-decision-recorded.md §5).
//
// The DB-level structural proof that a hard gate can never be selected lives in
// task_repo_human_gate_class_db_test.go (real Postgres). These tests cover the
// SERVICE's own behavior on top of whatever the repo returns: it must actually
// release what it's handed, and its release comment must not collide with any of
// pavel-digest.py's negator/resolution phrases (or the sweep would silently
// un-surface a question nobody answered — the exact regression AC1 forbids).
// ---------------------------------------------------------------------------

func newSoftTimeoutHarness() (*MockTaskRepository, *MockCommentRepository, HumanGateSoftTimeoutService) {
	taskRepo := NewMockTaskRepository()
	commentRepo := NewMockCommentRepository()
	svc := NewHumanGateSoftTimeoutService(taskRepo, commentRepo, time.Hour)
	return taskRepo, commentRepo, svc
}

func seedGateTask(t *testing.T, repo *MockTaskRepository, class domain.HumanGateClass, armed bool, armedAt *time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	task := &domain.Task{
		ID:               id,
		HumanGate:        armed,
		HumanGateClass:   class,
		HumanGateArmedAt: armedAt,
	}
	if err := repo.Create(context.Background(), task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return id
}

func timePtr(t time.Time) *time.Time { return &t }

// TestHumanGateSoftTimeoutService_ReleasesWhatRepoReturns proves the service
// actually flips human_gate false and posts a release comment for every candidate
// the repository hands it — it does not re-filter or drop candidates.
func TestHumanGateSoftTimeoutService_ReleasesWhatRepoReturns(t *testing.T) {
	taskRepo, commentRepo, svc := newSoftTimeoutHarness()
	ancient := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	id := seedGateTask(t, taskRepo, domain.HumanGateClassSoft, true, timePtr(ancient))

	n, err := svc.SweepExpiredSoftGates(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 released, got %d", n)
	}

	got, _ := taskRepo.GetByID(context.Background(), id)
	if got.HumanGate {
		t.Fatal("expected human_gate to be released")
	}

	var found *domain.Comment
	for _, c := range commentRepo.items {
		if c.TaskID == id {
			found = c
		}
	}
	if found == nil {
		t.Fatal("expected a release comment to be posted")
	}
	if found.AuthorType != domain.ActorTypeSystem {
		t.Fatalf("release comment must be system-authored, got %s", found.AuthorType)
	}
	if !strings.Contains(found.Body, releaseSoftTimeoutSystemMark) {
		t.Fatalf("release comment must carry its own distinct marker, got: %s", found.Body)
	}
}

// TestHumanGateSoftTimeoutService_NoCandidates_NoOp proves an empty repo result
// (nothing due) releases nothing and posts nothing — no accidental comment spam
// on every idle tick.
func TestHumanGateSoftTimeoutService_NoCandidates_NoOp(t *testing.T) {
	_, commentRepo, svc := newSoftTimeoutHarness()

	n, err := svc.SweepExpiredSoftGates(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 released, got %d", n)
	}
	if len(commentRepo.items) != 0 {
		t.Fatalf("expected no comments, got %d", len(commentRepo.items))
	}
}

// TestHumanGateSoftTimeoutService_ReleaseComment_AvoidsDigestNegatorPhrases pins the
// release comment text against the exact phrases pavel-digest.py's human_gate.py /
// pavel-digest.py treat as "this ask is over" — contract §5 AC1 requires the released
// card to STAY in the digest, and both of those modules decide that from comment text,
// not from the human_gate flag alone (a still-present `❓ Blocking @pavel` marker plus
// no later user reply keeps a card surfaced regardless of the flag — see
// bob/scripts/human_gate.py:is_human_gated). A comment that accidentally matched one of
// these phrases would silently drop the card from Pavel's queue despite no answer ever
// having arrived — the exact regression this test exists to catch.
func TestHumanGateSoftTimeoutService_ReleaseComment_AvoidsDigestNegatorPhrases(t *testing.T) {
	taskRepo, commentRepo, svc := newSoftTimeoutHarness()
	ancient := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	seedGateTask(t, taskRepo, domain.HumanGateClassSoft, true, timePtr(ancient))

	if _, err := svc.SweepExpiredSoftGates(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Mirrors bob/scripts/pavel-digest.py's WITHDRAWAL_SYSTEM_MARK and _RESOLVED tuple
	// verbatim — see that file if this list needs to be re-synced.
	forbidden := []string{
		"автор запроса отозвал его сам", // WITHDRAWAL_SYSTEM_MARK
		"ключи даны", "ключи переданы",
		"live-switch выполнен", "live-switch завершён", "live-switch done",
		"блокер снят", "блокер сн",
		"тех-часть закрыт", "технически закрыт", "технические части",
		"все dod выполнен",
		"осталось только твой close", "осталось только pavel",
		"только pavel вручную", "только pavel может",
		"готово к твоему close", "ready for your close", "ready to close",
		"human_gate=true",
		"закрыть может только pavel", "в done двигает только pavel",
	}

	var body string
	for _, c := range commentRepo.items {
		body = strings.ToLower(c.Body)
	}
	if body == "" {
		t.Fatal("expected a release comment")
	}
	for _, phrase := range forbidden {
		if strings.Contains(body, phrase) {
			t.Fatalf("release comment must not contain digest negator/resolution phrase %q: %s", phrase, body)
		}
	}
	// And it must not itself look like a NEW ask — it must not raise a fresh
	// `❓ Blocking @pavel` marker (that would re-arm the exact freeze it just cleared).
	if strings.Contains(body, "blocking @") {
		t.Fatalf("release comment must not itself read as a new Blocking marker: %s", body)
	}
}

// TestHumanGateSoftTimeoutService_DefaultWindow proves the constructor falls back to
// DefaultHumanGateSoftTimeout when given a non-positive window, rather than sweeping
// with an effectively-zero (or negative, always-due) window.
func TestHumanGateSoftTimeoutService_DefaultWindow(t *testing.T) {
	taskRepo := NewMockTaskRepository()
	commentRepo := NewMockCommentRepository()
	svc := NewHumanGateSoftTimeoutService(taskRepo, commentRepo, 0)

	justArmed := time.Now().UTC()
	seedGateTask(t, taskRepo, domain.HumanGateClassSoft, true, timePtr(justArmed))

	n, err := svc.SweepExpiredSoftGates(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Fatalf("a just-armed soft gate must not be due under the default (24h) window, got %d released", n)
	}
}

// TestHumanGateSoftTimeoutService_FindError_Propagates proves a repository lookup
// failure surfaces as an error instead of being swallowed as "nothing due".
func TestHumanGateSoftTimeoutService_FindError_Propagates(t *testing.T) {
	taskRepo := NewMockTaskRepository()
	commentRepo := NewMockCommentRepository()
	svc := NewHumanGateSoftTimeoutService(taskRepo, commentRepo, time.Hour)
	taskRepo.errToReturn = errors.New("db unreachable")

	n, err := svc.SweepExpiredSoftGates(context.Background())
	if err == nil {
		t.Fatal("expected FindSoftTimedOutGates error to propagate")
	}
	if n != 0 {
		t.Fatalf("expected 0 released on lookup failure, got %d", n)
	}
}

// failSetHumanGateRepo wraps MockTaskRepository so FindSoftTimedOutGates succeeds
// normally (the mock's shared errToReturn would fail BOTH calls, which can't isolate
// "lookup ok, release fails") while SetHumanGate always fails.
type failSetHumanGateRepo struct {
	*MockTaskRepository
	err error
}

func (r *failSetHumanGateRepo) ArmHumanGate(context.Context, domain.ArmHumanGateInput) error {
	return nil
}

func (r *failSetHumanGateRepo) SetHumanGate(context.Context, uuid.UUID, bool) error {
	return r.err
}

// TestHumanGateSoftTimeoutService_SetHumanGateError_SkipsBestEffort proves a release
// failure on one candidate is logged and skipped (not counted, not fatal to the sweep)
// rather than aborting the whole batch or panicking.
func TestHumanGateSoftTimeoutService_SetHumanGateError_SkipsBestEffort(t *testing.T) {
	mockRepo := NewMockTaskRepository()
	taskRepo := &failSetHumanGateRepo{MockTaskRepository: mockRepo, err: errors.New("write conflict")}
	commentRepo := NewMockCommentRepository()
	svc := NewHumanGateSoftTimeoutService(taskRepo, commentRepo, time.Hour)
	ancient := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	seedGateTask(t, mockRepo, domain.HumanGateClassSoft, true, timePtr(ancient))

	n, err := svc.SweepExpiredSoftGates(context.Background())
	if err != nil {
		t.Fatalf("a per-candidate SetHumanGate failure must not fail the whole sweep: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 released when SetHumanGate errors, got %d", n)
	}
	if len(commentRepo.items) != 0 {
		t.Fatal("must not post a release comment for a candidate whose release failed")
	}
}

// TestHumanGateSoftTimeoutService_NilCommentRepo_StillReleases mirrors
// MonitorPromotionService's "commentRepo may be nil" contract: the release itself must
// not depend on comment posting succeeding or even being configured.
func TestHumanGateSoftTimeoutService_NilCommentRepo_StillReleases(t *testing.T) {
	taskRepo := NewMockTaskRepository()
	svc := NewHumanGateSoftTimeoutService(taskRepo, nil, time.Hour)
	ancient := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	id := seedGateTask(t, taskRepo, domain.HumanGateClassSoft, true, timePtr(ancient))

	n, err := svc.SweepExpiredSoftGates(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 released even with no comment repo configured, got %d", n)
	}
	got, _ := taskRepo.GetByID(context.Background(), id)
	if got.HumanGate {
		t.Fatal("expected human_gate to be released regardless of comment posting")
	}
}

// TestHumanGateSoftTimeoutService_CommentCreateError_StillCountsRelease proves a
// failure posting the release comment does not undo or hide the release itself — the
// gate is already unfrozen by the time the comment is attempted, so a comment failure
// must be logged, not treated as sweep failure.
func TestHumanGateSoftTimeoutService_CommentCreateError_StillCountsRelease(t *testing.T) {
	taskRepo := NewMockTaskRepository()
	commentRepo := NewMockCommentRepository()
	svc := NewHumanGateSoftTimeoutService(taskRepo, commentRepo, time.Hour)
	ancient := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	id := seedGateTask(t, taskRepo, domain.HumanGateClassSoft, true, timePtr(ancient))
	commentRepo.errToReturn = errors.New("comment write failed")

	n, err := svc.SweepExpiredSoftGates(context.Background())
	if err != nil {
		t.Fatalf("a comment-posting failure must not fail the sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected the release to still count even if the comment failed, got %d", n)
	}
	got, _ := taskRepo.GetByID(context.Background(), id)
	if got.HumanGate {
		t.Fatal("expected human_gate to be released despite the comment failure")
	}
}
