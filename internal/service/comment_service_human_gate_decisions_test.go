package service

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// fakeHGDRepo is an in-memory stand-in for repository.HumanGateDecisionRepository,
// mirroring the real Postgres repo's semantics (append-only rows, revocation
// via revokes_id, FindLiveByRef excludes anything with a later revocation
// row) so service-layer logic can be tested without a database. The real
// query behavior is separately proven against actual Postgres in
// internal/repository/postgres/human_gate_decision_repo_db_test.go.
type fakeHGDRepo struct {
	mu   sync.Mutex
	rows []domain.HumanGateDecision
}

func (f *fakeHGDRepo) Create(_ context.Context, d *domain.HumanGateDecision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	f.rows = append(f.rows, *d)
	return nil
}

func (f *fakeHGDRepo) revocationForLocked(id uuid.UUID) *domain.HumanGateDecision {
	for i := range f.rows {
		if f.rows[i].RevokesID != nil && *f.rows[i].RevokesID == id {
			return &f.rows[i]
		}
	}
	return nil
}

func (f *fakeHGDRepo) hydrateLocked(d domain.HumanGateDecision) domain.HumanGateDecision {
	if rev := f.revocationForLocked(d.ID); rev != nil {
		createdAt := rev.CreatedAt
		d.RevokedAt = &createdAt
		d.RevokedReason = rev.RevokedReason
	}
	return d
}

func (f *fakeHGDRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.HumanGateDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.rows {
		if d.ID == id {
			hyd := f.hydrateLocked(d)
			return &hyd, nil
		}
	}
	return nil, nil
}

func (f *fakeHGDRepo) FindLiveByRef(_ context.Context, taskID uuid.UUID, questionRef *uuid.UUID, canonicalKey *string) (*domain.HumanGateDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if questionRef == nil && canonicalKey == nil {
		return nil, nil
	}
	var best *domain.HumanGateDecision
	for i := range f.rows {
		d := f.rows[i]
		if d.TaskID != taskID || d.RevokesID != nil {
			continue
		}
		match := (questionRef != nil && d.QuestionRef != nil && *d.QuestionRef == *questionRef) ||
			(canonicalKey != nil && d.CanonicalKey != nil && *d.CanonicalKey == *canonicalKey)
		if !match || f.revocationForLocked(d.ID) != nil {
			continue
		}
		if best == nil || d.CreatedAt.After(best.CreatedAt) {
			cp := d
			best = &cp
		}
	}
	return best, nil
}

