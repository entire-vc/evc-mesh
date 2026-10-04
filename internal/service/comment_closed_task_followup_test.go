package service

import (
	"context"
	"fmt"
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

// ---------------------------------------------------------------------------
// Closed-card follow-up (task #754173eb, audit 1.14).
//
// The task names four branches, and they are the four this file leads with:
//
//	чужой агент       → follow-up card opened
//	сам assignee      → no card
//	system / драйвер  → no card
//	карточка не закрыта → no card
//
// Three of those four assert an ABSENCE, which on their own would pass against
// a createClosedTaskFollowUp that does nothing at all. The first one is the
// positive control that rules that out, and it is deliberately the first test
// in the file rather than an afterthought: a suite of negative branches with no
// positive is not a test of a mechanism, it is a test that the mechanism is
// absent. (Verified by construction while writing: stubbing the mechanism's
// body out makes exactly the positive tests fail and the negative ones pass.)
// ---------------------------------------------------------------------------

// followUpTaskCreator is a TaskService double whose Create actually persists
// into the shared MockTaskRepository — the dedup path reads its own earlier
// output back through taskRepo, so a Create that only records the call would
// make the dedup test vacuously green.
type followUpTaskCreator struct {
	TaskService
	mu         sync.Mutex
	taskRepo   *MockTaskRepository
	statusRepo *MockTaskStatusRepository
	created    []*domain.Task
	err        error
	// moveErr fails MoveTask alone: the storm-budget test needs reopens that
	// fail AFTER the slot was taken while everything else stays healthy.
	moveErr error
}

func (f *followUpTaskCreator) Create(ctx context.Context, task *domain.Task) error {
	f.mu.Lock()
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if task.ID == uuid.Nil {
		task.ID = uuid.New()
	}
	task.CreatedAt = timeNow()
	task.UpdatedAt = timeNow()
	// Persist through the mock's own locked Create, never the raw map: the
	// concurrency test has loser goroutines reading this repo while the winner
	// writes, and a map write under f.mu races every reader holding m.mu.
	if err := f.taskRepo.Create(ctx, task, nil); err != nil {
		return err
	}
	f.mu.Lock()
	f.created = append(f.created, task)
	f.mu.Unlock()
	return nil
}

func (f *followUpTaskCreator) createdTasks() []*domain.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*domain.Task(nil), f.created...)
}

// MoveTask is the reopen half of the double (#5194afd4): the contract's
// closed-repeat branch returns the SAME root to todo through the task
// service's ordinary move path (activity log, position handling), not a raw
// repo write — so the double has to carry it too. It also mirrors the one
// guard the reopen contract depends on: a shipped task refuses any non-done
// destination (TaskShippedError), exactly like the real MoveTask — without
// the mirror, a reopen that forgot ShipTask(false) first would pass here and
// die only in production.
func (f *followUpTaskCreator) MoveTask(_ context.Context, taskID uuid.UUID, input MoveTaskInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if f.moveErr != nil {
		return f.moveErr
	}
	t, ok := f.taskRepo.items[taskID]
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}
	if input.StatusID != nil {
		st, ok := f.statusRepo.items[*input.StatusID]
		if ok && t.IsShipped && st.Category != domain.StatusCategoryDone {
			return &TaskShippedError{}
		}
		t.StatusID = *input.StatusID
	}
	return nil
}

// ShipTask is the shipped-root half of the reopen: the documented escape
// hatch is clearing the flag before the move, and the double applies it to
// the shared repo exactly as the real service would.
func (f *followUpTaskCreator) ShipTask(_ context.Context, taskID uuid.UUID, shipped bool) error {
	if f.err != nil {
		return f.err
	}
	return f.taskRepo.SetShipped(context.Background(), taskID, shipped)
}

type followUpEnv struct {
	svc          *commentService
	commentRepo  *MockCommentRepository
	taskRepo     *MockTaskRepository
	statusRepo   *MockTaskStatusRepository
	depRepo      *MockTaskDependencyRepository
	activityRepo *MockActivityLogRepository
	rootsRepo    *MockClosedFollowUpRootRepository
	pendingRepo  *MockClosedFollowUpPendingRepository
	taskSvc      *followUpTaskCreator
	projID       uuid.UUID
	wsID         uuid.UUID
	doneID       uuid.UUID
	todoID       uuid.UUID
	assignee     uuid.UUID
	sourceID     uuid.UUID
}

// setupFollowUpEnv builds a closed ("done") card assigned to an agent, in a
// project that has a todo column — i.e. the exact shape the mechanism fires on.
// Individual tests mutate one fact away from that shape to test one branch.
func setupFollowUpEnv(t *testing.T, opts ...func(*followUpEnv)) followUpEnv {
	t.Helper()
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	statusRepo := NewMockTaskStatusRepository()
	projectRepo := NewMockProjectRepository()
	depRepo := NewMockTaskDependencyRepository()
	rootsRepo := NewMockClosedFollowUpRootRepository()
	pendingRepo := NewMockClosedFollowUpPendingRepository()
	taskSvc := &followUpTaskCreator{taskRepo: taskRepo, statusRepo: statusRepo}

	wsID := uuid.New()
	projID := uuid.New()
	projectRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}

	todoID := uuid.New()
	statusRepo.items[todoID] = &domain.TaskStatus{
		ID: todoID, ProjectID: projID, Category: domain.StatusCategoryTodo, Name: "To Do",
	}
	doneID := uuid.New()
	statusRepo.items[doneID] = &domain.TaskStatus{
		ID: doneID, ProjectID: projID, Category: domain.StatusCategoryDone, Name: "Done",
	}

	timeNow = func() time.Time { return frozenTime }

	assignee := uuid.New()
	sourceID := uuid.New()
	taskRepo.items[sourceID] = &domain.Task{
		ID: sourceID, ProjectID: projID, StatusID: doneID,
		Title:        "Лендинг: секция цен",
		AssigneeID:   &assignee,
		AssigneeType: domain.AssigneeTypeAgent,
		Priority:     domain.PriorityHigh,
	}

	svc := NewCommentService(commentRepo, taskRepo, activityRepo,
		WithCommentProjectRepo(projectRepo),
		WithCommentStatusRepo(statusRepo),
		WithCommentTaskService(taskSvc),
		WithCommentDependencyRepo(depRepo),
		WithClosedFollowUpRootRepo(rootsRepo),
		WithClosedFollowUpPendingRepo(pendingRepo),
	).(*commentService)

	env := followUpEnv{
		svc: svc, commentRepo: commentRepo, taskRepo: taskRepo, statusRepo: statusRepo,
		depRepo: depRepo, activityRepo: activityRepo, rootsRepo: rootsRepo, pendingRepo: pendingRepo,
		taskSvc: taskSvc,
		projID:  projID, wsID: wsID, doneID: doneID, todoID: todoID,
		assignee: assignee, sourceID: sourceID,
	}
	for _, o := range opts {
		o(&env)
	}
	return env
}

// commentFrom posts body on the closed card as an agent that is NOT the
// assignee, unless the caller overrides author/type.
func (env followUpEnv) comment(t *testing.T, body string, mut ...func(*domain.Comment)) *domain.Comment {
	t.Helper()
	c := &domain.Comment{
		TaskID:     env.sourceID,
		AuthorID:   uuid.New(),
		AuthorType: domain.ActorTypeAgent,
		Body:       body,
	}
	for _, m := range mut {
		m(c)
	}
	require.NoError(t, env.svc.Create(context.Background(), c))
	return c
}

