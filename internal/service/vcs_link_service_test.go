package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ---------------------------------------------------------------------------
// Local test doubles. We define them inline rather than reusing the wider
// mock_services_test.go pile so the vcs_link orchestrator's contract is
// readable in one file.
// ---------------------------------------------------------------------------

// fakeVCSLinkRepo is an in-memory VCSLinkRepository.
type fakeVCSLinkRepo struct {
	mu    sync.Mutex
	links map[uuid.UUID]*domain.VCSLink
	// upsertKey: (task_id|provider|link_type|external_id) → link.ID
	upsertKey map[string]uuid.UUID
	// createErr/upsertErr, when set, are returned by Create/Upsert instead of
	// succeeding — used to exercise vcsLinkService.Create's and
	// HandleGitHubPullRequestEvent's error branches (a DB failure on either
	// path, e.g. #b73171fa's Upsert).
	createErr error
	upsertErr error
}

func newFakeVCSLinkRepo() *fakeVCSLinkRepo {
	return &fakeVCSLinkRepo{
		links:     map[uuid.UUID]*domain.VCSLink{},
		upsertKey: map[string]uuid.UUID{},
	}
}

func conflictKey(taskID uuid.UUID, p domain.VCSProvider, lt domain.VCSLinkType, eid string) string {
	return taskID.String() + "|" + string(p) + "|" + string(lt) + "|" + eid
}

func (r *fakeVCSLinkRepo) Create(_ context.Context, l *domain.VCSLink) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return r.createErr
	}
	r.links[l.ID] = l
	r.upsertKey[conflictKey(l.TaskID, l.Provider, l.LinkType, l.ExternalID)] = l.ID
	return nil
}

func (r *fakeVCSLinkRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.VCSLink, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.links[id], nil
}

func (r *fakeVCSLinkRepo) Delete(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.links, id)
	return nil
}

func (r *fakeVCSLinkRepo) ListByTask(_ context.Context, taskID uuid.UUID) ([]domain.VCSLink, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.VCSLink
	for _, l := range r.links {
		if l.TaskID == taskID {
			out = append(out, *l)
		}
	}
	return out, nil
}

func (r *fakeVCSLinkRepo) Upsert(_ context.Context, l *domain.VCSLink) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.upsertErr != nil {
		return false, r.upsertErr
	}
	key := conflictKey(l.TaskID, l.Provider, l.LinkType, l.ExternalID)
	if existingID, ok := r.upsertKey[key]; ok {
		existing := r.links[existingID]
		existing.Status = l.Status
		existing.Title = l.Title
		existing.Metadata = l.Metadata
		existing.URL = l.URL
		// Mirrors real Postgres: id/created_at are never touched by the
		// update branch — reflect the row's actual values back into l, same
		// contract as VCSLinkRepo.Upsert (#b73171fa).
		l.ID = existing.ID
		l.CreatedAt = existing.CreatedAt
		return false, nil
	}
	r.links[l.ID] = l
	r.upsertKey[key] = l.ID
	return true, nil
}

func (r *fakeVCSLinkRepo) ListByExternalID(_ context.Context, p domain.VCSProvider, lt domain.VCSLinkType, eid string) ([]domain.VCSLink, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.VCSLink
	for _, l := range r.links {
		if l.Provider == p && l.LinkType == lt && l.ExternalID == eid {
			out = append(out, *l)
		}
	}
	return out, nil
}

// fakeTaskRepo holds tasks by ID; only GetByID is exercised by the orchestrator.
type fakeTaskRepo struct {
	tasks map[uuid.UUID]*domain.Task
}

func (r *fakeTaskRepo) Create(context.Context, *domain.Task) error { return nil }
func (r *fakeTaskRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Task, error) {
	return r.tasks[id], nil
}

