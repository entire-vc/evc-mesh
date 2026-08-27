package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// fakeDeliveryRepo captures what notifyMentions decided, so the recording path
// can be asserted end to end without a database.
type fakeDeliveryRepo struct {
	mu        sync.Mutex
	rows      []domain.CommentDeliveryOutcome
	failed    []string
	insertErr error
	listErr   error
}

func (f *fakeDeliveryRepo) InsertBatch(_ context.Context, rows []domain.CommentDeliveryOutcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return f.insertErr
	}
	f.rows = append(f.rows, rows...)
	return nil
}

func (f *fakeDeliveryRepo) MarkFailed(_ context.Context, _ uuid.UUID, slug, _kind, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, slug)
	return nil
}

func (f *fakeDeliveryRepo) ListByCommentIDs(
	_ context.Context, ids []uuid.UUID,
) (map[uuid.UUID][]domain.CommentDeliveryOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := map[uuid.UUID][]domain.CommentDeliveryOutcome{}
	for _, id := range ids {
		for _, r := range f.rows {
			if r.CommentID == id {
				out[id] = append(out[id], r)
			}
		}
	}
	return out, nil
}

func (f *fakeDeliveryRepo) snapshot() []domain.CommentDeliveryOutcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.CommentDeliveryOutcome, len(f.rows))
	copy(out, f.rows)
	return out
}

func bySlug(rows []domain.CommentDeliveryOutcome, slug string) *domain.CommentDeliveryOutcome {
	for i := range rows {
		if rows[i].RecipientSlug == slug {
			return &rows[i]
		}
	}
	return nil
}

// The headline case. One comment naming a handle that resolves to nobody used
// to produce zero rows anywhere; now it produces a verdict that says so.
func TestNotifyMentions_UnknownHandleGetsRecorded(t *testing.T) {
	env := setupCommentServiceWithMentions()
	repo := &fakeDeliveryRepo{}
	env.svc.deliveryRepo = repo

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "T"}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@nobody-at-all please look"}
	env.svc.notifyMentions(context.Background(), comment, task, "", env.wsID)

	rows := repo.snapshot()
	require.Len(t, rows, 1, "an unresolvable handle must leave exactly one recorded verdict")
	assert.Equal(t, "nobody-at-all", rows[0].RecipientSlug)
	assert.Equal(t, domain.DeliverySkipped, rows[0].Outcome)
	assert.Equal(t, domain.ReasonRecipientUnknown, rows[0].Reason)
	assert.Nil(t, rows[0].RecipientID)
	assert.NotEmpty(t, rows[0].Reason)
}

// AC2 as it actually runs: two handles on ONE comment, one whose lane is down
// and one that is alive, neither holding this card. Two different reasons in
// one write.
func TestNotifyMentions_TwoRecipientsGetTwoDifferentReasons(t *testing.T) {
	env := setupCommentServiceWithMentions()
	repo := &fakeDeliveryRepo{}
	env.svc.deliveryRepo = repo

	recent := time.Now().Add(-30 * time.Second)
	stale := time.Now().Add(-6 * time.Hour)
	env.agentSvc.AddAgent(env.wsID, &domain.Agent{
		ID: uuid.New(), WorkspaceID: env.wsID, Slug: "awake", LastHeartbeat: &recent,
	})
	env.agentSvc.AddAgent(env.wsID, &domain.Agent{
		ID: uuid.New(), WorkspaceID: env.wsID, Slug: "asleep", LastHeartbeat: &stale,
	})

	taskID := uuid.New()
	// Assigned to nobody, so neither handle has a queue path to this card.
	env.taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: env.projID, Title: "T",
		AssigneeType: domain.AssigneeTypeUnassigned,
	}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@awake @asleep look here"}
	env.svc.notifyMentions(context.Background(), comment, task, "", env.wsID)

	rows := repo.snapshot()
	require.Len(t, rows, 2)

	awake := bySlug(rows, "awake")
	asleep := bySlug(rows, "asleep")
	require.NotNil(t, awake)
	require.NotNil(t, asleep)

	assert.Equal(t, domain.DeliverySkipped, awake.Outcome)
	assert.Equal(t, domain.DeliverySkipped, asleep.Outcome)
	assert.Equal(t, domain.ReasonNoQueuePath, awake.Reason)
	assert.Equal(t, domain.ReasonRecipientOffline, asleep.Reason)
	assert.NotEqual(t, awake.Reason, asleep.Reason,
		"the two scenarios must not collapse into one reason")
}

// taskIsInTodoCategory has no status repo wired here, and must answer "not in
// the queue" rather than assuming the best. An unreadable status rendering as
// "delivered" is the failure this feature exists to remove, so the fail-closed
// direction is the behaviour under test, not an implementation detail.
func TestTaskIsInTodoCategory_FailsClosedWithoutAStatusRepo(t *testing.T) {
	env := setupCommentServiceWithMentions()
	assert.False(t, env.svc.taskIsInTodoCategory(context.Background(),
		&domain.Task{ID: uuid.New(), StatusID: uuid.New()}))
	assert.False(t, env.svc.taskIsInTodoCategory(context.Background(), nil),
		"a nil task must not panic and must not read as queued")
}

// Same direction for the human side: with no notification service there is no
// way to confirm a subscription, and "unconfirmed" must not render as
// "subscribed".
func TestUserHasMentionSubscription_FailsClosedWithoutANotifyService(t *testing.T) {
	env := setupCommentServiceWithMentions()
	assert.False(t, env.svc.userHasMentionSubscription(context.Background(), uuid.New()))
}