// systemNotices returns the system comments this mechanism posted back onto the
// closed SOURCE card, in a deterministic order (commentRepo.items is a map —
// raw iteration order would make "the last notice" a random pick).
func (env followUpEnv) systemNotices() []domain.Comment {
	return env.systemCommentsOn(env.sourceID)
}

// systemCommentsOn returns the system comments on one card, deterministic.
func (env followUpEnv) systemCommentsOn(taskID uuid.UUID) []domain.Comment {
	var out []domain.Comment
	for _, c := range env.commentRepo.items {
		if c.TaskID == taskID && c.AuthorType == domain.ActorTypeSystem {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out
}

// oneSystemCommentContaining asserts exactly one system comment on taskID
// contains needle — the multi-outcome shapes (create/reopen/storm all post)
// make "the last one" meaningless, count-matching is the honest assertion.
func (env followUpEnv) oneSystemCommentContaining(t *testing.T, taskID uuid.UUID, needle string) domain.Comment {
	t.Helper()
	var found []domain.Comment
	for _, c := range env.systemCommentsOn(taskID) {
		if strings.Contains(c.Body, needle) {
			found = append(found, c)
		}
	}
	require.Len(t, found, 1, "expected exactly one system comment on %s containing %q", taskID, needle)
	return found[0]
}

// --- Branch 1 (positive control): another agent's remark on a closed card ---

// This is the measured precedent, reproduced: an agent that is not the assignee
// writes concrete corrections into an already-closed card. Before this
// mechanism the comment published and reached nobody — Create suppresses
// task.commented on a terminal task, and the agent feed polls only todo.
func TestClosedFollowUp_ForeignAgentCommentOpensFollowUpCard(t *testing.T) {
	env := setupFollowUpEnv(t)

	env.comment(t, "Шапка не sticky, вес 600 вместо 400, ширина 1120 вместо 1240.")

	created := env.taskSvc.createdTasks()
	require.Len(t, created, 1, "a remark from a non-assignee on a closed card must open exactly one follow-up card")
	fu := created[0]

	assert.Equal(t, env.todoID, fu.StatusID, "follow-up must land in the todo column — the only one the agent feed polls")
	require.NotNil(t, fu.AssigneeID)
	assert.Equal(t, env.assignee, *fu.AssigneeID, "follow-up goes to the closed card's assignee")
	assert.Equal(t, domain.AssigneeTypeAgent, fu.AssigneeType)
	assert.Equal(t, env.projID, fu.ProjectID)
	assert.Contains(t, fu.Labels, followUpLabel)
	assert.Equal(t, domain.PriorityHigh, fu.Priority, "priority is inherited from the work the remark is about")

	assert.True(t, strings.HasPrefix(fu.Title, "Замечание к #"+shortTaskID(env.sourceID)+" — "),
		"title must name the source card, got %q", fu.Title)
	assert.Contains(t, fu.Description, "Шапка не sticky", "the remark itself must travel with the card")
	assert.Contains(t, fu.Description, shortTaskID(env.sourceID), "the card must point back at its source")
}

// The edge is relates_to and must NOT be blocks: a blocks edge onto a
// still-open task freezes the feed (CLAUDE-workflow.md §ROUTE-gate), which
// would make this mechanism silence the very card it just created.
func TestClosedFollowUp_EdgeIsRelatesToNotBlocks(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.comment(t, "Цвет заголовка не тот.")

	created := env.taskSvc.createdTasks()
	require.Len(t, created, 1)

	deps, err := env.depRepo.ListDependents(context.Background(), env.sourceID)
	require.NoError(t, err)
	require.Len(t, deps, 1, "follow-up must be linked back to the card it came from")
	assert.Equal(t, created[0].ID, deps[0].TaskID)
	assert.Equal(t, env.sourceID, deps[0].DependsOnTaskID)
	assert.Equal(t, domain.DependencyTypeRelatesTo, deps[0].DependencyType,
		"must be relates_to — a blocks edge would freeze the follow-up's own feed")
}

// The commenter has to learn what became of their remark, otherwise they are
// still writing into a void — just a void that now has a side effect.
func TestClosedFollowUp_PostsNoticeNamingTheNewCard(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.comment(t, "Ширина контейнера 1120 вместо 1240.")

	created := env.taskSvc.createdTasks()
	require.Len(t, created, 1)

	notices := env.systemNotices()
	require.Len(t, notices, 1, "exactly one notice back to the commenter")
	assert.Contains(t, notices[0].Body, shortTaskID(created[0].ID), "the notice must name the card it opened")
	assert.Contains(t, notices[0].Body, "закрытая карточка никого не будит")
	assert.Equal(t, env.sourceID, notices[0].TaskID, "the notice belongs on the card the remark was written on")
}

// --- Branch 2: the assignee's own comment on their own closed card ---------

func TestClosedFollowUp_AssigneeOwnCommentCreatesNothing(t *testing.T) {
	env := setupFollowUpEnv(t)

	env.comment(t, "Пост-мортем: причина была в порядке миграций.", func(c *domain.Comment) {
		c.AuthorID = env.assignee
		c.AuthorType = domain.ActorTypeAgent
	})

	assert.Empty(t, env.taskSvc.createdTasks(),
		"routing an assignee's own remark back to themselves is a loop, not a delivery")
	assert.Empty(t, env.systemNotices())
}

// --- Branch 3: system and driver comments ---------------------------------

func TestClosedFollowUp_SystemCommentCreatesNothing(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.comment(t, "Задача закрыта по таймауту.", func(c *domain.Comment) {
		c.AuthorType = domain.ActorTypeSystem
		c.AuthorID = systemActorID
	})
	assert.Empty(t, env.taskSvc.createdTasks())
}

func TestClosedFollowUp_DriverPrefixedCommentCreatesNothing(t *testing.T) {
	for _, body := range []string{
		"🤖 Auto: PR #123 merged (commit `abc1234`) → moved to done.",
		"[fiddler] задача подана в сессию",
		"🔄 Checkout TTL истёк — задача возвращена в todo.",
		"Verdict: DO-NOT-SHIP — PR не смёржен.",
		"╔══ MESH DONE GATE ══",
	} {
		t.Run(body[:min(len(body), 24)], func(t *testing.T) {
			env := setupFollowUpEnv(t)
			env.comment(t, body)
			assert.Empty(t, env.taskSvc.createdTasks(),
				"a driver's own line must not open a card; got one for %q", body)
		})
	}
}

// A driver may also declare itself in metadata rather than in the body. The
// label is cooperative and is honoured only to SUPPRESS a side effect on the
// declarer — never to grant anything — so a lie costs the liar their own card
// and nothing else.
func TestClosedFollowUp_DriverMetadataSourceCreatesNothing(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.comment(t, "нужен ребейз", func(c *domain.Comment) {
		c.Metadata = []byte(`{"source":"pr-driver"}`)
	})
	assert.Empty(t, env.taskSvc.createdTasks())
}

// Negative control for the driver check, and the reason it matches a PREFIX of
// the first line rather than a substring of the body: a person writing ABOUT a
// driver is exactly the remark this mechanism exists to deliver. A substring
// match would drop it silently — the failure would look identical to "no remark
// was written".
func TestClosedFollowUp_HumanQuotingADriverStillOpensACard(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.comment(t, "Поезд ответил «Verdict: DO-NOT-SHIP», но по-моему он не прав — посмотри сам.")
	assert.Len(t, env.taskSvc.createdTasks(), 1,
		"a human quoting a driver's verdict is a real remark, not a driver's line")
}

// --- Branch 4: the card is not closed --------------------------------------

func TestClosedFollowUp_OpenCardCreatesNothing(t *testing.T) {
	env := setupFollowUpEnv(t)
	inProgID := uuid.New()
	env.statusRepo.items[inProgID] = &domain.TaskStatus{
		ID: inProgID, ProjectID: env.projID, Category: domain.StatusCategoryInProgress, Name: "In Progress",
	}
	env.taskRepo.items[env.sourceID].StatusID = inProgID

	env.comment(t, "Шапка не sticky.")

	assert.Empty(t, env.taskSvc.createdTasks(),
		"an open card already wakes its assignee through task.commented — a second card would duplicate a channel that works")
	assert.Empty(t, env.systemNotices())
}

// cancelled is terminal for the same reason done is: nothing polls it.
func TestClosedFollowUp_CancelledCardAlsoRoutes(t *testing.T) {
	env := setupFollowUpEnv(t)
	cancelledID := uuid.New()
	env.statusRepo.items[cancelledID] = &domain.TaskStatus{
		ID: cancelledID, ProjectID: env.projID, Category: domain.StatusCategoryCancelled, Name: "Cancelled",
	}
	env.taskRepo.items[env.sourceID].StatusID = cancelledID

	env.comment(t, "Отменили зря — вот почему.")

	assert.Len(t, env.taskSvc.createdTasks(), 1)
}

// --- Identity, dedup and reopen (#5194afd4) ----------------------------------
//
// The old label-scan dedup pinned "a burst of remarks → one card", whatever
// the texts. The approved contract replaces that premise: a finding is the
// pair (source card, finding key) — same text/finding_id → same root for its
// whole life; a DIFFERENT text is a different finding and must get its own
// card even while another finding's root is open. The tests below pin both
// sides of that split.

// The same finding twice while its root is still open: one card, and the
// second commenter is told the card already exists. This is the measured
// precedent (two remarks the same morning) preserved under the new identity.
func TestClosedFollowUp_SecondSameFindingReusesTheOpenFollowUp(t *testing.T) {
	env := setupFollowUpEnv(t)
	withID := func(c *domain.Comment) { c.Metadata = []byte(`{"finding_id":"landing/sticky-header"}`) }

	env.comment(t, "Шапка не sticky.", withID)
	env.comment(t, "Шапка не sticky, повторяю.", withID)

	created := env.taskSvc.createdTasks()
	require.Len(t, created, 1, "the same finding while its root is open must not open a second card")
	notices := env.systemNotices()
	require.Len(t, notices, 2, "but every commenter still learns where their remark went")
	var reused int
	for _, n := range notices {
		if strings.Contains(n.Body, "уже открыта") {
			reused++
		}
	}
	assert.Equal(t, 1, reused, "the second notice must say the card already exists, not claim a new one")
}

// Once the follow-up root is closed, a remark with a DIFFERENT text is a
// DIFFERENT finding: it opens its own root. An open (or closed) root of one
// finding never absorbs another finding — that absorption is the defect the
// identity store replaces.
func TestClosedFollowUp_DifferentFindingOpensItsOwnRoot(t *testing.T) {
	env := setupFollowUpEnv(t)

	env.comment(t, "Шапка не sticky.")
	env.comment(t, "И ширина 1120 вместо 1240.") // different text, no finding_id → different key

	created := env.taskSvc.createdTasks()
	require.Len(t, created, 2,
		"a different finding must get its own card even while another finding's root is open")
	assert.NotEqual(t, created[0].ID, created[1].ID)
	assert.Len(t, env.rootsRepo.items, 2, "two findings → two identity rows")
}

// RED test, written and run against the pre-fix code BEFORE implementing the
// #5194afd4 contract (P5 step 1). A repeat of the SAME finding after its root
// was closed must reopen THAT root — the same card back in todo, not a second
// card. Against the pre-fix code this fails with createdTasks=2, which is the
// measured defect (#9b712414 repro: f0d4539b closed → same text → 06b275a8).
func TestClosedFollowUp_ClosedRepeatReopensTheSameRoot(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.comment(t, "Шапка не sticky.")
	first := env.taskSvc.createdTasks()
	require.Len(t, first, 1)
	root := first[0]
	root.StatusID = env.doneID // the assignee handled it and closed it

	env.comment(t, "Шапка не sticky.") // the same finding, again

	created := env.taskSvc.createdTasks()
	require.Len(t, created, 1,
		"a closed repeat of the same finding must reopen the same root, not open a second card")
	assert.Equal(t, root.ID, created[0].ID)
	assert.Equal(t, env.todoID, env.taskRepo.items[root.ID].StatusID,
		"the root must be back in todo — the only column the agent feed polls")

	// The reopen is counted and attributed: the counter feeds the storm limit,
	// and the repeat itself must be ON the root (with a link back to the
	// source comment), so the reopened card opens with the new remark on it.
	row, err := env.rootsRepo.Get(context.Background(), env.sourceID, "txt:"+sha256Hex("шапка не sticky."))
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 1, row.ReopenCount, "one reopen must be counted against the 24h window")
	require.NotNil(t, row.LastReopenedAt)

	onRoot := env.systemCommentsOn(root.ID)
	require.Len(t, onRoot, 1, "exactly one system comment on the reopened root")
	assert.Contains(t, onRoot[0].Body, "Шапка не sticky.", "the repeat body must travel onto the root")
	assert.Contains(t, onRoot[0].Body, "переоткрыта")

	notices := env.systemNotices()
	require.Len(t, notices, 2, "created + reopened notices to the two commenters")
	env.oneSystemCommentContaining(t, env.sourceID, "переоткрыта")
}

// The assignee survives the reopen untouched: the card comes back to the same
// person who owned the work, which is the whole point of reopening instead of
// opening a duplicate.
func TestClosedFollowUp_ReopenKeepsAssigneeAndProject(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.comment(t, "Шапка не sticky.")
	root := env.taskSvc.createdTasks()[0]
	root.StatusID = env.doneID

	env.comment(t, "Шапка не sticky.")

	fresh := env.taskRepo.items[root.ID]
	require.NotNil(t, fresh)
	assert.Equal(t, env.assignee, *fresh.AssigneeID, "the reopen must not reassign the root")
	assert.Equal(t, env.projID, fresh.ProjectID)
}

// Explicit identity beats text: a detector stamping finding_id groups its own
// repeats even when every body is worded differently.
func TestClosedFollowUp_SameFindingIDDifferentTextsIsOneRoot(t *testing.T) {
	env := setupFollowUpEnv(t)
	withID := func(c *domain.Comment) {
		c.Metadata = []byte(`{"finding_id":"audit-1.14/sticky","finding_ns":"mesh-audit"}`)
	}

	env.comment(t, "Шапка не sticky.", withID)
	env.comment(t, "Вернулось: шапка снова не держится при скролле.", withID)

	created := env.taskSvc.createdTasks()
	require.Len(t, created, 1, "same finding_id → same root, whatever the bodies say")
	assert.Len(t, env.rootsRepo.items, 1)
	row := env.rootsRepo.items[closedFollowUpRootKey(env.sourceID, "id:mesh-audit:audit-1.14/sticky")]
	require.NotNil(t, row, "the key must be the explicit id:<ns>:<id> identity")
	assert.Equal(t, created[0].ID, row.RootTaskID)
}

// The legacy text key normalizes exactly trim + whitespace-collapse + lower,
// and nothing more: a cosmetic retyping is the same finding, a reworded remark
// is not (and must be delivered as its own).
func TestClosedFollowUp_LegacyKeyNormalizesCaseAndWhitespaceOnly(t *testing.T) {
	env := setupFollowUpEnv(t)

	env.comment(t, "Шапка не sticky.")
	env.comment(t, "  ШАПКА\t не \n  sticky.  ")

	created := env.taskSvc.createdTasks()
	require.Len(t, created, 1, "case and whitespace are not findings of their own")
	notices := env.systemNotices()
	require.Len(t, notices, 2)
	env.oneSystemCommentContaining(t, env.sourceID, "уже открыта")
}

// --- The 24h storm limit -----------------------------------------------------

// closeRootAndRepeat is one close/reopen cycle of the same finding: the
// assignee closes the root, the same finding arrives again.
func (env followUpEnv) closeRootAndRepeat(t *testing.T, root *domain.Task, body string) {
	t.Helper()
	root.StatusID = env.doneID
	env.comment(t, body)
}

// Three reopens inside 24h are the allowed budget; the FOURTH repeat of the
// same finding must not wake anyone again. Nothing is swallowed: the repeat
// lands as a comment on the root, the commenter gets a notice naming the
// limit, and the root stays closed.
func TestClosedFollowUp_FourthReopenIn24hIsStormLimited(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.comment(t, "Шапка не sticky.")
	root := env.taskSvc.createdTasks()[0]

	for i := 1; i <= 3; i++ {
		env.closeRootAndRepeat(t, root, "Шапка не sticky.")
		require.Equal(t, env.todoID, env.taskRepo.items[root.ID].StatusID,
			"reopen #%d must go through (budget %d)", i, followUpReopenWindowLimit)
	}

	// The 4th repeat inside the same window: no reopen, but full delivery.
	root.StatusID = env.doneID
	before := len(env.taskSvc.createdTasks())
	env.comment(t, "Шапка не sticky.")

	assert.Len(t, env.taskSvc.createdTasks(), before, "no new card on a storm-limited repeat")
	assert.Equal(t, env.doneID, env.taskRepo.items[root.ID].StatusID,
		"the 4th reopen inside 24h must not move the root — that is the storm")

	stormComment := env.oneSystemCommentContaining(t, root.ID, "НЕ переоткрыта")
	assert.Contains(t, stormComment.Body, "Шапка не sticky.",
		"the repeat itself must still land on the root as a comment")
	env.oneSystemCommentContaining(t, env.sourceID, "не переоткрыта")
}

// The limit is a window, not a lifetime lock: after 24h without a reopen the
// same finding can wake its root again. A permanent lock would trade a storm
// of cards for a silently dead finding — the defect this file exists to fix.
func TestClosedFollowUp_StormWindowExpiryAllowsReopenAgain(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.comment(t, "Шапка не sticky.")
	root := env.taskSvc.createdTasks()[0]

	for i := 1; i <= 3; i++ {
		env.closeRootAndRepeat(t, root, "Шапка не sticky.")
	}
	root.StatusID = env.doneID
	env.comment(t, "Шапка не sticky.") // 4th inside the window → limited
	require.Equal(t, env.doneID, env.taskRepo.items[root.ID].StatusID, "sanity: storm limited")

	// 25 hours later (the window anchored at the last REOPEN, not at the
	// limited repeat), the same finding reopens the root again.
	timeNow = func() time.Time { return frozenTime.Add(followUpReopenWindow + time.Hour) }
	defer func() { timeNow = func() time.Time { return frozenTime } }()

	env.comment(t, "Шапка не sticky.")

	assert.Equal(t, env.todoID, env.taskRepo.items[root.ID].StatusID,
		"after the 24h window goes quiet the finding must be able to wake its root again")
	row, err := env.rootsRepo.Get(context.Background(), env.sourceID, "txt:"+sha256Hex("шапка не sticky."))
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 1, row.ReopenCount, "the expired window resets, it does not grow a lifetime total")
}