// GetByShortID mirrors the postgres behaviour the resolver depends on: prefix
// match, apierror.NotFound on no match, apierror.BadRequest when the prefix is
// ambiguous. A fake that just returned nil would make the ambiguity test pass
// for the wrong reason.
func (r *fakeTaskRepo) GetByShortID(_ context.Context, prefix string) (*domain.Task, error) {
	var hits []*domain.Task
	for id, tk := range r.tasks {
		if strings.HasPrefix(id.String(), strings.ToLower(prefix)) {
			hits = append(hits, tk)
		}
	}
	switch len(hits) {
	case 0:
		return nil, apierror.NotFound("Task")
	case 1:
		return hits[0], nil
	default:
		return nil, apierror.BadRequest("ambiguous short ID: multiple tasks match")
	}
}
func (r *fakeTaskRepo) Search(context.Context, uuid.UUID, repository.TaskFilter, pagination.Params) (*pagination.Page[domain.Task], error) {
	return nil, nil
}
func (r *fakeTaskRepo) Update(_ context.Context, t *domain.Task) error {
	r.tasks[t.ID] = t
	return nil
}
func (r *fakeTaskRepo) Delete(context.Context, uuid.UUID) error { return nil }
func (r *fakeTaskRepo) List(context.Context, uuid.UUID, repository.TaskFilter, pagination.Params) (*pagination.Page[domain.Task], error) {
	return nil, nil
}
func (r *fakeTaskRepo) ListByAssignee(context.Context, uuid.UUID, uuid.UUID, domain.AssigneeType, repository.AssigneeTaskFilter) ([]domain.Task, int, error) {
	return nil, 0, nil
}
func (r *fakeTaskRepo) ListSubtasks(context.Context, uuid.UUID) ([]domain.Task, error) {
	return nil, nil
}
func (r *fakeTaskRepo) CountByStatus(context.Context, uuid.UUID) (map[uuid.UUID]int, error) {
	return nil, nil
}
func (r *fakeTaskRepo) CountByStatusCategory(context.Context, uuid.UUID) (map[domain.StatusCategory]int, error) {
	return nil, nil
}
func (r *fakeTaskRepo) ListByStatusCategory(context.Context, uuid.UUID, domain.StatusCategory, pagination.Params) (*pagination.Page[domain.Task], error) {
	return nil, nil
}
func (r *fakeTaskRepo) AtomicCheckout(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) error {
	return nil
}
func (r *fakeTaskRepo) ReleaseCheckout(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *fakeTaskRepo) ExtendCheckout(context.Context, uuid.UUID, uuid.UUID, time.Time) error {
	return nil
}
func (r *fakeTaskRepo) MoveToProject(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}
func (r *fakeTaskRepo) ForceReleaseCheckout(context.Context, uuid.UUID) error  { return nil }
func (r *fakeTaskRepo) ReleaseExpiredCheckouts(context.Context) (int64, error) { return 0, nil }
func (r *fakeTaskRepo) FindExpiredInProgressCheckouts(context.Context) ([]domain.Task, error) {
	return nil, nil
}
func (r *fakeTaskRepo) FindDueMonitorBacklogTasks(context.Context) ([]domain.Task, error) {
	return nil, nil
}
func (r *fakeTaskRepo) ListByUserActive(context.Context, uuid.UUID, uuid.UUID, pagination.Params) (*pagination.Page[domain.Task], error) {
	return nil, nil
}
func (r *fakeTaskRepo) ListOpenByRecurringScheduleID(context.Context, uuid.UUID, uuid.UUID) ([]domain.Task, error) {
	return nil, nil
}

func (r *fakeTaskRepo) SetHumanGate(context.Context, uuid.UUID, bool) error { return nil }
func (r *fakeTaskRepo) SetShipped(context.Context, uuid.UUID, bool) error   { return nil }
func (r *fakeTaskRepo) SetDodCheck(context.Context, uuid.UUID, string, string, string) error {
	return nil
}

// fakeStatusRepo holds task_status rows per project.
type fakeStatusRepo struct {
	byID        map[uuid.UUID]*domain.TaskStatus
	byProject   map[uuid.UUID][]domain.TaskStatus
	defaultByPr map[uuid.UUID]uuid.UUID
}

func newFakeStatusRepo() *fakeStatusRepo {
	return &fakeStatusRepo{
		byID:        map[uuid.UUID]*domain.TaskStatus{},
		byProject:   map[uuid.UUID][]domain.TaskStatus{},
		defaultByPr: map[uuid.UUID]uuid.UUID{},
	}
}

func (r *fakeStatusRepo) addStatus(projectID uuid.UUID, slug string, cat domain.StatusCategory) *domain.TaskStatus {
	s := &domain.TaskStatus{
		ID:        uuid.New(),
		ProjectID: projectID,
		Slug:      slug,
		Name:      slug,
		Category:  cat,
	}
	r.byID[s.ID] = s
	r.byProject[projectID] = append(r.byProject[projectID], *s)
	return s
}

func (r *fakeStatusRepo) Create(context.Context, *domain.TaskStatus) error { return nil }
func (r *fakeStatusRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.TaskStatus, error) {
	return r.byID[id], nil
}
func (r *fakeStatusRepo) Update(context.Context, *domain.TaskStatus) error { return nil }
func (r *fakeStatusRepo) Delete(context.Context, uuid.UUID) error          { return nil }
func (r *fakeStatusRepo) ListByProject(_ context.Context, pid uuid.UUID) ([]domain.TaskStatus, error) {
	return r.byProject[pid], nil
}
func (r *fakeStatusRepo) GetDefaultForProject(_ context.Context, pid uuid.UUID) (*domain.TaskStatus, error) {
	if id, ok := r.defaultByPr[pid]; ok {
		return r.byID[id], nil
	}
	return nil, nil
}
func (r *fakeStatusRepo) Reorder(context.Context, uuid.UUID, []uuid.UUID) error { return nil }

