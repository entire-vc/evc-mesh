package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Regression: the class of a "❓ Blocking" gate is decided by the hard rules of
// asks.py, not by whether the asker remembered a "[soft]" tag.

func TestClassifyAsk(t *testing.T) {
	cases := []struct {
		name     string
		question string
		labels   []string
		hard     bool
		rule     string
	}{
		// live gates of 2026-11-11
		{"plan approval", "Утверждаешь план пяти витрин (Stage 1)? Default: да, Local Sync с текущим H1", nil, false, "no hard rule matched"},
		{"letter in the owner's name", "Отправлять КП Галине (письмо от вашего имени, код в письме) или закрыть заявку без отправки?", nil, true, "keyword:кп"},
		{"call and whatsapp", "Одобряете этот текст для WhatsApp при недозвоне и берёте первый звонок Анне?", nil, true, "keyword:whatsapp"},
		// the four classes
		{"payment", "Оплатить счёт подрядчику?", nil, true, "keyword:оплат"},
		{"token", "Rotate the API token today?", nil, true, "keyword:token"},
		{"publication", "Опубликовать анонс в канале?", nil, true, "keyword:опубликов"},
		{"destructive", "Drop the old table?", nil, true, "keyword:drop"},
		// Go's \b is ASCII-only: a Cyrillic whole-word fragment must still match
		{"cyrillic whole word at end", "Выставить счет", nil, true, "keyword:счет"},
		{"cyrillic whole word mid-text", "Какая цена, ок?", nil, true, "keyword:цена"},
		{"cyrillic whole word not a prefix", "Ценности команды описать так?", nil, false, "no hard rule matched"},
		// word-start anchoring (Python's (?<!\w))
		{"inside a word", "Repay the tech debt first or ship?", nil, false, "no hard rule matched"},
		{"underscore is a word rune", "Rename my_payment_flag?", nil, false, "no hard rule matched"},
		// labels and the fail-closed cases
		{"hard label", "Which layout?", []string{"design", "Money"}, true, "label:money"},
		{"no text", "   ", nil, true, "unsure: no question text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := classifyAsk(tc.question, tc.labels)
			assert.Equal(t, tc.hard, v.Hard, v.Rule)
			assert.Equal(t, tc.rule, v.Rule)
		})
	}
}

func TestMarkerGateClass(t *testing.T) {
	cases := []struct {
		name string
		body string
		want domain.HumanGateClass
		rule string
	}{
		{"soft question with the asker's default", "❓ Blocking @alex: Which of the two layouts?\n\nDefault: the left one",
			domain.HumanGateClassSoft, "no hard rule matched"},
		{"bold marker, russian default line", "❓ **Blocking @alex**: Утверждаешь план?\nПо умолчанию: да, как в черновике",
			domain.HumanGateClassSoft, "no hard rule matched"},
		{"no default stated", "❓ Blocking @alex: Which of the two layouts?",
			domain.HumanGateClassHard, "no default stated by the asker"},
		{"only the system fallback default", "❓ Blocking @alex: Which layout?\nDefault: " + domain.DefaultMarkerRecommendedDefault,
			domain.HumanGateClassHard, "no default stated by the asker"},
		{"hard rule beats [soft]", "❓ Blocking @alex: Pay the invoice now? [soft]\nDefault: pay on Monday",
			domain.HumanGateClassHard, "keyword:pay"},
		{"hard keyword in a following line", "❓ Blocking @alex: Go ahead?\nThis sends the newsletter.\nDefault: yes",
			domain.HumanGateClassHard, "keyword:newsletter"},
		{"[hard] beats everything", "❓ Blocking @alex: Which layout? [hard]\nDefault: the left one",
			domain.HumanGateClassHard, "tag:hard"},
		{"[soft] without a default stays soft", "❓ Blocking @alex: Which layout? [soft]",
			domain.HumanGateClassSoft, "tag:soft"},
		{"a quoted hard word does not count", "❓ Blocking @alex: Which layout?\n> the old card said `pay now`\nDefault: the left one",
			domain.HumanGateClassSoft, "no hard rule matched"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rule := markerGateClass(tc.body, "alex", nil)
			assert.Equal(t, tc.want, got, rule)
			assert.Equal(t, tc.rule, rule)
		})
	}
}