// --- Concurrency and failure compensation ------------------------------------

// N concurrent remarks carrying the same finding_id: exactly one root card.
// The claim is INSERT ... ON CONFLICT DO NOTHING BEFORE the card is created,
// so the race cannot produce two cards; the losers read the winner's root and
// take the repeat branch — waiting for the card if the claim is ahead of it,
// taking the creation over if the winner released its claim (both raced
// branches have dedicated tests above).
func TestClosedFollowUp_ConcurrentClaimsProduceExactlyOneRoot(t *testing.T) {
	env := setupFollowUpEnv(t)
	const n = 16

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := &domain.Comment{
				TaskID:     env.sourceID,
				AuthorID:   uuid.New(),
				AuthorType: domain.ActorTypeAgent,
				Body:       fmt.Sprintf("Замечание #%d", i),
				Metadata:   []byte(`{"finding_id":"race/one-root"}`),
			}
			_ = env.svc.Create(context.Background(), c)
		}(i)
	}
	wg.Wait()

	created := env.taskSvc.createdTasks()
	require.Len(t, created, 1, "the claim must be atomic: exactly one card for one finding_id, got %d", len(created))
	require.Len(t, env.rootsRepo.items, 1)
	row := env.rootsRepo.items[closedFollowUpRootKey(env.sourceID, "id::race/one-root")]
	require.NotNil(t, row)
	assert.Equal(t, created[0].ID, row.RootTaskID, "the identity row must point at the card that exists")
}