// fakeTaskService is a minimal TaskService that only implements MoveTask
// against the fake task + status repos. The orchestrator only calls MoveTask
// — every other method is a no-op so the fake can implement the interface.
type fakeTaskService struct {
	taskRepo   *fakeTaskRepo
	statusRepo *fakeStatusRepo
	moveCalls  []moveCall
}

func (t *fakeTaskService) Create(context.Context, *domain.Task) error { return nil }
func (t *fakeTaskService) GetByID(_ context.Context, id uuid.UUID) (*domain.Task, error) {
	return t.taskRepo.tasks[id], nil
}
func (t *fakeTaskService) GetByShortID(context.Context, string) (*domain.Task, error) {
	return nil, nil
}
func (t *fakeTaskService) Search(context.Context, uuid.UUID, repository.TaskFilter, pagination.Params) (*pagination.Page[domain.Task], error) {
	return nil, nil
}
func (t *fakeTaskService) Update(context.Context, *domain.Task) error { return nil }
func (t *fakeTaskService) Delete(context.Context, uuid.UUID) error    { return nil }
func (t *fakeTaskService) List(context.Context, uuid.UUID, repository.TaskFilter, pagination.Params) (*pagination.Page[domain.Task], error) {
	return nil, nil
}
func (t *fakeTaskService) MoveTask(_ context.Context, taskID uuid.UUID, input MoveTaskInput) error {
	if input.StatusID == nil {
		return nil
	}
	t.moveCalls = append(t.moveCalls, moveCall{taskID: taskID, input: input})
	tk := t.taskRepo.tasks[taskID]
	if tk != nil {
		tk.StatusID = *input.StatusID
	}
	return nil
}
func (t *fakeTaskService) AssignTask(context.Context, uuid.UUID, AssignTaskInput) error { return nil }
func (t *fakeTaskService) CreateSubtask(context.Context, uuid.UUID, CreateSubtaskInput) (*domain.Task, error) {
	return nil, nil
}
func (t *fakeTaskService) ListSubtasks(context.Context, uuid.UUID) ([]domain.Task, error) {
	return nil, nil
}
func (t *fakeTaskService) GetMyTasks(context.Context, uuid.UUID, uuid.UUID, domain.AssigneeType, repository.AssigneeTaskFilter) ([]domain.Task, int, error) {
	return nil, 0, nil
}
func (t *fakeTaskService) GetDefaultStatus(context.Context, uuid.UUID) (*domain.TaskStatus, error) {
	return nil, nil
}
func (t *fakeTaskService) BulkUpdate(context.Context, uuid.UUID, BulkUpdateTasksInput) BulkUpdateTasksResult {
	return BulkUpdateTasksResult{}
}
func (t *fakeTaskService) CheckoutTask(context.Context, uuid.UUID, int, map[string]interface{}) (*CheckoutResult, error) {
	return nil, nil
}
func (t *fakeTaskService) ForceReleaseCheckout(context.Context, uuid.UUID) error       { return nil }
func (t *fakeTaskService) SelfReleaseCheckout(context.Context, uuid.UUID) error        { return nil }
func (t *fakeTaskService) ReleaseCheckout(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (t *fakeTaskService) ExtendCheckout(context.Context, uuid.UUID, uuid.UUID, int) (*CheckoutResult, error) {
	return nil, nil
}
func (t *fakeTaskService) MoveToProject(context.Context, uuid.UUID, uuid.UUID) (*domain.Task, error) {
	return nil, nil
}
func (t *fakeTaskService) GetStatusByID(context.Context, uuid.UUID) (*domain.TaskStatus, error) {
	return nil, nil
}
func (t *fakeTaskService) GetUserActiveTasks(context.Context, uuid.UUID, uuid.UUID, pagination.Params) (*pagination.Page[domain.Task], error) {
	return nil, nil
}
func (t *fakeTaskService) SupersedeRecurringInstances(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (t *fakeTaskService) SetHumanGate(context.Context, uuid.UUID, bool) error { return nil }
func (t *fakeTaskService) ShipTask(context.Context, uuid.UUID, bool) error     { return nil }
func (t *fakeTaskService) SetDodCheck(context.Context, uuid.UUID, string, string, string) error {
	return nil
}
func (t *fakeTaskService) ValidateAssigneeForProject(_ context.Context, _ uuid.UUID, _ *uuid.UUID, assigneeType domain.AssigneeType) (domain.AssigneeType, error) {
	return assigneeType, nil
}

// fakeCommentService captures Create calls so tests can assert on the comment
// body and authorship.
type fakeCommentService struct {
	created []*domain.Comment
}

func (c *fakeCommentService) Create(_ context.Context, comment *domain.Comment) error {
	c.created = append(c.created, comment)
	return nil
}
func (c *fakeCommentService) Update(context.Context, *domain.Comment) error { return nil }
func (c *fakeCommentService) Delete(context.Context, uuid.UUID) error       { return nil }
func (c *fakeCommentService) ListByTask(context.Context, uuid.UUID, repository.CommentFilter, pagination.Params) (*pagination.Page[domain.Comment], error) {
	return nil, nil
}
func (c *fakeCommentService) ListByAuthor(context.Context, uuid.UUID, repository.CommentViewFilter) (*domain.CommentViewPage, error) {
	return nil, nil
}
func (c *fakeCommentService) ListRecentByWorkspace(context.Context, uuid.UUID, repository.CommentViewFilter) (*domain.CommentViewPage, error) {
	return nil, nil
}
func (c *fakeCommentService) GetHumanGateOwner(context.Context, uuid.UUID) (*domain.HumanGateInfo, error) {
	return nil, nil
}
func (c *fakeCommentService) RecordHumanGateDecision(context.Context, domain.RecordHumanGateDecisionInput) (*domain.HumanGateDecision, error) {
	return nil, nil
}
func (c *fakeCommentService) RevokeHumanGateDecision(context.Context, domain.RevokeHumanGateDecisionInput) error {
	return nil
}
func (c *fakeCommentService) ListHumanGateDecisions(context.Context, uuid.UUID) ([]domain.HumanGateDecision, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Test fixtures.
// ---------------------------------------------------------------------------

type harness struct {
	repo       *fakeVCSLinkRepo
	taskRepo   *fakeTaskRepo
	statusRepo *fakeStatusRepo
	taskSvc    *fakeTaskService
	commentSvc *fakeCommentService
	svc        VCSLinkService
	projectID  uuid.UUID
	statusIDs  map[domain.StatusCategory]uuid.UUID
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	projectID := uuid.New()
	statusRepo := newFakeStatusRepo()
	todo := statusRepo.addStatus(projectID, "todo", domain.StatusCategoryTodo)
	inProgress := statusRepo.addStatus(projectID, "in_progress", domain.StatusCategoryInProgress)
	review := statusRepo.addStatus(projectID, "review", domain.StatusCategoryReview)
	done := statusRepo.addStatus(projectID, "done", domain.StatusCategoryDone)

	taskRepo := &fakeTaskRepo{tasks: map[uuid.UUID]*domain.Task{}}
	taskSvc := &fakeTaskService{taskRepo: taskRepo, statusRepo: statusRepo}
	commentSvc := &fakeCommentService{}
	repo := newFakeVCSLinkRepo()

	svc := NewVCSLinkService(repo,
		WithVCSTaskRepo(taskRepo),
		WithVCSStatusRepo(statusRepo),
		WithVCSTaskService(taskSvc),
		WithVCSCommentService(commentSvc),
	)

	return &harness{
		repo:       repo,
		taskRepo:   taskRepo,
		statusRepo: statusRepo,
		taskSvc:    taskSvc,
		commentSvc: commentSvc,
		svc:        svc,
		projectID:  projectID,
		statusIDs: map[domain.StatusCategory]uuid.UUID{
			domain.StatusCategoryTodo:       todo.ID,
			domain.StatusCategoryInProgress: inProgress.ID,
			domain.StatusCategoryReview:     review.ID,
			domain.StatusCategoryDone:       done.ID,
		},
	}
}

func (h *harness) makeTask(t *testing.T, cat domain.StatusCategory) *domain.Task {
	t.Helper()
	id := uuid.New()
	statusID, ok := h.statusIDs[cat]
	require.True(t, ok, "unknown status category %s", cat)
	tk := &domain.Task{ID: id, ProjectID: h.projectID, StatusID: statusID}
	h.taskRepo.tasks[id] = tk
	return tk
}

func mergedClosedEvent(prNum int, taskID uuid.UUID, title string) GitHubWebhookEvent {
	return GitHubWebhookEvent{
		Action:    "closed",
		PRNumber:  prNum,
		PRTitle:   title,
		PRBody:    "",
		PRHTMLURL: "https://github.com/example/repo/pull/" + uintToString(prNum),
		PRState:   "closed",
		PRMerged:  true,
		MergeSHA:  "deadbeefcafebabe",
	}
}

func uintToString(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	if n < 0 {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

// ---------------------------------------------------------------------------
// Test 1 (spec): PR merged, task=in_progress → moves to review.
// ---------------------------------------------------------------------------
func TestHandlePR_MergedFromInProgress_TransitionsToReview(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)

	ev := mergedClosedEvent(101, task.ID, "MESH-"+task.ID.String())

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.True(t, res.Transitioned)
	assert.Equal(t, "in_progress", res.OldStatus)
	assert.Equal(t, "review", res.NewStatus)

	// Task actually moved.
	assert.Equal(t, h.statusIDs[domain.StatusCategoryReview], h.taskRepo.tasks[task.ID].StatusID)

	// MoveTask was called once.
	require.Len(t, h.taskSvc.moveCalls, 1)
	assert.Equal(t, task.ID, h.taskSvc.moveCalls[0].taskID)

	// Comment posted.
	require.Len(t, h.commentSvc.created, 1)
	c := h.commentSvc.created[0]
	assert.Equal(t, task.ID, c.TaskID)
	assert.Equal(t, domain.ActorTypeAgent, c.AuthorType)
	assert.Contains(t, c.Body, "PR #101 merged")
	assert.Contains(t, c.Body, "moved to review")

	// Link upserted with status=merged.
	links, _ := h.repo.ListByTask(context.Background(), task.ID)
	require.Len(t, links, 1)
	assert.Equal(t, domain.VCSLinkStatusMerged, links[0].Status)
}

// ---------------------------------------------------------------------------
// Test 2 (spec): PR merged, task=review → moves to done.
// ---------------------------------------------------------------------------
func TestHandlePR_MergedFromReview_TransitionsToDone(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryReview)

	ev := mergedClosedEvent(202, task.ID, "MESH-"+task.ID.String())

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.True(t, res.Transitioned)
	assert.Equal(t, "review", res.OldStatus)
	assert.Equal(t, "done", res.NewStatus)
	assert.Equal(t, h.statusIDs[domain.StatusCategoryDone], h.taskRepo.tasks[task.ID].StatusID)
	require.Len(t, h.commentSvc.created, 1)
	assert.Contains(t, h.commentSvc.created[0].Body, "moved to done")
}

// ---------------------------------------------------------------------------
// Test 3 (spec): PR merged, task=todo → no transition.
// ---------------------------------------------------------------------------
func TestHandlePR_MergedFromTodo_NoTransition(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryTodo)

	ev := mergedClosedEvent(303, task.ID, "MESH-"+task.ID.String())

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.False(t, res.Transitioned)
	assert.Equal(t, "source_status_not_eligible", res.Reason)
	assert.Equal(t, "todo", res.OldStatus)

	// Task did NOT move.
	assert.Equal(t, h.statusIDs[domain.StatusCategoryTodo], h.taskRepo.tasks[task.ID].StatusID)
	assert.Empty(t, h.taskSvc.moveCalls, "MoveTask must not be called")

	require.Len(t, h.commentSvc.created, 1)
	assert.Contains(t, h.commentSvc.created[0].Body, "no auto-transition")
}

// ---------------------------------------------------------------------------
// Test 4 (spec): PR closed without merge → comment posted, no status change.
// ---------------------------------------------------------------------------
func TestHandlePR_ClosedWithoutMerge_NoStatusChange(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)

	ev := GitHubWebhookEvent{
		Action:    "closed",
		PRNumber:  404,
		PRTitle:   "MESH-" + task.ID.String(),
		PRState:   "closed",
		PRMerged:  false,
		PRHTMLURL: "https://github.com/example/repo/pull/404",
	}

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.False(t, res.Transitioned)
	assert.Equal(t, "closed_without_merge", res.Reason)
	assert.Empty(t, h.taskSvc.moveCalls)

	require.Len(t, h.commentSvc.created, 1)
	assert.Contains(t, h.commentSvc.created[0].Body, "closed without merge")

	// Link should be present with status=closed (handler upserts before exit).
	links, _ := h.repo.ListByTask(context.Background(), task.ID)
	require.Len(t, links, 1)
	assert.Equal(t, domain.VCSLinkStatusClosed, links[0].Status)
}

// ---------------------------------------------------------------------------
// Test 5a (spec): Multi-PR — only 1 merged → no transition.
// ---------------------------------------------------------------------------
func TestHandlePR_MultiPR_PartialMerge_NoTransition(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)

	// Pre-populate a second OPEN PR linked to the same task so the merge of
	// PR #500 triggers "awaiting other PRs" rather than a transition.
	pendingLinkID := uuid.New()
	_, err := h.repo.Upsert(context.Background(), &domain.VCSLink{
		ID:         pendingLinkID,
		TaskID:     task.ID,
		Provider:   domain.VCSProviderGitHub,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: "501",
		Status:     domain.VCSLinkStatusOpen,
		CreatedAt:  time.Now().Add(-time.Hour),
	})
	require.NoError(t, err)

	ev := mergedClosedEvent(500, task.ID, "MESH-"+task.ID.String())
	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.False(t, res.Transitioned)
	assert.Equal(t, "awaiting_other_prs", res.Reason)
	assert.Empty(t, h.taskSvc.moveCalls)

	require.Len(t, h.commentSvc.created, 1)
	assert.Contains(t, h.commentSvc.created[0].Body, "Awaiting 1 more PR")
}