func (f *fakeHGDRepo) ListByTask(_ context.Context, taskID uuid.UUID) ([]domain.HumanGateDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.HumanGateDecision
	for _, d := range f.rows {
		if d.TaskID == taskID {
			out = append(out, f.hydrateLocked(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// fakeHumanGateTaskService is a minimal TaskService stand-in whose GetByID
// and SetHumanGate stay mutually consistent — RecordHumanGateDecision and
// RevokeHumanGateDecision both read the task back after mutating it, so a
// fake that doesn't keep them in sync would make every release/refreeze
// assertion pass or fail for the wrong reason.
type fakeHumanGateTaskService struct {
	TaskService
	mu    sync.Mutex
	tasks map[uuid.UUID]*domain.Task
}

func newFakeHumanGateTaskService() *fakeHumanGateTaskService {
	return &fakeHumanGateTaskService{tasks: map[uuid.UUID]*domain.Task{}}
}

func (f *fakeHumanGateTaskService) seed(task *domain.Task) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[task.ID] = task
}

func (f *fakeHumanGateTaskService) GetByID(_ context.Context, id uuid.UUID) (*domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	if !ok {
		return nil, nil
	}
	cp := *t
	return &cp, nil
}

func (f *fakeHumanGateTaskService) SetHumanGate(_ context.Context, id uuid.UUID, value bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tasks[id]; ok {
		t.HumanGate = value
	}
	return nil
}

func (f *fakeHumanGateTaskService) humanGate(id uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	return ok && t.HumanGate
}

type hgdEnv struct {
	svc         *commentService
	hgdRepo     *fakeHGDRepo
	taskSvc     *fakeHumanGateTaskService
	commentRepo *MockCommentRepository
}

func setupHGDEnv(t *testing.T) hgdEnv {
	t.Helper()
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	hgdRepo := &fakeHGDRepo{}
	taskSvc := newFakeHumanGateTaskService()

	svc := NewCommentService(commentRepo, taskRepo, activityRepo,
		WithCommentTaskService(taskSvc),
		WithHumanGateDecisionRepo(hgdRepo),
	).(*commentService)

	return hgdEnv{svc: svc, hgdRepo: hgdRepo, taskSvc: taskSvc, commentRepo: commentRepo}
}

func (env hgdEnv) systemComments(taskID uuid.UUID) []domain.Comment {
	var out []domain.Comment
	for _, c := range env.commentRepo.items {
		if c.TaskID == taskID && c.AuthorType == domain.ActorTypeSystem {
			out = append(out, *c)
		}
	}
	return out
}

func strP(s string) *string { return &s }

// TestRecordHumanGateDecision_ReleasesLiveGate is AC1: a gated task closes
// via an attached decision record with no UI trip.
func TestRecordHumanGateDecision_ReleasesLiveGate(t *testing.T) {
	env := setupHGDEnv(t)
	taskID, pavel := uuid.New(), uuid.New()
	env.taskSvc.seed(&domain.Task{ID: taskID, HumanGate: true})
	ref := uuid.New()

	d, err := env.svc.RecordHumanGateDecision(context.Background(), domain.RecordHumanGateDecisionInput{
		TaskID: taskID, QuestionRef: &ref, DecidedBy: pavel,
		Provenance: domain.HumanGateProvenanceDirect, Channel: domain.HumanGateChannelMesh,
	})
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.False(t, env.taskSvc.humanGate(taskID), "gate must be released as a consequence of recording")

	comments := env.systemComments(taskID)
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0].Body, "human_gate снят")
	assert.Contains(t, comments[0].Body, "direct")
}

// TestRecordHumanGateDecision_TaskNotGated_NoOpOnGate proves recording a
// decision on a task whose gate is already false does not spuriously post a
// release notice — the release is a CONSEQUENCE, only fires when there was
// something to release (contract §3, P1).
func TestRecordHumanGateDecision_TaskNotGated_NoOpOnGate(t *testing.T) {
	env := setupHGDEnv(t)
	taskID, pavel := uuid.New(), uuid.New()
	env.taskSvc.seed(&domain.Task{ID: taskID, HumanGate: false})
	key := "canonical-decision-test"

	_, err := env.svc.RecordHumanGateDecision(context.Background(), domain.RecordHumanGateDecisionInput{
		TaskID: taskID, CanonicalKey: &key, DecidedBy: pavel,
		Provenance: domain.HumanGateProvenanceDirect, Channel: domain.HumanGateChannelMesh,
	})
	require.NoError(t, err)
	assert.Empty(t, env.systemComments(taskID), "no release notice when there was nothing to release")
}

// TestRecordHumanGateDecision_RequiresQuoteForNonDirect is the input-side
// mirror of the DB CHECK constraint proven in the repo db_test — fails fast
// with a clear error rather than a raw SQL constraint violation.
func TestRecordHumanGateDecision_RequiresQuoteForNonDirect(t *testing.T) {
	env := setupHGDEnv(t)
	taskID, pavel := uuid.New(), uuid.New()
	env.taskSvc.seed(&domain.Task{ID: taskID, HumanGate: true})
	ref := uuid.New()

	_, err := env.svc.RecordHumanGateDecision(context.Background(), domain.RecordHumanGateDecisionInput{
		TaskID: taskID, QuestionRef: &ref, DecidedBy: pavel,
		Provenance: domain.HumanGateProvenanceAttested, Channel: domain.HumanGateChannelChat,
	})
	require.Error(t, err)
	assert.True(t, env.taskSvc.humanGate(taskID), "a rejected record must not have released the gate")
}

// TestRevokeHumanGateDecision_RefreezesTask is AC5: after revoking a
// decision, the task is frozen again.
func TestRevokeHumanGateDecision_RefreezesTask(t *testing.T) {
	env := setupHGDEnv(t)
	taskID, pavel := uuid.New(), uuid.New()
	env.taskSvc.seed(&domain.Task{ID: taskID, HumanGate: true})
	ref := uuid.New()

	d, err := env.svc.RecordHumanGateDecision(context.Background(), domain.RecordHumanGateDecisionInput{
		TaskID: taskID, QuestionRef: &ref, DecidedBy: pavel,
		Provenance: domain.HumanGateProvenanceDirect, Channel: domain.HumanGateChannelMesh,
	})
	require.NoError(t, err)
	require.False(t, env.taskSvc.humanGate(taskID))

	err = env.svc.RevokeHumanGateDecision(context.Background(), domain.RevokeHumanGateDecisionInput{
		DecisionID: d.ID, RevokedBy: pavel, RevokedByType: domain.ActorTypeUser,
		Reason: "dispute — this was never confirmed",
	})
	require.NoError(t, err)
	assert.True(t, env.taskSvc.humanGate(taskID), "task must be frozen again after revocation")

	comments := env.systemComments(taskID)
	var sawRefreeze bool
	for _, c := range comments {
		if strings.Contains(c.Body, "взведён снова") {
			sawRefreeze = true
		}
	}
	assert.True(t, sawRefreeze, "the re-freeze must be visible in the task thread")
}

// TestRevokeHumanGateDecision_RejectsNonUserActor is the negative control
// for the "only a human may revoke" rule — defense-in-depth mirror of
// task_handler.go's PATCH {human_gate:false} 403 (AC2's spirit, applied to
// this new endpoint).
func TestRevokeHumanGateDecision_RejectsNonUserActor(t *testing.T) {
	env := setupHGDEnv(t)
	taskID, pavel, agent := uuid.New(), uuid.New(), uuid.New()
	env.taskSvc.seed(&domain.Task{ID: taskID, HumanGate: true})
	ref := uuid.New()

	d, err := env.svc.RecordHumanGateDecision(context.Background(), domain.RecordHumanGateDecisionInput{
		TaskID: taskID, QuestionRef: &ref, DecidedBy: pavel,
		Provenance: domain.HumanGateProvenanceDirect, Channel: domain.HumanGateChannelMesh,
	})
	require.NoError(t, err)

	err = env.svc.RevokeHumanGateDecision(context.Background(), domain.RevokeHumanGateDecisionInput{
		DecisionID: d.ID, RevokedBy: agent, RevokedByType: domain.ActorTypeAgent, Reason: "trying to self-clear",
	})
	require.Error(t, err, "an agent must never be able to revoke a decision")
	assert.False(t, env.taskSvc.humanGate(taskID), "the rejected revocation must not have refrozen the task")
}

// TestRevokeHumanGateDecision_AlreadyRevoked proves a decision cannot be
// revoked twice.
func TestRevokeHumanGateDecision_AlreadyRevoked(t *testing.T) {
	env := setupHGDEnv(t)
	taskID, pavel := uuid.New(), uuid.New()
	env.taskSvc.seed(&domain.Task{ID: taskID, HumanGate: true})
	ref := uuid.New()

	d, err := env.svc.RecordHumanGateDecision(context.Background(), domain.RecordHumanGateDecisionInput{
		TaskID: taskID, QuestionRef: &ref, DecidedBy: pavel,
		Provenance: domain.HumanGateProvenanceDirect, Channel: domain.HumanGateChannelMesh,
	})
	require.NoError(t, err)

	revoke := domain.RevokeHumanGateDecisionInput{
		DecisionID: d.ID, RevokedBy: pavel, RevokedByType: domain.ActorTypeUser, Reason: "first revoke",
	}
	require.NoError(t, env.svc.RevokeHumanGateDecision(context.Background(), revoke))

	revoke.Reason = "second attempt"
	err = env.svc.RevokeHumanGateDecision(context.Background(), revoke)
	require.ErrorIs(t, err, ErrHumanGateDecisionAlreadyRevoked)
}

// ---------------------------------------------------------------------------
// enforceBlockingTriage repeat-question prevention (contract §6, AC3+AC4)
// ---------------------------------------------------------------------------

// setupHGDTriageEnv wires a full enforceBlockingTriage-capable env (unlike
// hgdEnv above, which only needs taskSvc+hgdRepo) with the decision repo
// also attached, so the repeat-check inside enforceBlockingTriage is live.
func setupHGDTriageEnv(t *testing.T) (triageTestEnv, *fakeHGDRepo) {
	t.Helper()
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	statusRepo := NewMockTaskStatusRepository()
	projectRepo := NewMockProjectRepository()
	userRepo := NewMockUserRepository()
	taskMover := &fakeTaskMover{}
	hgdRepo := &fakeHGDRepo{}

	wsID, projID := uuid.New(), uuid.New()
	projectRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}
	inProgressID := uuid.New()
	statusRepo.items[inProgressID] = &domain.TaskStatus{
		ID: inProgressID, ProjectID: projID, Category: domain.StatusCategoryInProgress, Name: "In Progress",
	}
	userRepo.AddUser(wsID, &domain.User{ID: uuid.New(), Username: "pavel", Name: "Pavel"})

	svc := NewCommentService(commentRepo, taskRepo, activityRepo,
		WithCommentProjectRepo(projectRepo),
		WithCommentStatusRepo(statusRepo),
		WithCommentUserRepo(userRepo),
		WithCommentTaskService(taskMover),
		WithHumanGateDecisionRepo(hgdRepo),
	).(*commentService)

	env := triageTestEnv{
		svc: svc, commentRepo: commentRepo, taskRepo: taskRepo, statusRepo: statusRepo,
		userRepo: userRepo, taskMover: taskMover, projID: projID, wsID: wsID,
		inProgressID: inProgressID, activityRepo: activityRepo,
	}
	return env, hgdRepo
}

// TestEnforceBlockingTriage_RepeatQuestion_DoesNotArm is AC3: a repeat
// marker (a reply to an already-answered marker thread) does not arm the
// flag and shows the existing record.
func TestEnforceBlockingTriage_RepeatQuestion_DoesNotArm(t *testing.T) {
	env, hgdRepo := setupHGDTriageEnv(t)
	taskID := env.seedTask(env.inProgressID)

	original := &domain.Comment{
		TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Body: "❓ **Blocking @pavel**: original question",
	}
	require.NoError(t, env.svc.Create(context.Background(), original))
	env.taskMover.mu.Lock()
	env.taskMover.gateSetCalls = nil // reset — only the REPEAT marker's arm/no-arm behavior is under test
	env.taskMover.mu.Unlock()

	require.NoError(t, hgdRepo.Create(context.Background(), &domain.HumanGateDecision{
		TaskID: taskID, QuestionRef: &original.ID, DecidedBy: uuid.New(),
		Provenance: ptr(domain.HumanGateProvenanceDirect), Channel: ptr(domain.HumanGateChannelMesh),
	}))

	repeat := &domain.Comment{
		TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
		ParentCommentID: &original.ID,
		Body:            "❓ **Blocking @pavel**: same question, re-raised",
	}
	require.NoError(t, env.svc.Create(context.Background(), repeat))

	assert.Empty(t, env.taskMover.humanGateCalls(), "a repeat question must never call SetHumanGate")
	var sawNotice bool
	for _, c := range env.systemComments() {
		if strings.Contains(c.Body, "уже отвечен") {
			sawNotice = true
		}
	}
	assert.True(t, sawNotice, "the existing record must be surfaced")
}

// TestEnforceBlockingTriage_DifferentQuestion_DoesArm is AC4, the reverse
// control: a marker about a DIFFERENT question on the same task arms the
// flag normally — proving AC3 isn't green because arming broke entirely.
func TestEnforceBlockingTriage_DifferentQuestion_DoesArm(t *testing.T) {
	env, hgdRepo := setupHGDTriageEnv(t)
	taskID := env.seedTask(env.inProgressID)

	original := &domain.Comment{
		ID: uuid.New(), TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Body: "❓ **Blocking @pavel**: original question",
	}
	require.NoError(t, hgdRepo.Create(context.Background(), &domain.HumanGateDecision{
		TaskID: taskID, QuestionRef: &original.ID, DecidedBy: uuid.New(),
		Provenance: ptr(domain.HumanGateProvenanceDirect), Channel: ptr(domain.HumanGateChannelMesh),
	}))

	// No ParentCommentID and no canonical_key metadata citation — a
	// genuinely unrelated question, not a reply to the answered thread.
	newQuestion := &domain.Comment{
		TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Body: "❓ **Blocking @pavel**: a completely different question",
	}
	require.NoError(t, env.svc.Create(context.Background(), newQuestion))

	calls := env.taskMover.humanGateCalls()
	require.Len(t, calls, 1, "an unrelated question must arm the gate normally")
	assert.True(t, calls[0].value)
}

// TestEnforceBlockingTriage_CanonicalKeyCitation_DoesNotArm proves the
// second matching path — an explicit canonical_key citation in the new
// marker's metadata, not a reply relationship.
func TestEnforceBlockingTriage_CanonicalKeyCitation_DoesNotArm(t *testing.T) {
	env, hgdRepo := setupHGDTriageEnv(t)
	taskID := env.seedTask(env.inProgressID)
	key := "canonical-decision-2026-08-21-tr-free-tier"

	require.NoError(t, hgdRepo.Create(context.Background(), &domain.HumanGateDecision{
		TaskID: taskID, CanonicalKey: &key, DecidedBy: uuid.New(),
		Provenance: ptr(domain.HumanGateProvenanceBridged), Channel: ptr(domain.HumanGateChannelTelegram),
		Quote: strP("да, отвечал уже"),
	}))

	repeat := &domain.Comment{
		TaskID: taskID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Body:     "❓ **Blocking @pavel**: citing the same decision explicitly",
		Metadata: []byte(`{"canonical_key":"canonical-decision-2026-08-21-tr-free-tier"}`),
	}
	require.NoError(t, env.svc.Create(context.Background(), repeat))

	assert.Empty(t, env.taskMover.humanGateCalls(), "citing a live canonical_key must not arm the gate")
}