// A card create failure must not leave a phantom claim: the next repeat of the
// finding claims cleanly and opens its card, instead of being routed to a root
// that does not exist.
func TestClosedFollowUp_CreateFailureReleasesTheClaim(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.taskSvc.err = assert.AnError

	env.comment(t, "Шапка не sticky.")
	assert.Empty(t, env.taskSvc.createdTasks())
	assert.Empty(t, env.rootsRepo.items, "the claim must be compensated away when no card came to exist")

	env.taskSvc.err = nil
	env.comment(t, "Шапка не sticky.")
	assert.Len(t, env.taskSvc.createdTasks(), 1, "after compensation the finding claims cleanly")
}

// The identity store failing means NO card, not an orphan card outside the
// store (which would be invisible to every later repeat of the finding — the
// duplicate defect back again, just delayed). The remark itself survives.
func TestClosedFollowUp_ClaimFailureCreatesNoCardAndKeepsTheComment(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.rootsRepo.errToReturn = assert.AnError

	c := &domain.Comment{
		TaskID: env.sourceID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Body: "Шапка не sticky.",
	}
	require.NoError(t, env.svc.Create(context.Background(), c),
		"an identity-store failure must not take the comment down with it")
	assert.Empty(t, env.taskSvc.createdTasks())
}

// stubRootRetrySleep collapses waitForRootCard's backoff for one test: the
// wait DECISIONS are under test, not the wall clock.
func stubRootRetrySleep(t *testing.T) {
	t.Helper()
	prev := followUpRootRetrySleep
	followUpRootRetrySleep = func(time.Duration) {}
	t.Cleanup(func() { followUpRootRetrySleep = prev })
}