// ---------------------------------------------------------------------------
// Test 5b (spec): Multi-PR — all merged → transition fires on the last one.
// ---------------------------------------------------------------------------
func TestHandlePR_MultiPR_AllMerged_TransitionsOnLast(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)

	// Pre-populate an already-merged PR #600 so the merge of #601 closes the set.
	_, err := h.repo.Upsert(context.Background(), &domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     task.ID,
		Provider:   domain.VCSProviderGitHub,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: "600",
		Status:     domain.VCSLinkStatusMerged,
		CreatedAt:  time.Now().Add(-time.Hour),
	})
	require.NoError(t, err)

	ev := mergedClosedEvent(601, task.ID, "MESH-"+task.ID.String())
	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.True(t, res.Transitioned)
	assert.Equal(t, "review", res.NewStatus)
	assert.Equal(t, h.statusIDs[domain.StatusCategoryReview], h.taskRepo.tasks[task.ID].StatusID)
}

// ---------------------------------------------------------------------------
// Test 8 (spec): MESH-<uuid> in body but not title → resolves.
// ---------------------------------------------------------------------------
func TestHandlePR_MeshRefInBody_Resolves(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)

	ev := GitHubWebhookEvent{
		Action:    "closed",
		PRNumber:  701,
		PRTitle:   "feat: random title",
		PRBody:    "Closes MESH-" + task.ID.String() + ".\n\nDetails ...",
		PRState:   "closed",
		PRMerged:  true,
		PRHTMLURL: "https://github.com/example/repo/pull/701",
		MergeSHA:  "abc1234",
	}

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.True(t, res.Transitioned)
	assert.Equal(t, task.ID, res.TaskID)
}

