package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
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

func (r *fakeVCSLinkRepo) Upsert(_ context.Context, l *domain.VCSLink) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := conflictKey(l.TaskID, l.Provider, l.LinkType, l.ExternalID)
	if existingID, ok := r.upsertKey[key]; ok {
		existing := r.links[existingID]
		existing.Status = l.Status
		existing.Title = l.Title
		existing.Metadata = l.Metadata
		existing.URL = l.URL
		return nil
	}
	r.links[l.ID] = l
	r.upsertKey[key] = l.ID
	return nil
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
func (r *fakeTaskRepo) GetByShortID(context.Context, string) (*domain.Task, error) {
	return nil, nil
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
func (r *fakeTaskRepo) ListByAssignee(context.Context, uuid.UUID, domain.AssigneeType) ([]domain.Task, error) {
	return nil, nil
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
func (r *fakeTaskRepo) ForceReleaseCheckout(context.Context, uuid.UUID) error { return nil }
func (r *fakeTaskRepo) ListByUserActive(context.Context, uuid.UUID, uuid.UUID, pagination.Params) (*pagination.Page[domain.Task], error) {
	return nil, nil
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
func (t *fakeTaskService) GetMyTasks(context.Context, uuid.UUID, domain.AssigneeType) ([]domain.Task, error) {
	return nil, nil
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
	err := h.repo.Upsert(context.Background(), &domain.VCSLink{
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
	err := h.repo.Upsert(context.Background(), &domain.VCSLink{
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
	err := h.repo.Upsert(context.Background(), &domain.VCSLink{
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