// --- The claim/card visibility race (codex-review P1 on MR !1078) ----------
//
// The claim row is visible before the winner's CARD is. A loser reading the
// store in that window must WAIT for the card, not drop the repeat — the
// source is closed, so a dropped repeat reaches nobody at all. RED against
// the pre-fix single lookup, which logged and returned.

func TestClosedFollowUp_LoserWaitsForTheWinnersCard(t *testing.T) {
	env := setupFollowUpEnv(t)
	stubRootRetrySleep(t)

	key := "txt:" + sha256Hex("шапка не sticky.")
	winnerRootID := uuid.New()
	_, err := env.rootsRepo.Claim(context.Background(), env.sourceID, key, winnerRootID, frozenTime)
	require.NoError(t, err)
	// The winner's card exists but shows up only on the third lookup: the
	// first two model the winner sitting between its claim and its INSERT.
	env.taskRepo.items[winnerRootID] = &domain.Task{
		ID: winnerRootID, ProjectID: env.projID, StatusID: env.todoID,
		Title: "Замечание (root)", AssigneeID: &env.assignee, AssigneeType: domain.AssigneeTypeAgent,
	}
	env.taskRepo.DeferGetByID(winnerRootID, 2)

	env.comment(t, "Шапка не sticky.")

	assert.Empty(t, env.taskSvc.createdTasks(), "the loser must not open a second card")
	env.oneSystemCommentContaining(t, env.sourceID, "уже открыта")
}

// The winner released its claim (no todo column / failed create) exactly
// while the loser was waiting: the finding has no root and no owner, so the
// loser takes the creation over — once, never recursively.
func TestClosedFollowUp_ClaimReleasedWhileWaitingIsTakenOver(t *testing.T) {
	env := setupFollowUpEnv(t)
	stubRootRetrySleep(t)

	key := "txt:" + sha256Hex("шапка не sticky.")
	deadRootID := uuid.New()
	_, err := env.rootsRepo.Claim(context.Background(), env.sourceID, key, deadRootID, frozenTime)
	require.NoError(t, err)
	// Get call 2 is the re-read after the wait budget: the "winner's"
	// compensation lands there. No card with deadRootID ever exists.
	env.rootsRepo.onGet = func(call int, m *MockClosedFollowUpRootRepository) {
		if call == 2 {
			_ = m.Delete(context.Background(), env.sourceID, key)
		}
	}

	env.comment(t, "Шапка не sticky.")

	created := env.taskSvc.createdTasks()
	require.Len(t, created, 1, "a released claim must be taken over — the finding still needs its root")
	assert.NotEqual(t, deadRootID, created[0].ID, "the takeover must not resurrect the released root ID")
	row, err := env.rootsRepo.Get(context.Background(), env.sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, created[0].ID, row.RootTaskID, "the takeover's card and claim must agree")
	env.oneSystemCommentContaining(t, env.sourceID, "заведена")
}

// A wedged winner — claim present, card never appearing past the whole wait
// budget — must not swallow the repeat: no second card either, but a visible
// deferral notice on the source, and the next repeat re-enters delivery.
func TestClosedFollowUp_WedgedWinnerDefersTheRepeatVisibly(t *testing.T) {
	env := setupFollowUpEnv(t)
	stubRootRetrySleep(t)

	key := "txt:" + sha256Hex("шапка не sticky.")
	_, err := env.rootsRepo.Claim(context.Background(), env.sourceID, key, uuid.New(), frozenTime)
	require.NoError(t, err)
	// No card, no release: the winner sits between claim and create forever.

	env.comment(t, "Шапка не sticky.")

	assert.Empty(t, env.taskSvc.createdTasks(), "a wedged claim must not be answered with a second card")
	env.oneSystemCommentContaining(t, env.sourceID, "пока не доставлен")
	assert.Len(t, env.rootsRepo.items, 1, "the wedged claim stays — the next repeat retries against it")
}

// --- The shipped-root reopen (codex-review P2 on MR !1078) -----------------

// A SHIPPED root refuses MoveTask to todo (TaskShippedError is the guard);
// the reopen goes through the guard's own escape hatch — clear the flag
// first — because a repeat of the same finding is the disproof of "fix
// verified live". RED against a reopen that moves without clearing.
func TestClosedFollowUp_ReopenClearsTheShippedFlag(t *testing.T) {
	env := setupFollowUpEnv(t)

	env.comment(t, "Шапка не sticky.")
	roots := env.taskSvc.createdTasks()
	require.Len(t, roots, 1)
	roots[0].StatusID = env.doneID
	roots[0].IsShipped = true // fixed, merged AND verified live — and it came back

	env.comment(t, "Шапка не sticky.")

	created := env.taskSvc.createdTasks()
	require.Len(t, created, 1, "the same finding reopens its root even when the root was shipped")
	fresh := env.taskRepo.items[roots[0].ID]
	require.NotNil(t, fresh)
	assert.Equal(t, env.todoID, fresh.StatusID, "the shipped root must be back in todo")
	assert.False(t, fresh.IsShipped, "the reopen costs the shipped flag — the fix is no longer verified live")
}

// The shipped flag is cleared BEFORE the move, so a move that then fails must
// put it back (codex-review P2, MR !1078, round 12): a failed reopen changes
// nothing about the closed root, and "fix verified live" was not disproved by
// a repeat that was never delivered. RED against a failure path that only
// returns the storm slot.
func TestClosedFollowUp_FailedReopenRestoresTheShippedFlag(t *testing.T) {
	env := setupFollowUpEnv(t)

	env.comment(t, "Шапка не sticky.")
	roots := env.taskSvc.createdTasks()
	require.Len(t, roots, 1)
	roots[0].StatusID = env.doneID
	roots[0].IsShipped = true

	env.taskSvc.moveErr = assert.AnError
	env.comment(t, "Шапка не sticky.")
	env.taskSvc.moveErr = nil

	fresh := env.taskRepo.items[roots[0].ID]
	require.NotNil(t, fresh)
	assert.Equal(t, env.doneID, fresh.StatusID, "the failed move leaves the root closed")
	assert.True(t, fresh.IsShipped, "a reopen that failed must not cost the shipped flag")
}

// The storm budget counts DELIVERED reopens, not attempts (codex-review P2
// on MR !1078, round 4). TryReopen takes its slot BEFORE the move that can
// fail; without compensation three failed reopens burn the whole 24h window
// with nothing delivered and the fourth — healthy this time — is
// storm-limited. RED against the pre-compensation code: the fourth comment
// leaves the root closed.
func TestClosedFollowUp_FailedReopenDoesNotConsumeTheStormBudget(t *testing.T) {
	env := setupFollowUpEnv(t)

	env.comment(t, "Шапка не sticky.")
	roots := env.taskSvc.createdTasks()
	require.Len(t, roots, 1)
	roots[0].StatusID = env.doneID

	for i := 0; i < followUpReopenWindowLimit; i++ {
		env.taskSvc.moveErr = assert.AnError
		env.comment(t, "Шапка не sticky.")
		env.taskSvc.moveErr = nil
		root := env.taskRepo.items[roots[0].ID]
		require.Equal(t, env.doneID, root.StatusID,
			"attempt %d: the move refused, the root must stay closed", i+1)
	}

	env.comment(t, "Шапка не sticky.") // healthy, and the window must be empty
	fresh := env.taskRepo.items[roots[0].ID]
	assert.Equal(t, env.todoID, fresh.StatusID,
		"failed reopens must not exhaust the storm window")
}

