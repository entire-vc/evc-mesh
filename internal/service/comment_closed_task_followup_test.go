package service

import (
	"context"
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
	mu       sync.Mutex
	taskRepo *MockTaskRepository
	created  []*domain.Task
	err      error
}

func (f *followUpTaskCreator) Create(_ context.Context, task *domain.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if task.ID == uuid.Nil {
		task.ID = uuid.New()
	}
	task.CreatedAt = timeNow()
	task.UpdatedAt = timeNow()
	f.taskRepo.items[task.ID] = task
	f.created = append(f.created, task)
	return nil
}

func (f *followUpTaskCreator) createdTasks() []*domain.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*domain.Task(nil), f.created...)
}

type followUpEnv struct {
	svc          *commentService
	commentRepo  *MockCommentRepository
	taskRepo     *MockTaskRepository
	statusRepo   *MockTaskStatusRepository
	depRepo      *MockTaskDependencyRepository
	activityRepo *MockActivityLogRepository
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
	taskSvc := &followUpTaskCreator{taskRepo: taskRepo}

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
	).(*commentService)

	env := followUpEnv{
		svc: svc, commentRepo: commentRepo, taskRepo: taskRepo, statusRepo: statusRepo,
		depRepo: depRepo, activityRepo: activityRepo, taskSvc: taskSvc,
		projID: projID, wsID: wsID, doneID: doneID, todoID: todoID,
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
// closed card.
func (env followUpEnv) systemNotices() []domain.Comment {
	var out []domain.Comment
	for _, c := range env.commentRepo.items {
		if c.AuthorType == domain.ActorTypeSystem {
			out = append(out, *c)
		}
	}
	return out
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

// --- Dedup, scoping and switches -------------------------------------------

// The measured precedent was two remarks the same morning. Two cards for one
// conversation is noise; the second remark is not lost — it is on the closed
// card the live follow-up points at.
func TestClosedFollowUp_SecondRemarkReusesTheOpenFollowUp(t *testing.T) {
	env := setupFollowUpEnv(t)

	env.comment(t, "Шапка не sticky.")
	env.comment(t, "И ширина 1120 вместо 1240.")

	assert.Len(t, env.taskSvc.createdTasks(), 1, "a burst of remarks must not become a card each")
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

// Once the follow-up is itself closed, a fresh remark opens a fresh card —
// otherwise the dedup would swallow every later remark on the card forever.
func TestClosedFollowUp_ClosedFollowUpDoesNotBlockANewOne(t *testing.T) {
	env := setupFollowUpEnv(t)
	env.comment(t, "Шапка не sticky.")
	first := env.taskSvc.createdTasks()
	require.Len(t, first, 1)
	first[0].StatusID = env.doneID // the assignee handled it and closed it

	env.comment(t, "Вернулось после деплоя.")

	assert.Len(t, env.taskSvc.createdTasks(), 2,
		"dedup must key on a LIVE follow-up, not on one ever having existed")
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