// ---------------------------------------------------------------------------
// Test 9 (spec): no MESH ref but external_id matches an existing link → resolves.
// ---------------------------------------------------------------------------
func TestHandlePR_NoMeshRef_FallsBackToExternalID(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)

	// Pre-populate an open link for PR #800 — simulates a prior delivery (e.g.
	// "opened" event that ran when the PR title still had the MESH ref).
	_, err := h.repo.Upsert(context.Background(), &domain.VCSLink{
		ID:         uuid.New(),
		TaskID:     task.ID,
		Provider:   domain.VCSProviderGitHub,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: "800",
		Status:     domain.VCSLinkStatusOpen,
		CreatedAt:  time.Now().Add(-time.Hour),
	})
	require.NoError(t, err)

	// New close-event without MESH ref in title or body.
	ev := GitHubWebhookEvent{
		Action:    "closed",
		PRNumber:  800,
		PRTitle:   "fix: typo",
		PRBody:    "",
		PRState:   "closed",
		PRMerged:  true,
		PRHTMLURL: "https://github.com/example/repo/pull/800",
		MergeSHA:  "f00b1234",
	}

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, task.ID, res.TaskID, "external_id fallback should resolve to the originally-linked task")
	assert.True(t, res.Transitioned)
}

// ---------------------------------------------------------------------------
// Sanity: action=opened just upserts the link, no transition.
// ---------------------------------------------------------------------------
func TestHandlePR_ActionOpened_NoTransition(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)

	ev := GitHubWebhookEvent{
		Action:    "opened",
		PRNumber:  900,
		PRTitle:   "MESH-" + task.ID.String(),
		PRState:   "open",
		PRMerged:  false,
		PRHTMLURL: "https://github.com/example/repo/pull/900",
	}

	res, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.NoError(t, err)
	assert.False(t, res.Transitioned)
	assert.Equal(t, "not_closed", res.Reason)
	assert.Empty(t, h.taskSvc.moveCalls)

	links, _ := h.repo.ListByTask(context.Background(), task.ID)
	require.Len(t, links, 1)
	assert.Equal(t, domain.VCSLinkStatusOpen, links[0].Status)
}