// The compensation returns the slot WHOLE — the window anchor with the count
// (codex-review P2, MR !1078, round 5). Restoring the count while leaving the
// anchor at the failed attempt's instant stretches the window over reopens
// that never delivered: a failed attempt near the window's end would
// storm-limit a healthy repeat a day later. The anchor must return to the
// last DELIVERED reopen.
func TestClosedFollowUp_FailedReopenDoesNotStretchTheWindow(t *testing.T) {
	env := setupFollowUpEnv(t)
	key := "txt:" + sha256Hex("шапка не sticky.")
	defer func() { timeNow = func() time.Time { return frozenTime } }()

	env.comment(t, "Шапка не sticky.") // t0 — the root
	root := env.taskSvc.createdTasks()[0]

	// Reopens delivered at t0 and t0+1h: count 2, anchor t0+1h.
	root.StatusID = env.doneID
	env.comment(t, "Шапка не sticky.")
	timeNow = func() time.Time { return frozenTime.Add(time.Hour) }
	root.StatusID = env.doneID
	env.comment(t, "Шапка не sticky.")
	require.Equal(t, env.todoID, env.taskRepo.items[root.ID].StatusID, "reopen #2 delivered")

	// A failed attempt at t0+23h: the slot is taken, the move refuses, the
	// slot must come back with its anchor.
	timeNow = func() time.Time { return frozenTime.Add(23 * time.Hour) }
	root.StatusID = env.doneID
	env.taskSvc.moveErr = assert.AnError
	env.comment(t, "Шапка не sticky.")
	env.taskSvc.moveErr = nil
	require.Equal(t, env.doneID, env.taskRepo.items[root.ID].StatusID,
		"the failed attempt leaves the root closed")

	row, err := env.rootsRepo.Get(context.Background(), env.sourceID, key)
	require.NoError(t, err)
	require.NotNil(t, row)
	assert.Equal(t, 2, row.ReopenCount, "the failed attempt's slot is returned")
	require.NotNil(t, row.LastReopenedAt)
	assert.True(t, row.LastReopenedAt.Equal(frozenTime.Add(time.Hour)),
		"the anchor must return to the last DELIVERED reopen, not stay at the failed attempt")

	// A day after the last delivered reopen the window is fresh: two healthy
	// repeats in a row must BOTH go through — a window stretched by the failed
	// attempt would count them 3 and 4 and refuse the second.
	timeNow = func() time.Time { return frozenTime.Add(26 * time.Hour) }
	root.StatusID = env.doneID
	env.comment(t, "Шапка не sticky.")
	require.Equal(t, env.todoID, env.taskRepo.items[root.ID].StatusID, "fresh window: reopen goes through")

	timeNow = func() time.Time { return frozenTime.Add(26*time.Hour + 10*time.Minute) }
	root.StatusID = env.doneID
	env.comment(t, "Шапка не sticky.")
	assert.Equal(t, env.todoID, env.taskRepo.items[root.ID].StatusID,
		"a failed attempt must not stretch the window over reopens never delivered")
}

// The displaced-claim corner of the storm budget (codex-review P2, MR !1078,
// round 10): two repeats of one finding take slots close together, the later
// claim displaces the earlier claim's pin, and the EARLIER reopen's move then
// fails. Its compensation must still return its slot — the budget counts
// delivered reopens, never attempts (round 4) — or overlapping failures eat
// the window from under later healthy repeats. RED against single-token
// compensation semantics: with the phantom slot only one more repeat delivers
// where two must.
func TestClosedFollowUp_DisplacedFailedReopenDoesNotBurnTheStormBudget(t *testing.T) {
	env := setupFollowUpEnv(t)
	key := "txt:" + sha256Hex("шапка не sticky.")
	ctx := context.Background()

	env.comment(t, "Шапка не sticky.") // the root
	root := env.taskSvc.createdTasks()[0]
	root.StatusID = env.doneID

	// Two near-simultaneous repeats take two slots; the later claim displaces
	// the earlier one's pin — the exact interleave the single pin cannot
	// represent.
	okA, claimA, err := env.rootsRepo.TryReopen(ctx, env.sourceID, key, timeNow())
	require.NoError(t, err)
	require.True(t, okA)
	okB, _, err := env.rootsRepo.TryReopen(ctx, env.sourceID, key, timeNow().Add(time.Second))
	require.NoError(t, err)
	require.True(t, okB)
	// A's reopen fails after B claimed (B delivers); this is the exact call
	// compensateFailedReopen makes on every failure exit.
	require.NoError(t, env.rootsRepo.CompensateReopen(ctx, env.sourceID, key, claimA))

	// Budget spent: 1 (B's delivered reopen). Two healthy repeats must BOTH
	// deliver inside the limit of 3 — a phantom A-slot refuses the second.
	for i := 0; i < 2; i++ {
		root.StatusID = env.doneID
		env.comment(t, "Шапка не sticky.")
		require.Equal(t, env.todoID, env.taskRepo.items[root.ID].StatusID,
			"healthy repeat %d: the displaced failure must not have burned its slot", i+1)
	}
}

// A human assignee already has a real notification channel for comment.created
// (in-app / push / email / Telegram). This mechanism exists because the agent
// feed has none — manufacturing a card for someone already told is noise.
func TestClosedFollowUp_HumanAssigneeIsNotRouted(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.taskRepo.items[env.sourceID].AssigneeType = domain.AssigneeTypeUser

	env.comment(t, "Шапка не sticky.")

	assert.Empty(t, env.taskSvc.createdTasks())
}

func TestClosedFollowUp_UnassignedClosedCardCreatesNothing(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.taskRepo.items[env.sourceID].AssigneeID = nil
	env.taskRepo.items[env.sourceID].AssigneeType = domain.AssigneeTypeUnassigned

	env.comment(t, "Шапка не sticky.")

	assert.Empty(t, env.taskSvc.createdTasks(), "there is nobody to route to")
}

// A project with no todo column has no status the agent feed polls, so there is
// no card we could create that would wake anyone. Refusing to guess a column is
// the point — parking it in whatever happens to be first would look like a
// delivery and be none.
func TestClosedFollowUp_ProjectWithoutTodoColumnCreatesNothing(t *testing.T) {
	env := setupFollowUpEnv(t)
	delete(env.statusRepo.items, env.todoID)

	env.comment(t, "Шапка не sticky.")

	assert.Empty(t, env.taskSvc.createdTasks())
}

func TestClosedFollowUp_KillSwitchDisablesTheMechanism(t *testing.T) {
	t.Setenv(closedFollowUpDisableEnv, "1")
	env := setupFollowUpEnv(t)

	env.comment(t, "Шапка не sticky.")

	assert.Empty(t, env.taskSvc.createdTasks())
	assert.Empty(t, env.systemNotices())
}

// The remark must survive a failure to route it. This mechanism routes; it
// never rejects.
func TestClosedFollowUp_CommentSurvivesFollowUpCreateFailure(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.taskSvc.err = assert.AnError

	c := &domain.Comment{
		TaskID: env.sourceID, AuthorID: uuid.New(), AuthorType: domain.ActorTypeAgent,
		Body: "Шапка не sticky.",
	}
	require.NoError(t, env.svc.Create(context.Background(), c),
		"a follow-up that could not be created must not take the comment down with it")
	assert.NotEmpty(t, env.commentRepo.items, "the comment itself must still be persisted")
}

// --- Title excerpt ---------------------------------------------------------