// attachDeliveryOutcomes must distinguish "this comment addressed nobody" from
// "the record could not be read". Both leave Delivery empty, but only the
// second is a fault — so the read error must not be mistaken for a clean
// result by anything downstream, and must not blow up the thread either.
func TestAttachDeliveryOutcomes_ReadFailureLeavesTheThreadRenderable(t *testing.T) {
	env := setupCommentServiceWithMentions()
	repo := &fakeDeliveryRepo{listErr: errors.New("database is on fire")}
	env.svc.deliveryRepo = repo

	comments := []domain.Comment{{ID: uuid.New()}, {ID: uuid.New()}}
	env.svc.attachDeliveryOutcomes(context.Background(), comments)

	assert.Nil(t, comments[0].Delivery)
	assert.Nil(t, comments[1].Delivery)
}

func TestAttachDeliveryOutcomes_AttachesRowsToTheirOwnComment(t *testing.T) {
	env := setupCommentServiceWithMentions()
	repo := &fakeDeliveryRepo{}
	env.svc.deliveryRepo = repo

	a, b := uuid.New(), uuid.New()
	repo.rows = []domain.CommentDeliveryOutcome{
		{CommentID: a, RecipientSlug: "one", Outcome: domain.DeliverySkipped, Reason: domain.ReasonNoQueuePath},
		{CommentID: b, RecipientSlug: "two", Outcome: domain.DeliveryDelivered, Reason: domain.ReasonTaskQueue},
	}

	comments := []domain.Comment{{ID: a}, {ID: b}, {ID: uuid.New()}}
	env.svc.attachDeliveryOutcomes(context.Background(), comments)

	require.Len(t, comments[0].Delivery, 1)
	assert.Equal(t, "one", comments[0].Delivery[0].RecipientSlug)
	require.Len(t, comments[1].Delivery, 1)
	assert.Equal(t, "two", comments[1].Delivery[0].RecipientSlug)
	assert.Nil(t, comments[2].Delivery, "a comment with no record must carry no record")
}

// markDeliveryFailed returns nil when there is nowhere to write, so the hook
// on the notification payload stays honestly unset rather than carrying a
// closure that silently does nothing.
func TestMarkDeliveryFailed_NilWithoutARepoAndDowngradesWithOne(t *testing.T) {
	env := setupCommentServiceWithMentions()
	commentID := uuid.New()

	assert.Nil(t, env.svc.markDeliveryFailed(commentID, "somebody", domain.RecipientKindAgent))

	repo := &fakeDeliveryRepo{}
	env.svc.deliveryRepo = repo
	hook := env.svc.markDeliveryFailed(commentID, "somebody", domain.RecipientKindAgent)
	require.NotNil(t, hook)

	hook(errors.New("event store rejected the write"))

	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Equal(t, []string{"somebody"}, repo.failed)
}

// The hook must actually be attached to the notification that goes out —
// otherwise a lost event leaves a confident "delivered" standing forever, and
// the failed outcome would be an enum value with no producer.
func TestNotifyMentions_AttachesThePersistFailureHook(t *testing.T) {
	env := setupCommentServiceWithMentions()
	repo := &fakeDeliveryRepo{}
	env.svc.deliveryRepo = repo

	recent := time.Now().Add(-time.Minute)
	env.agentSvc.AddAgent(env.wsID, &domain.Agent{
		ID: uuid.New(), WorkspaceID: env.wsID, Slug: "bill", LastHeartbeat: &recent,
	})

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "T"}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@bill ping"}
	env.svc.notifyMentions(context.Background(), comment, task, "", env.wsID)

	calls := env.notifySvc.Calls()
	require.Len(t, calls, 1)
	require.NotNil(t, calls[0].OnPersistErr,
		"the dispatched notification must carry the downgrade hook")

	// Firing it is what a failed durable write would do.
	calls[0].OnPersistErr(errors.New("persist failed"))
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Equal(t, []string{"bill"}, repo.failed)
}

// A failing recorder must not break commenting. The comment is the product;
// the verdict about it is commentary.
func TestNotifyMentions_RecorderFailureDoesNotBreakTheMentionPath(t *testing.T) {
	env := setupCommentServiceWithMentions()
	env.svc.deliveryRepo = &fakeDeliveryRepo{insertErr: errors.New("write refused")}

	recent := time.Now().Add(-time.Minute)
	env.agentSvc.AddAgent(env.wsID, &domain.Agent{
		ID: uuid.New(), WorkspaceID: env.wsID, Slug: "bill", LastHeartbeat: &recent,
	})

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "T"}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@bill ping"}
	require.NotPanics(t, func() {
		env.svc.notifyMentions(context.Background(), comment, task, "", env.wsID)
	})

	// The mention itself still went out.
	assert.Len(t, env.notifySvc.Calls(), 1)
}

// With no recorder wired at all, mentions must behave exactly as before —
// this is what makes the option safe to roll out.
func TestNotifyMentions_WithoutARecorderNothingChanges(t *testing.T) {
	env := setupCommentServiceWithMentions()
	require.Nil(t, env.svc.deliveryRepo)

	recent := time.Now().Add(-time.Minute)
	env.agentSvc.AddAgent(env.wsID, &domain.Agent{
		ID: uuid.New(), WorkspaceID: env.wsID, Slug: "bill", LastHeartbeat: &recent,
	})

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "T"}
	task := env.taskRepo.items[taskID]

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: "@bill ping"}
	env.svc.notifyMentions(context.Background(), comment, task, "", env.wsID)

	assert.Len(t, env.notifySvc.Calls(), 1)
}