// ---------------------------------------------------------------------------
// Create: link_type canonicalisation.
// ---------------------------------------------------------------------------

// The HTTP edge normalises link_type, but the service is the last shared
// choke point before the repository — and the uniqueness index treats "pr"
// and "pull_request" as different links to the same PR. Canonicalising here
// too means no caller can split the vocabulary, whichever entry point it
// uses.
func TestVCSLinkService_Create_CanonicalisesLinkType(t *testing.T) {
	for _, given := range []string{"pr", "pull_request", "PR", "Pull_Request"} {
		t.Run(given, func(t *testing.T) {
			h := newHarness(t)
			task := h.makeTask(t, domain.StatusCategoryInProgress)

			link, _, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
				TaskID:     task.ID,
				LinkType:   domain.VCSLinkType(given),
				ExternalID: "42",
				URL:        "https://github.com/entire-vc/evc-mesh/pull/42",
			})
			require.NoError(t, err)
			assert.Equal(t, domain.VCSLinkTypePR, link.LinkType)

			stored, err := h.repo.GetByID(context.Background(), link.ID)
			require.NoError(t, err)
			assert.Equal(t, domain.VCSLinkTypePR, stored.LinkType)
		})
	}
}

// An unrecognised value is left untouched rather than coerced — the service
// is not the validation layer, and silently rewriting an unknown type would
// hide a caller's mistake instead of surfacing it.
func TestVCSLinkService_Create_LeavesUnknownLinkTypeAlone(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)

	link, _, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID:     task.ID,
		LinkType:   domain.VCSLinkType("tag"),
		ExternalID: "v1.2.3",
		URL:        "https://github.com/entire-vc/evc-mesh/releases/tag/v1.2.3",
	})
	require.NoError(t, err)
	assert.Equal(t, domain.VCSLinkType("tag"), link.LinkType)
}