// The key format is a wire contract with every future repeat of the finding:
// change it and past rows stop matching. Pinned here on its own, separate from
// behaviour, so a format change fails THIS table rather than a behaviour test
// someone misreads as "the dedup broke".
func TestClosedFindingKey_Format(t *testing.T) {
	withMeta := func(raw string, body string) *domain.Comment {
		return &domain.Comment{Body: body, Metadata: []byte(raw)}
	}
	cases := []struct {
		name    string
		comment *domain.Comment
		want    string
	}{
		{"explicit id with ns", withMeta(`{"finding_id":"a-1","finding_ns":"audit"}`, "любой текст"), "id:audit:a-1"},
		{"explicit id without ns", withMeta(`{"finding_id":"a-1"}`, "любой текст"), "id::a-1"},
		{"finding_id must be a string", withMeta(`{"finding_id":42}`, "текст"), "txt:" + sha256Hex("текст")},
		{"empty finding_id falls back", withMeta(`{"finding_id":"  "}`, "текст"), "txt:" + sha256Hex("текст")},
		{"oversized finding_id falls back", withMeta(`{"finding_id":"`+strings.Repeat("x", 129)+`"}`, "текст"), "txt:" + sha256Hex("текст")},
		{"legacy text is normalized", &domain.Comment{Body: "  А\tБ \n В  "}, "txt:" + sha256Hex("а б в")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, closedFindingKey(tc.comment))
		})
	}
}

// The "id:<ns>:<id>" join must be injective (codex-review P1, MR !1078,
// round 7): ':' is the delimiter, so two DIFFERENT (ns, id) pairs that spell
// the same joined string would merge distinct findings into one root. The
// doc on findingIdentityFromMetadata promises a collision "only ever merges
// comments carrying the SAME id" — this is the test that keeps that promise
// true. RED against an unescaped join.
func TestClosedFindingKey_ExplicitIdentityIsInjective(t *testing.T) {
	withMeta := func(raw string) *domain.Comment {
		return &domain.Comment{Body: "любой текст", Metadata: []byte(raw)}
	}
	// The reviewer's pair: under a raw ':' join both spell "id:a:b:c".
	assert.NotEqual(t,
		closedFindingKey(withMeta(`{"finding_ns":"a:b","finding_id":"c"}`)),
		closedFindingKey(withMeta(`{"finding_ns":"a","finding_id":"b:c"}`)),
		"distinct (ns, id) pairs must never serialize to one key")

	// Longer shapes collide the same way.
	assert.NotEqual(t,
		closedFindingKey(withMeta(`{"finding_ns":"x:y","finding_id":"z:w"}`)),
		closedFindingKey(withMeta(`{"finding_ns":"x","finding_id":"y:z:w"}`)),
		"any delimiter split must be recoverable from the key alone")

	// A literal backslash in a component must not fake an escape sequence
	// and collide with a genuinely escaped one.
	assert.NotEqual(t,
		closedFindingKey(withMeta(`{"finding_ns":"a","finding_id":"b\\:c"}`)),
		closedFindingKey(withMeta(`{"finding_ns":"a:b","finding_id":"c"}`)),
		"escaped and raw components must stay distinguishable")
}

// Whitespace is part of an explicit identity (codex-review P1, MR !1078,
// round 9). The contract defines the identity by the metadata values as
// written — nothing normalizes them — so " x" and "x" are different finding
// ids and must not share a root. Trimming the value before the join merged
// them exactly the way the raw-':' join merged (ns, id) pairs in round 7.
// RED against a trimming implementation.
func TestClosedFollowUp_WhitespaceIsPartOfTheFindingIdentity(t *testing.T) {
	env := setupFollowUpEnv(t)

	env.comment(t, "Первое замечание.", func(c *domain.Comment) {
		c.Metadata = []byte(`{"finding_id":"x"}`)
	})
	env.comment(t, "Второе замечание.", func(c *domain.Comment) {
		c.Metadata = []byte(`{"finding_id":" x"}`)
	})
	env.comment(t, "Третье замечание.", func(c *domain.Comment) {
		c.Metadata = []byte(`{"finding_ns":"a","finding_id":"y"}`)
	})
	env.comment(t, "Четвёртое замечание.", func(c *domain.Comment) {
		c.Metadata = []byte(`{"finding_ns":" a","finding_id":"y"}`)
	})

	created := env.taskSvc.createdTasks()
	assert.Len(t, created, 4,
		`leading/trailing spaces are part of the id value: "x" vs " x" and ns "a" vs " a" are four distinct findings`)
	assert.Len(t, env.rootsRepo.items, 4, "four identities → four identity rows")
}

// The service-level shape of the same defect: two remarks whose explicit
// identities JOIN to the same string are two findings, and the second must
// get its own root — not be swallowed by "уже открыта" against the first
// one's root (codex-review P1, MR !1078, round 7).
func TestClosedFollowUp_CollidingExplicitIdentitiesAreDistinctFindings(t *testing.T) {
	env := setupFollowUpEnv(t)

	env.comment(t, "Первое замечание.", func(c *domain.Comment) {
		c.Metadata = []byte(`{"finding_ns":"a:b","finding_id":"c"}`)
	})
	env.comment(t, "Второе замечание.", func(c *domain.Comment) {
		c.Metadata = []byte(`{"finding_ns":"a","finding_id":"b:c"}`)
	})

	created := env.taskSvc.createdTasks()
	assert.Len(t, created, 2,
		"distinct (ns,id) pairs that join to one string are still two findings")
}