// gateFlowEnv is the whole path of a "❓ Blocking" marker: the comment service arms the
// gate through the real task service, and the real default-on-timeout sweep records
// the decision through the same comment service.
type gateFlowEnv struct {
	comments *commentService
	taskRepo *MockTaskRepository
	hgd      *fakeHGDRepo
	notify   *MockNotificationService
	sweep    HumanGateDefaultTimeoutService
	tasks    TaskService
	taskID   uuid.UUID
	asker    uuid.UUID
}

func newGateFlowEnv(t *testing.T) gateFlowEnv {
	t.Helper()
	freezeTimeNow(t)
	commentRepo := NewMockCommentRepository()
	taskRepo := NewMockTaskRepository()
	activityRepo := NewMockActivityLogRepository()
	statusRepo := NewMockTaskStatusRepository()
	projectRepo := NewMockProjectRepository()
	userRepo := NewMockUserRepository()
	hgd := &fakeHGDRepo{}
	notify := NewMockNotificationService()

	wsID, projID, inProgress := uuid.New(), uuid.New(), uuid.New()
	projectRepo.items[projID] = &domain.Project{ID: projID, WorkspaceID: wsID}
	statusRepo.items[inProgress] = &domain.TaskStatus{ID: inProgress, ProjectID: projID,
		Category: domain.StatusCategoryInProgress, Name: "In Progress"}
	userRepo.AddUser(wsID, &domain.User{ID: uuid.New(), Username: "alex", Name: "Alex"})

	taskSvc := newTestTaskService(taskRepo, statusRepo, NewMockTaskDependencyRepository(), activityRepo,
		WithCommentRepoTask(commentRepo))
	comments := NewCommentService(commentRepo, taskRepo, activityRepo,
		WithCommentProjectRepo(projectRepo), WithCommentStatusRepo(statusRepo), WithCommentUserRepo(userRepo),
		WithCommentTaskService(taskSvc), WithHumanGateDecisionRepo(hgd),
	).(*commentService)

	taskID := uuid.New()
	taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: projID, StatusID: inProgress, Title: "T"}
	mover := newFakeDefaultTimeoutTaskMover()
	mover.seed(&domain.Task{ID: taskID, ProjectID: projID, StatusID: inProgress, Title: "T"})
	sweep := NewHumanGateDefaultTimeoutService(taskRepo, statusRepo, projectRepo, comments, mover, notify)
	return gateFlowEnv{comments: comments, taskRepo: taskRepo, hgd: hgd, notify: notify, sweep: sweep,
		tasks: taskSvc, taskID: taskID, asker: uuid.New()}
}

func (e gateFlowEnv) ask(t *testing.T, body string) *domain.Task {
	t.Helper()
	require.NoError(t, e.comments.Create(context.Background(), &domain.Comment{
		TaskID: e.taskID, AuthorID: e.asker, AuthorType: domain.ActorTypeAgent, Body: body}))
	task, err := e.taskRepo.GetByID(context.Background(), e.taskID)
	require.NoError(t, err)
	require.True(t, task.HumanGate, "the marker must arm the gate")
	return task
}

func (e gateFlowEnv) sweepAt(t *testing.T, at time.Time) int {
	t.Helper()
	original := timeNow
	timeNow = func() time.Time { return at }
	defer func() { timeNow = original }()
	n, err := e.sweep.SweepExpiredDefaultGates(context.Background())
	require.NoError(t, err)
	return n
}