// ---------------------------------------------------------------------------
// Create: status defaulting and explicit-status re-link (#df734dd9).
//
// Root cause under test: add_vcs_link previously had no way to record a PR's
// status at link time, so a link created for an already-merged PR sat with
// status="" forever — no webhook fires for a merge that happened before the
// link existed — and the done-evidence gate (service.MoveTask, #2697392d)
// blocks any non-merged/non-closed PR link unconditionally.
// ---------------------------------------------------------------------------

// A caller that omits status on a PR link gets an explicit "open", not an
// ambiguous "". The done-evidence gate's inequality check already treated ""
// the same as "open" (blocks either way), so this is a data-quality fix, not
// a behavior change: the stored value now says what we actually know.
func TestVCSLinkService_Create_DefaultsEmptyStatusToOpenForPRLink(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)

	link, _, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID:     task.ID,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: "42",
		URL:        "https://github.com/entire-vc/evc-mesh/pull/42",
	})
	require.NoError(t, err)
	assert.Equal(t, domain.VCSLinkStatusOpen, link.Status)

	stored, err := h.repo.GetByID(context.Background(), link.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.VCSLinkStatusOpen, stored.Status)
}

// Non-PR link types (commit, branch) have no meaningful "open/merged/closed"
// state — VCSLinkStatus is documented as "PRs only" — so an omitted status
// must stay empty rather than being defaulted to "open", which would be a
// meaningless claim about a commit.
func TestVCSLinkService_Create_DoesNotDefaultStatusForNonPRLinkTypes(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)

	link, _, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID:     task.ID,
		LinkType:   domain.VCSLinkTypeCommit,
		ExternalID: "deadbeef",
		URL:        "https://github.com/entire-vc/evc-mesh/commit/deadbeef",
	})
	require.NoError(t, err)
	assert.Equal(t, domain.VCSLinkStatus(""), link.Status)
}

// The core fix: a caller who knows the PR is already merged (the exact
// scenario a webhook can never cover, since it was merged before the link
// existed) can say so at link time and have it stick.
func TestVCSLinkService_Create_ExplicitMergedStatusIsStored(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryReview)

	link, _, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID:     task.ID,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: "40",
		URL:        "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
		Status:     domain.VCSLinkStatusMerged,
	})
	require.NoError(t, err)
	assert.Equal(t, domain.VCSLinkStatusMerged, link.Status)

	stored, err := h.repo.GetByID(context.Background(), link.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.VCSLinkStatusMerged, stored.Status)
}

// Re-linking with an explicit status must succeed even when a link to the
// same (task, provider, link_type, external_id) already exists — that
// uniqueness collision is exactly what made a3bdf4ad's rows permanently
// uncorrectable before this fix: the only way to record the real status was
// to add_vcs_link again, and a plain insert 409s/500s on the unique index.
func TestVCSLinkService_Create_ExplicitStatusUpsertsOnExistingLink(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryReview)

	// First call: no status known yet (mirrors the historical add_vcs_link
	// behavior before this fix) — lands as "open".
	first, created, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID:     task.ID,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: "40",
		URL:        "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
	})
	require.NoError(t, err)
	require.Equal(t, domain.VCSLinkStatusOpen, first.Status)
	require.True(t, created, "the first call for a new (task,provider,link_type,external_id) must insert")

	// Second call: the caller now knows it's merged and re-links with an
	// explicit status. Must not error, and must not create a second row.
	second, created, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID:     task.ID,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: "40",
		URL:        "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
		Status:     domain.VCSLinkStatusMerged,
	})
	require.NoError(t, err, "re-linking with an explicit status must succeed, not fail on the unique index")

	// The core regression (#b73171fa): the upsert branch used to echo back a
	// freshly-generated id/created_at instead of the row it actually updated,
	// making a correctly-applied update look like a newly created duplicate.
	// Equality, not mere presence — a vacuous "field exists" assert would pass
	// on the broken code too.
	assert.False(t, created, "an update onto an existing link must report created=false")
	assert.Equal(t, first.ID, second.ID, "the upsert response must carry the EXISTING row's id, not a freshly generated one")
	assert.Equal(t, first.CreatedAt, second.CreatedAt, "the upsert response must carry the EXISTING row's created_at, not time.Now()")

	stored, err := h.repo.GetByID(context.Background(), second.ID)
	require.NoError(t, err)
	assert.Equal(t, first.ID, stored.ID, "the response id must match what a follow-up GET actually returns")
	assert.Equal(t, first.CreatedAt, stored.CreatedAt)

	links, err := h.repo.ListByTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Len(t, links, 1, "re-linking the same PR must update the existing row, not duplicate it")
	assert.Equal(t, domain.VCSLinkStatusMerged, links[0].Status)
}