// The excerpt is counted in runes. On majority-Cyrillic traffic a byte cut is
// both half the intended length and free to split a rune, putting invalid UTF-8
// into a title.
func TestFollowUpTitleExcerpt_CountsRunesAndFlattens(t *testing.T) {
	short := followUpTitleExcerpt("Шапка\nне   sticky")
	assert.Equal(t, "Шапка не sticky", short, "newlines and runs of whitespace collapse to single spaces")
	assert.NotContains(t, short, "…", "a body that fits is not marked as cut")

	long := strings.Repeat("я", 200)
	cut := followUpTitleExcerpt(long)
	assert.True(t, strings.HasSuffix(cut, "…"))
	assert.Equal(t, followUpTitleExcerptRunes, len([]rune(strings.TrimSuffix(cut, "…"))),
		"exactly 60 runes, not 60 bytes")
	assert.True(t, utf8Valid(cut), "must never cut a multi-byte rune in half")
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// --- The closing-report branch (found by the live acceptance run) -----------
//
// This branch does not come from the task's four criteria. It comes from
// running the shipped mechanism against prod: `move_task(done, comment=…)` is
// two API calls, so the closing note lands on an ALREADY-closed card, and the
// closer is usually not the assignee. Every routine cross-agent close —
// which the fleet's own rules both permit and require a comment for — opened a
// follow-up card. Live instance: `#0da96e03`, from the closer's own
// "Закрываю: …" note, within the same second.

// logMove records a task.moved activity entry by actor at t.
func (env followUpEnv) logMove(actorID uuid.UUID, actorType domain.ActorType, t time.Time) {
	id := uuid.New()
	env.activityRepo.items[id] = &domain.ActivityLog{
		ID: id, EntityType: "task", EntityID: env.sourceID,
		Action: "task.moved", ActorID: actorID, ActorType: actorType, CreatedAt: t,
	}
}

func TestClosedFollowUp_ClosersOwnClosingNoteCreatesNothing(t *testing.T) {
	env := setupFollowUpEnv(t)
	closer := uuid.New()
	env.logMove(closer, domain.ActorTypeAgent, frozenTime)

	env.comment(t, "Закрываю: работа отгружена, PR смёржен.", func(c *domain.Comment) {
		c.AuthorID = closer
		c.AuthorType = domain.ActorTypeAgent
	})

	assert.Empty(t, env.taskSvc.createdTasks(),
		"the closer's own note about closing is that close's report, not a remark to route back")
	assert.Empty(t, env.systemNotices())
}

// The narrowness is the whole point: a remark from someone who did NOT close
// the card must still be routed, however soon after the close it arrives.
func TestClosedFollowUp_DifferentAuthorAfterACloseStillRoutes(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.logMove(uuid.New(), domain.ActorTypeAgent, frozenTime) // somebody else closed it

	env.comment(t, "Шапка не sticky — вернулось.")

	assert.Len(t, env.taskSvc.createdTasks(), 1,
		"only the CLOSER's own note is exempt; a colleague's remark seconds later is the real case")
}

// And the same actor coming back LATER is writing a remark, not a closing note.
//
// "Later" is measured in SECONDS, not hours, and that is the point of the
// second case below: the window was 60s for one deploy, and a live run caught it
// swallowing an ordinary "close it, then think of something" remark written 28
// seconds after the close. The guard has to be narrow enough that the pairing
// it recognises is one client round-trip (measured: 139ms), not a train of
// thought.
func TestClosedFollowUp_SameCloserLongAfterTheCloseStillRoutes(t *testing.T) {
	env := setupFollowUpEnv(t)
	closer := uuid.New()
	env.logMove(closer, domain.ActorTypeAgent, frozenTime.Add(-2*time.Hour))

	env.comment(t, "Вернулся на следующий день: ширина 1120 вместо 1240.", func(c *domain.Comment) {
		c.AuthorID = closer
		c.AuthorType = domain.ActorTypeAgent
	})

	assert.Len(t, env.taskSvc.createdTasks(), 1,
		"the exemption is for the note that accompanies the close, not for the closer forever")
}

// The live regression, pinned: 28 seconds after their own close, the same actor
// writes a genuine remark. Under the original 60s window this was silently
// swallowed — no card, no notice, and nothing anywhere saying a remark had been
// dropped.
func TestClosedFollowUp_SameCloserTwentyEightSecondsLaterStillRoutes(t *testing.T) {
	env := setupFollowUpEnv(t)
	closer := uuid.New()
	env.logMove(closer, domain.ActorTypeAgent, frozenTime.Add(-28*time.Second))

	env.comment(t, "И ещё: ширина 1120 вместо 1240 — заметил уже после закрытия.", func(c *domain.Comment) {
		c.AuthorID = closer
		c.AuthorType = domain.ActorTypeAgent
	})

	assert.Len(t, env.taskSvc.createdTasks(), 1,
		"28s after a close is a second thought, not the close's own note — measured live on prod")
}

// The pairing the guard DOES have to catch: the closing note of the same call,
// which lands a fraction of a second after the move.
func TestClosedFollowUp_ClosingNoteOneRoundTripAfterTheMoveIsStillExempt(t *testing.T) {
	env := setupFollowUpEnv(t)
	closer := uuid.New()
	env.logMove(closer, domain.ActorTypeAgent, frozenTime.Add(-139*time.Millisecond))

	env.comment(t, "Закрываю: отгружено.", func(c *domain.Comment) {
		c.AuthorID = closer
		c.AuthorType = domain.ActorTypeAgent
	})

	assert.Empty(t, env.taskSvc.createdTasks(),
		"139ms is the measured move→comment gap of one move_task call; that pairing must stay exempt")
}

// Fails OPEN when the activity log cannot be read: a duplicate card is
// recoverable, a swallowed remark is the defect this file exists to fix.
func TestClosedFollowUp_UnreadableActivityLogStillRoutes(t *testing.T) {
	env := setupFollowUpEnv(t)
	closer := uuid.New()
	env.logMove(closer, domain.ActorTypeAgent, frozenTime)
	env.activityRepo.errToReturn = assert.AnError

	env.comment(t, "Закрываю.", func(c *domain.Comment) {
		c.AuthorID = closer
		c.AuthorType = domain.ActorTypeAgent
	})

	assert.Len(t, env.taskSvc.createdTasks(), 1,
		"could-not-look must not read as could-not-find — this guard fails open by design")
}

// --- The informational-flag branch (task #df22e695, audit follow-up to
// #754173eb) --------------------------------------------------------------
//
// The task names three branches and all three are here, including the red
// control (unflagged still routes) the task's own acceptance criteria demand:
// a guard that refuses everything is indistinguishable from a working one
// unless something shows the positive case still fires.

func TestClosedFollowUp_InformationalFlagCreatesNothing(t *testing.T) {
	env := setupFollowUpEnv(t)

	env.comment(t, "Принял, спасибо. Со своей стороны действий не требуется.", func(c *domain.Comment) {
		c.Metadata = []byte(`{"informational":true}`)
	})

	assert.Empty(t, env.taskSvc.createdTasks(),
		"a comment the author declared purely informational must not open a follow-up")
	assert.Empty(t, env.systemNotices())
}

// Red control: the exact same wording, unflagged, still opens a card — proving
// the guard added above is not silently swallowing every acknowledgement (or
// everything else) regardless of the flag. Without this test, a `return` added
// unconditionally at the top of createClosedTaskFollowUp would pass the case
// above and go undetected.
func TestClosedFollowUp_UnflaggedAcknowledgementStillRoutes(t *testing.T) {
	env := setupFollowUpEnv(t)

	env.comment(t, "Принял, спасибо. Со своей стороны действий не требуется.")

	assert.Len(t, env.taskSvc.createdTasks(), 1,
		"forgetting the flag must cost nothing — an unflagged comment keeps today's behaviour")
}

// A live "❓ Blocking @pavel" marker in the same body overrides the flag: a
// question addressed to a human must never disappear because the author also
// (mistakenly, or for the rest of the same comment) marked it informational.
func TestClosedFollowUp_InformationalFlagDoesNotSuppressABlockingMarker(t *testing.T) {
	env := setupFollowUpEnv(t)

	env.comment(t, "Со своей стороны действий не требуется.\n\n"+
		"---\n\n❓ **Blocking @pavel**: но нужно решение по бюджету на этот квартал.",
		func(c *domain.Comment) {
			c.Metadata = []byte(`{"informational":true}`)
		})

	assert.Len(t, env.taskSvc.createdTasks(), 1,
		"a Blocking marker must win over the informational flag — a question to a human is never dropped")
}

// The flag is read the same way metadata.source is: self-declared, tolerant of
// garbage, and honoured only to suppress a side effect on the author's OWN
// comment. Malformed or absent metadata must read as "not flagged", not error.
func TestCommentIsInformational(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
		want bool
	}{
		{"true", []byte(`{"informational":true}`), true},
		{"false explicit", []byte(`{"informational":false}`), false},
		{"absent key", []byte(`{"source":"pr-driver"}`), false},
		{"nil metadata", nil, false},
		{"empty object", []byte(`{}`), false},
		{"malformed json", []byte(`not json`), false},
		{"wrong type", []byte(`{"informational":"yes"}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, commentIsInformational(tc.raw))
		})
	}
}