// AC1: a soft question with a stated default is decided by that default once the
// window passes: a default_applied decision, the gate lifted, the asker notified.
func TestSoftMarker_DefaultAppliedAfterWindow(t *testing.T) {
	e := newGateFlowEnv(t)
	task := e.ask(t, "❓ Blocking @alex: Which of the two layouts goes first?\n\nDefault: the left one")
	assert.Equal(t, domain.HumanGateClassSoft, task.HumanGateClass)
	require.NotNil(t, task.GateDeadline)

	assert.Zero(t, e.sweepAt(t, task.GateDeadline.Add(-time.Minute)), "nothing applies before the deadline")
	assert.Empty(t, e.hgd.rows)

	require.Equal(t, 1, e.sweepAt(t, task.GateDeadline.Add(time.Minute)))
	require.Len(t, e.hgd.rows, 1)
	d := e.hgd.rows[0]
	require.NotNil(t, d.Provenance)
	assert.Equal(t, domain.HumanGateProvenanceDefaultApplied, *d.Provenance)
	require.NotNil(t, d.Quote)
	assert.Equal(t, "the left one", *d.Quote)
	assert.Equal(t, e.asker, d.DecidedBy)

	after, err := e.taskRepo.GetByID(context.Background(), e.taskID)
	require.NoError(t, err)
	assert.False(t, after.HumanGate, "the gate is lifted, which wakes the asker's human wait")
	require.Len(t, e.notify.calls, 1)
	assert.Equal(t, "task.human_gate_default_applied", e.notify.calls[0].EventType)
}

// AC2: a question with a hard trait is never released by the clock, whatever the
// deadline and however late the sweep runs, with or without a [soft] tag.
func TestHardMarker_NeverReleasedByClock(t *testing.T) {
	for _, body := range []string{
		"❓ Blocking @alex: Оплатить счёт подрядчику сегодня?\nDefault: оплатить\nDeadline: 2025-06-16T00:00:00Z",
		"❓ Blocking @alex: Rotate the deploy token now? [soft]\nDefault: rotate on Monday",
		"❓ Blocking @alex: Publish the release notes on the blog?\nDefault: publish as drafted",
	} {
		e := newGateFlowEnv(t)
		task := e.ask(t, body)
		assert.Equal(t, domain.HumanGateClassHard, task.HumanGateClass, body)
		for _, at := range []time.Time{frozenTime.Add(25 * time.Hour), frozenTime.AddDate(100, 0, 0)} {
			assert.Zero(t, e.sweepAt(t, at), body)
		}
		assert.Empty(t, e.hgd.rows, body)
		after, err := e.taskRepo.GetByID(context.Background(), e.taskID)
		require.NoError(t, err)
		assert.True(t, after.HumanGate, body)
	}
}

// legacyHard re-labels the gate hard, as the tag-only rule armed it before this change.
func (e gateFlowEnv) legacyHard(t *testing.T) {
	t.Helper()
	e.taskRepo.mu.Lock()
	e.taskRepo.items[e.taskID].HumanGateClass = domain.HumanGateClassHard
	e.taskRepo.mu.Unlock()
}

func (e gateFlowEnv) class(t *testing.T) domain.HumanGateClass {
	t.Helper()
	task, err := e.taskRepo.GetByID(context.Background(), e.taskID)
	require.NoError(t, err)
	return task.HumanGateClass
}

// AC4: a gate armed hard before the rule goes through the same rule on the sweep's
// tick and is then decided by its stated default, with no hand on the gate.
func TestReconcileMarkerGateClasses_LegacyGateGetsItsDefault(t *testing.T) {
	e := newGateFlowEnv(t)
	task := e.ask(t, "❓ Blocking @alex: Утверждаешь план пяти витрин?\n\nПо умолчанию: да, как в черновике")
	e.legacyHard(t)
	assert.Zero(t, e.sweepAt(t, task.GateDeadline.Add(time.Hour)), "a hard gate is not swept")

	n, err := e.comments.ReconcileMarkerGateClasses(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, domain.HumanGateClassSoft, e.class(t))
	var notice bool
	for _, c := range e.comments.commentRepo.(*MockCommentRepository).items {
		if c.SystemCode != nil && *c.SystemCode == SysCodeGateReclassifiedSoft {
			notice = true
		}
	}
	assert.True(t, notice, "the card says why its gate changed class")

	n, err = e.comments.ReconcileMarkerGateClasses(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n, "idempotent")

	require.Equal(t, 1, e.sweepAt(t, task.GateDeadline.Add(time.Hour)))
	require.Len(t, e.hgd.rows, 1)
	assert.Equal(t, domain.HumanGateProvenanceDefaultApplied, *e.hgd.rows[0].Provenance)
	assert.Equal(t, "да, как в черновике", *e.hgd.rows[0].Quote)
}