// A caller that omits status keeps the plain-insert path (explicitStatus ==
// false must call repo.Create, never repo.Upsert). Proved by colliding on
// the same (task, provider, link_type, external_id) as an existing 'merged'
// link without stating a status: if this ever went through Upsert instead,
// the second call's defaulted "open" would silently overwrite the existing
// row's real status. It must not — an accidental duplicate/uninformed
// add_vcs_link call is a distinct case from "I'm correcting the status"
// (that case is Test_ExplicitStatusUpsertsOnExistingLink above), and must
// leave the original row exactly as it was.
func TestVCSLinkService_Create_ImplicitStatusNeverCallsUpsert(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryReview)

	merged, _, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID:     task.ID,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: "40",
		URL:        "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
		Status:     domain.VCSLinkStatusMerged,
	})
	require.NoError(t, err)
	require.Equal(t, domain.VCSLinkStatusMerged, merged.Status)

	// Same external_id, no status — the exact shape of an accidental
	// duplicate add_vcs_link call.
	_, _, err = h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID:     task.ID,
		LinkType:   domain.VCSLinkTypePR,
		ExternalID: "40",
		URL:        "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
	})
	require.NoError(t, err)

	// The original row, addressed by its own ID, must still say 'merged'.
	stillMerged, err := h.repo.GetByID(context.Background(), merged.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.VCSLinkStatusMerged, stillMerged.Status,
		"an implicit-status Create on a colliding link must not reset the existing row's status")
}

// ---------------------------------------------------------------------------
// Create: required-field validation. Each returns created=false alongside the
// error — nothing was persisted, so true would misreport the outcome.
// ---------------------------------------------------------------------------

func TestVCSLinkService_Create_RequiresTaskID(t *testing.T) {
	h := newHarness(t)
	link, created, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		LinkType: domain.VCSLinkTypePR, ExternalID: "1", URL: "https://github.com/o/r/pull/1",
	})
	require.Error(t, err)
	assert.Nil(t, link)
	assert.False(t, created)
}

func TestVCSLinkService_Create_RequiresURL(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	link, created, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID: task.ID, LinkType: domain.VCSLinkTypePR, ExternalID: "1",
	})
	require.Error(t, err)
	assert.Nil(t, link)
	assert.False(t, created)
}

func TestVCSLinkService_Create_RequiresExternalID(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	link, created, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID: task.ID, LinkType: domain.VCSLinkTypePR, URL: "https://github.com/o/r/pull/1",
	})
	require.Error(t, err)
	assert.Nil(t, link)
	assert.False(t, created)
}

func TestVCSLinkService_Create_RequiresLinkType(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	link, created, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID: task.ID, ExternalID: "1", URL: "https://github.com/o/r/pull/1",
	})
	require.Error(t, err)
	assert.Nil(t, link)
	assert.False(t, created)
}

// ---------------------------------------------------------------------------
// Create: repository failures on both the upsert and plain-insert branches
// must surface as a wrapped error and created=false, never a silent partial
// success.
// ---------------------------------------------------------------------------

func TestVCSLinkService_Create_UpsertRepoErrorIsWrapped(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	h.repo.upsertErr = errors.New("db unavailable")

	link, created, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID: task.ID, LinkType: domain.VCSLinkTypePR, ExternalID: "1",
		URL: "https://github.com/o/r/pull/1", Status: domain.VCSLinkStatusMerged,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db unavailable")
	assert.Nil(t, link)
	assert.False(t, created)
}

func TestVCSLinkService_Create_PlainInsertRepoErrorIsWrapped(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	h.repo.createErr = errors.New("db unavailable")

	link, created, err := h.svc.Create(context.Background(), domain.CreateVCSLinkInput{
		TaskID: task.ID, LinkType: domain.VCSLinkTypePR, ExternalID: "1",
		URL: "https://github.com/o/r/pull/1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db unavailable")
	assert.Nil(t, link)
	assert.False(t, created)
}

// The webhook orchestrator's own Upsert call (distinct from Create's) must
// also propagate a repo failure rather than silently reporting success.
func TestHandlePR_UpsertRepoError_Propagates(t *testing.T) {
	h := newHarness(t)
	task := h.makeTask(t, domain.StatusCategoryInProgress)
	h.repo.upsertErr = errors.New("db unavailable")

	ev := mergedClosedEvent(999, task.ID, "MESH-"+task.ID.String())
	_, err := h.svc.HandleGitHubPullRequestEvent(context.Background(), ev)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db unavailable")
}