// A gate armed before markerDefault stored the comment's first "Default:" line, which
// on a live card was the default of one option of the plan, not of the ask.
// The reconciliation stores the ask's own default, and that is what applies.
func TestReconcileMarkerGateClasses_CorrectsTheStoredDefault(t *testing.T) {
	e := newGateFlowEnv(t)
	body := "План витрин.\n- **Default:** текущий H1 «Sync your docs»\n- **B:** «Sync selected folders»\n\n---\n\n" +
		"❓ **Blocking @alex**: Утверждаешь план пяти витрин (Stage 1)? Default: да, с текущим H1"
	task := e.ask(t, body)
	e.taskRepo.mu.Lock()
	old := extractRecommendedDefault(body)
	e.taskRepo.items[e.taskID].RecommendedDefault = &old
	e.taskRepo.items[e.taskID].HumanGateClass = domain.HumanGateClassHard
	e.taskRepo.mu.Unlock()
	require.Equal(t, "текущий H1 «Sync your docs»", old)

	n, err := e.comments.ReconcileMarkerGateClasses(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, 1, e.sweepAt(t, task.GateDeadline.Add(time.Hour)))
	require.Len(t, e.hgd.rows, 1)
	assert.Equal(t, "да, с текущим H1", *e.hgd.rows[0].Quote)
}

// An API re-arm that lands between the reconciliation's read and its UPDATE changes the
// gate's author and reason; the guarded UPDATE then leaves that gate hard.
func TestReconcileMarkerGateClasses_LosesToAConcurrentReArm(t *testing.T) {
	e := newGateFlowEnv(t)
	e.ask(t, "❓ Blocking @alex: Which layout goes first?\nDefault: the left one")
	e.legacyHard(t)
	e.taskRepo.mu.Lock()
	task := e.taskRepo.items[e.taskID]
	author, reason := *task.GateAuthor, *task.GateReason
	marker := e.taskRepo.gateSourceComment[e.taskID]
	e.taskRepo.mu.Unlock()
	require.NotEqual(t, uuid.Nil, marker, "a marker arm records its comment")

	repo := e.taskRepo
	e.taskRepo.mu.Lock()
	other := uuid.New()
	task.GateAuthor = &other
	e.taskRepo.mu.Unlock()
	_, ok, err := repo.ReclassifyHumanGateSoft(context.Background(), e.taskID, author, marker, reason, "the left one")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, domain.HumanGateClassHard, e.class(t))

	// Same author and reason, but the gate is now an API arm: still refused.
	e.taskRepo.mu.Lock()
	task.GateAuthor = &author
	e.taskRepo.mu.Unlock()
	e.taskRepo.setGateSource(e.taskID, domain.ArmHumanGateSourceAPI)
	_, ok, err = repo.ReclassifyHumanGateSoft(context.Background(), e.taskID, author, marker, reason, "the left one")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, domain.HumanGateClassHard, e.class(t))
}

// An API arm that carries the marker's own author and reason is still an API arm: the
// stored arm source, not author and reason, tells it apart, and it keeps its class.
func TestReconcileMarkerGateClasses_APIArmWithTheMarkersAuthorAndReasonStaysHard(t *testing.T) {
	e := newGateFlowEnv(t)
	marked := e.ask(t, "❓ Blocking @alex: Which layout goes first?\nDefault: the left one")
	require.NoError(t, e.tasks.ArmHumanGate(context.Background(), domain.ArmHumanGateInput{
		TaskID: e.taskID, Author: *marked.GateAuthor, AuthorType: *marked.GateAuthorType,
		Reason: *marked.GateReason, RecommendedDefault: "the left one",
		Class: domain.HumanGateClassHard, Source: domain.ArmHumanGateSourceAPI, Predicate: allowingPredicate(),
	}))
	armed, err := e.taskRepo.GetByID(context.Background(), e.taskID)
	require.NoError(t, err)
	require.Equal(t, *marked.GateAuthor, *armed.GateAuthor)
	require.Equal(t, *marked.GateReason, *armed.GateReason)
	require.Equal(t, domain.HumanGateClassHard, armed.HumanGateClass)

	n, err := e.comments.ReconcileMarkerGateClasses(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, domain.HumanGateClassHard, e.class(t))
	assert.Zero(t, e.sweepAt(t, frozenTime.AddDate(100, 0, 0)))
}

// A gate whose arm source was not recorded (armed before tasks.human_gate_source) is
// judged by the thread: it passes the marker checks only if no API-arm hint, which every
// A re-ask by the same author with the same question line re-arms the gate from a newer
// comment that says more (here: a money line under the question). A reconciliation that
// read the older marker must not apply that marker's soft verdict to the newer gate: the
// UPDATE is guarded on the id of the marker it checked.
func TestReconcileMarkerGateClasses_SameAuthorReAskIsNotJudgedByTheOlderMarker(t *testing.T) {
	e := newGateFlowEnv(t)
	e.ask(t, "❓ Blocking @alex: Which layout goes first?\nDefault: the left one")
	e.legacyHard(t)
	e.taskRepo.mu.Lock()
	task := e.taskRepo.items[e.taskID]
	author, reason := *task.GateAuthor, *task.GateReason
	older := e.taskRepo.gateSourceComment[e.taskID]
	e.taskRepo.mu.Unlock()

	e.ask(t, "❓ Blocking @alex: Which layout goes first?\nThe winner ships with the invoice page.\nDefault: the left one")
	e.taskRepo.mu.Lock()
	newer := e.taskRepo.gateSourceComment[e.taskID]
	require.Equal(t, reason, *task.GateReason, "same question line, same reason")
	require.Equal(t, author, *task.GateAuthor)
	e.taskRepo.mu.Unlock()
	require.NotEqual(t, older, newer)
	require.Equal(t, domain.HumanGateClassHard, e.class(t))

	_, ok, err := e.taskRepo.ReclassifyHumanGateSoft(context.Background(), e.taskID, author, older, reason, "the left one")
	require.NoError(t, err)
	assert.False(t, ok, "the older marker's verdict does not reach the newer gate")
	n, err := e.comments.ReconcileMarkerGateClasses(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n, "the live marker is the newer, hard one")
	assert.Equal(t, domain.HumanGateClassHard, e.class(t))
}

// API arm posts, is in the thread.
func TestReconcileMarkerGateClasses_UnrecordedSource(t *testing.T) {
	// A gate whose arm source was never recorded (armed before the column, or by a raw
	// PATCH flip) stays hard whether or not the thread holds an API-arm hint: the hint is
	// best-effort, so its absence proves nothing.
	for _, apiHint := range []bool{false, true} {
		t.Run(fmt.Sprintf("api hint %v", apiHint), func(t *testing.T) {
			e := newGateFlowEnv(t)
			e.ask(t, "❓ Blocking @alex: Which layout goes first?\nDefault: the left one")
			e.legacyHard(t)
			e.taskRepo.setGateSource(e.taskID, "")
			if apiHint {
				hint := &domain.Comment{ID: uuid.New(), TaskID: e.taskID, AuthorID: systemActorID,
					AuthorType: domain.ActorTypeSystem, SystemCode: SysCodePtr(SysCodeAPIArmHint),
					Body: "hint", CreatedAt: frozenTime.Add(time.Minute), UpdatedAt: frozenTime.Add(time.Minute)}
				require.NoError(t, e.comments.commentRepo.Create(context.Background(), hint))
			}
			n, err := e.comments.ReconcileMarkerGateClasses(context.Background())
			require.NoError(t, err)
			assert.Equal(t, 0, n)
			assert.Equal(t, domain.HumanGateClassHard, e.class(t))
		})
	}
}

func TestReconcileMarkerGateClasses_KeepsHard(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		mutate func(task *domain.Task)
	}{
		{"hard question", "❓ Blocking @alex: Оплатить счёт подрядчику?\nПо умолчанию: оплатить в понедельник", nil},
		{"no stated default", "❓ Blocking @alex: Which layout goes first?", nil},
		{"gate re-armed through the API after the marker", "❓ Blocking @alex: Which layout goes first?\nDefault: the left one",
			func(task *domain.Task) { r := "an API ask"; task.GateReason = &r }},
		{"gate author is not the marker's author", "❓ Blocking @alex: Which layout goes first?\nDefault: the left one",
			func(task *domain.Task) { a := uuid.New(); task.GateAuthor = &a }},
		{"hard label added later", "❓ Blocking @alex: Which layout goes first?\nDefault: the left one",
			func(task *domain.Task) { task.Labels = []string{"publish"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newGateFlowEnv(t)
			e.ask(t, tc.body)
			e.legacyHard(t)
			if tc.mutate != nil {
				e.taskRepo.mu.Lock()
				tc.mutate(e.taskRepo.items[e.taskID])
				e.taskRepo.mu.Unlock()
			}
			n, err := e.comments.ReconcileMarkerGateClasses(context.Background())
			require.NoError(t, err)
			assert.Zero(t, n)
			assert.Equal(t, domain.HumanGateClassHard, e.class(t))
			assert.Zero(t, e.sweepAt(t, frozenTime.AddDate(100, 0, 0)))
		})
	}
}

// Gates that stay ineligible (no marker) and sort first never crowd out an eligible
// one behind them: the reconciliation walks every page of the listing.
func TestReconcileMarkerGateClasses_WalksEveryPage(t *testing.T) {
	e := newGateFlowEnv(t)
	e.ask(t, "❓ Blocking @alex: Which layout goes first?\nDefault: the left one")
	e.legacyHard(t)
	for i := 1; i <= 3; i++ {
		id := uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
		e.taskRepo.mu.Lock()
		e.taskRepo.items[id] = &domain.Task{ID: id, HumanGate: true, HumanGateClass: domain.HumanGateClassHard}
		e.taskRepo.mu.Unlock()
		e.taskRepo.setGateSource(id, domain.ArmHumanGateSourceMarker)
	}
	orig := reconcileGatePageSize
	reconcileGatePageSize = 1
	t.Cleanup(func() { reconcileGatePageSize = orig })

	n, err := e.comments.ReconcileMarkerGateClasses(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, domain.HumanGateClassSoft, e.class(t))
}

// The arm parser before markerDefault read "Default:" only at a line start, so an ask
// whose default sits inline on the marker line stored the system fallback, or nothing
// and no deadline. Neither keeps the gate from its own default.
func TestReconcileMarkerGateClasses_LegacyStoredFallbackOrNothing(t *testing.T) {
	fallback := domain.DefaultMarkerRecommendedDefault
	for _, tc := range []struct {
		name   string
		stored *string
	}{
		{"stored the system fallback", &fallback},
		{"stored nothing", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newGateFlowEnv(t)
			e.ask(t, "❓ Blocking @alex: Which layout goes first? Default: the left one")
			e.taskRepo.mu.Lock()
			task := e.taskRepo.items[e.taskID]
			task.HumanGateClass = domain.HumanGateClassHard
			task.RecommendedDefault = tc.stored
			if tc.stored == nil {
				task.GateDeadline = nil
			}
			armedAt := *task.HumanGateArmedAt
			e.taskRepo.mu.Unlock()

			n, err := e.comments.ReconcileMarkerGateClasses(context.Background())
			require.NoError(t, err)
			require.Equal(t, 1, n)
			after, err := e.taskRepo.GetByID(context.Background(), e.taskID)
			require.NoError(t, err)
			require.NotNil(t, after.GateDeadline, "the sweep needs a deadline to reach the gate")

			assert.Zero(t, e.sweepAt(t, armedAt.Add(time.Hour)))
			require.Equal(t, 1, e.sweepAt(t, after.GateDeadline.Add(time.Minute)))
			require.Len(t, e.hgd.rows, 1)
			assert.Equal(t, "the left one", *e.hgd.rows[0].Quote)
		})
	}
}
