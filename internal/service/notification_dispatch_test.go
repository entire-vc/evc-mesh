package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The reported hole was that PUT /notifications/preferences let any authenticated
// caller subscribe to any workspace. Guarding that route stops new rows being
// written, and stops nothing that is already in the table: a row written before
// the fix, or by an account that was in the workspace and has since been removed,
// still names a recipient, and the dispatcher used to read the table and send.
//
// So the table is a record of what people asked for, not of what they are allowed
// to receive, and these tests are about the difference. The notification body
// carries the comment text — that is the thing being protected here.

// --- fake repository --------------------------------------------------------

type fakeNotificationRepo struct {
	mu sync.Mutex

	prefs        []domain.NotificationPreference
	prefsErr     error
	members      map[uuid.UUID]bool
	membersErr   error
	membersAsked [][]uuid.UUID

	created []domain.Notification

	deleted        int64
	deleteErr      error
	deleteArgsID   uuid.UUID
	deleteArgsUser uuid.UUID
}

func (f *fakeNotificationRepo) GetPreferencesByWorkspace(context.Context, uuid.UUID) ([]domain.NotificationPreference, error) {
	return f.prefs, f.prefsErr
}

func (f *fakeNotificationRepo) FilterWorkspaceMembers(_ context.Context, _ uuid.UUID, userIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	f.mu.Lock()
	f.membersAsked = append(f.membersAsked, userIDs)
	f.mu.Unlock()
	if f.membersErr != nil {
		return nil, f.membersErr
	}
	return f.members, nil
}

func (f *fakeNotificationRepo) CreateNotification(_ context.Context, n *domain.Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, *n)
	return nil
}

func (f *fakeNotificationRepo) DeletePreferenceForUser(_ context.Context, id, userID uuid.UUID) (int64, error) {
	f.deleteArgsID, f.deleteArgsUser = id, userID
	return f.deleted, f.deleteErr
}

func (f *fakeNotificationRepo) notifiedUsers() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]uuid.UUID, 0, len(f.created))
	for _, n := range f.created {
		ids = append(ids, *n.UserID)
	}
	return ids
}

// Unused by these tests, present to satisfy notificationRepository.
func (f *fakeNotificationRepo) GetPreferencesByUser(context.Context, uuid.UUID) ([]domain.NotificationPreference, error) {
	return nil, nil
}
func (f *fakeNotificationRepo) UpsertPreference(context.Context, *domain.NotificationPreference) error {
	return nil
}
func (f *fakeNotificationRepo) ListUnread(context.Context, uuid.UUID, int) ([]domain.Notification, error) {
	return nil, nil
}
func (f *fakeNotificationRepo) CountUnread(context.Context, uuid.UUID) (int, error) { return 0, nil }
func (f *fakeNotificationRepo) MarkRead(context.Context, uuid.UUID, []uuid.UUID) error {
	return nil
}
func (f *fakeNotificationRepo) MarkAllRead(context.Context, uuid.UUID) error { return nil }

var _ notificationRepository = (*fakeNotificationRepo)(nil)

// --- fake push service ------------------------------------------------------

type fakePushService struct {
	mu   sync.Mutex
	sent []uuid.UUID
}

func (f *fakePushService) Subscribe(context.Context, uuid.UUID, string, string, string, string) (*domain.PushSubscription, error) {
	return nil, nil
}
func (f *fakePushService) Unsubscribe(context.Context, uuid.UUID, string) error { return nil }
func (f *fakePushService) ListByUser(context.Context, uuid.UUID) ([]domain.PushSubscription, error) {
	return nil, nil
}
func (f *fakePushService) SendToUser(_ context.Context, userID, _ uuid.UUID, _ domain.PushPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, userID)
	return nil
}
func (f *fakePushService) GetVAPIDPublicKey() string { return "" }

func (f *fakePushService) recipients() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uuid.UUID(nil), f.sent...)
}

// --- helpers ----------------------------------------------------------------

func webPushPref(wsID, userID uuid.UUID) domain.NotificationPreference {
	return domain.NotificationPreference{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		UserID:      &userID,
		Channel:     "web_push",
		Events:      pq.StringArray{"comment.created"},
		IsEnabled:   true,
	}
}

func commentEvent(wsID uuid.UUID) domain.NotificationEvent {
	taskID := uuid.New()
	return domain.NotificationEvent{
		WorkspaceID: wsID,
		TaskID:      &taskID,
		EventType:   "comment.created",
		Title:       "New comment",
		Body:        "the confidential contents of somebody else's comment",
	}
}

// --- tests ------------------------------------------------------------------

// TestDispatch_StrangerSubscriptionDeliversNothing is the repro for the second
// layer of the hole. The subscription row exists, is enabled, and names the right
// workspace and the right event type — everything the dispatcher used to ask
// about. The one thing it is not is a membership, and that is now the thing that
// decides.
func TestDispatch_StrangerSubscriptionDeliversNothing(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()
	stranger := uuid.New()

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{webPushPref(wsID, member), webPushPref(wsID, stranger)},
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	assert.Equal(t, []uuid.UUID{member}, repo.notifiedUsers(),
		"a subscription row for somebody who is not in the workspace was delivered a comment body")
}

// TestDispatch_RemovedMemberStopsReceiving is the same rule in its everyday form:
// a subscription outlives the membership it was created under, and the row alone
// must not keep the notifications flowing after the person is gone.
func TestDispatch_RemovedMemberStopsReceiving(t *testing.T) {
	wsID := uuid.New()
	formerMember := uuid.New()

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{webPushPref(wsID, formerMember)},
		members: map[uuid.UUID]bool{},
	}
	svc := NewNotificationService(repo).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	assert.Empty(t, repo.notifiedUsers(),
		"a stale subscription kept delivering after the subscriber left the workspace")
}

// TestDispatch_AsksAboutEverySubscriberOnce: the membership question is asked in
// one query for the whole fan-out, not one per recipient, and every candidate has
// to be in it — a name left out of the question is a name nobody checked.
func TestDispatch_AsksAboutEverySubscriberOnce(t *testing.T) {
	wsID := uuid.New()
	first, second := uuid.New(), uuid.New()

	prefs := []domain.NotificationPreference{
		webPushPref(wsID, first),
		webPushPref(wsID, second),
		// A second channel for the same person must not ask about them twice.
		func() domain.NotificationPreference {
			p := webPushPref(wsID, first)
			p.Channel = "browser_push"
			return p
		}(),
	}
	repo := &fakeNotificationRepo{prefs: prefs, members: map[uuid.UUID]bool{first: true, second: true}}
	svc := NewNotificationService(repo).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	require.Len(t, repo.membersAsked, 1, "membership was resolved more than once for one event")
	assert.ElementsMatch(t, []uuid.UUID{first, second}, repo.membersAsked[0])
}

// TestDispatch_UnanswerableMembershipDeliversNothing pins the failure direction.
//
// If the membership lookup errors, the tempting reading is that it "did not say
// no" and the send should go ahead — the notification is only a convenience, after
// all. But the payload is a comment body, and the cost of the two mistakes is not
// symmetric: a dropped notification is a nuisance and a leaked comment cannot be
// taken back.
func TestDispatch_UnanswerableMembershipDeliversNothing(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()

	repo := &fakeNotificationRepo{
		prefs:      []domain.NotificationPreference{webPushPref(wsID, member)},
		membersErr: errors.New("connection reset"),
	}
	svc := NewNotificationService(repo).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	assert.Empty(t, repo.notifiedUsers(),
		"a failed membership lookup was treated as permission to send")
}

// TestDispatch_PreferenceLoadFailureDeliversNothing: the other early return.
func TestDispatch_PreferenceLoadFailureDeliversNothing(t *testing.T) {
	repo := &fakeNotificationRepo{prefsErr: errors.New("connection reset")}
	svc := NewNotificationService(repo).(*notificationService)

	svc.dispatch(commentEvent(uuid.New()))

	assert.Empty(t, repo.notifiedUsers())
	assert.Empty(t, repo.membersAsked, "membership was resolved for a preference set that failed to load")
}

// TestDispatch_BrowserPushSkipsStrangersToo: the in-app row and the browser push
// are two separate loops over the same preference list, and the first fix that
// suggests itself — filter where the notification row is written — leaves the
// second one sending the same body straight to a stranger's browser.
func TestDispatch_BrowserPushSkipsStrangersToo(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()
	stranger := uuid.New()

	memberPref := webPushPref(wsID, member)
	memberPref.Channel = "browser_push"
	strangerPref := webPushPref(wsID, stranger)
	strangerPref.Channel = "browser_push"

	push := &fakePushService{}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{memberPref, strangerPref},
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo, WithPushService(push)).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	require.Eventually(t, func() bool { return len(push.recipients()) > 0 }, time.Second, 5*time.Millisecond,
		"the member never received their push")
	assert.Equal(t, []uuid.UUID{member}, push.recipients(),
		"a browser push carrying the comment body went to somebody outside the workspace")
}

// TestDispatch_UnsubscribedEventTypeIsStillHonoured: the membership filter is an
// additional condition, not a replacement for the ones that were already there.
func TestDispatch_UnsubscribedEventTypeIsStillHonoured(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()

	pref := webPushPref(wsID, member)
	pref.Events = pq.StringArray{"task.assigned"}

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{pref},
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	assert.Empty(t, repo.notifiedUsers())
}

// TestDispatch_TargetUserIDSkipsOtherWorkspaceSubscribers is the repro for the
// reviewer-notification bug: a task.reviewer_assigned event is about one specific
// person, but dispatchUserNotification's underlying fan-out has no notion of "the
// reviewer" — it is gated purely by each subscriber's own preference row. Without
// TargetUserID, every workspace member subscribed to the event type would learn
// that someone else was made reviewer on someone else's task.
func TestDispatch_TargetUserIDSkipsOtherWorkspaceSubscribers(t *testing.T) {
	wsID := uuid.New()
	reviewer := uuid.New()
	bystander := uuid.New()

	reviewerPref := webPushPref(wsID, reviewer)
	reviewerPref.Events = pq.StringArray{"task.reviewer_assigned"}
	bystanderPref := webPushPref(wsID, bystander)
	bystanderPref.Events = pq.StringArray{"task.reviewer_assigned"}

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{reviewerPref, bystanderPref},
		members: map[uuid.UUID]bool{reviewer: true, bystander: true},
	}
	svc := NewNotificationService(repo).(*notificationService)

	event := domain.NotificationEvent{
		WorkspaceID:  wsID,
		EventType:    "task.reviewer_assigned",
		Title:        "Review requested",
		TargetUserID: &reviewer,
	}
	svc.dispatch(event)

	assert.Equal(t, []uuid.UUID{reviewer}, repo.notifiedUsers(),
		"a targeted reviewer-assigned event was broadcast to a bystander subscribed to the same event type")
}

// TestDispatch_TargetUserIDGatesBrowserPushToo: same rule, second loop — see
// TestDispatch_BrowserPushSkipsStrangersToo for why these are checked separately.
func TestDispatch_TargetUserIDGatesBrowserPushToo(t *testing.T) {
	wsID := uuid.New()
	reviewer := uuid.New()
	bystander := uuid.New()

	reviewerPref := webPushPref(wsID, reviewer)
	reviewerPref.Channel = "browser_push"
	reviewerPref.Events = pq.StringArray{"task.reviewer_assigned"}
	bystanderPref := webPushPref(wsID, bystander)
	bystanderPref.Channel = "browser_push"
	bystanderPref.Events = pq.StringArray{"task.reviewer_assigned"}

	push := &fakePushService{}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{reviewerPref, bystanderPref},
		members: map[uuid.UUID]bool{reviewer: true, bystander: true},
	}
	svc := NewNotificationService(repo, WithPushService(push)).(*notificationService)

	event := domain.NotificationEvent{
		WorkspaceID:  wsID,
		EventType:    "task.reviewer_assigned",
		Title:        "Review requested",
		TargetUserID: &reviewer,
	}
	svc.dispatch(event)

	require.Eventually(t, func() bool { return len(push.recipients()) > 0 }, time.Second, 5*time.Millisecond,
		"the reviewer never received their push")
	assert.Equal(t, []uuid.UUID{reviewer}, push.recipients(),
		"a targeted reviewer-assigned push went to a bystander too")
}

// TestNotify_DispatchesInTheBackground keeps the fire-and-forget contract: the
// caller is a request handler and must not wait on this.
func TestNotify_DispatchesInTheBackground(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{webPushPref(wsID, member)},
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo)

	svc.Notify(context.Background(), commentEvent(wsID))

	assert.Eventually(t, func() bool { return len(repo.notifiedUsers()) == 1 }, time.Second, 5*time.Millisecond)
}

// --- unsubscribe ------------------------------------------------------------

// TestDeletePreference_RemovesTheCallersOwnRow: the route that did not exist.
// Until it did, a subscription written by someone with no business in the
// workspace could not be withdrawn through the API at all.
func TestDeletePreference_RemovesTheCallersOwnRow(t *testing.T) {
	repo := &fakeNotificationRepo{deleted: 1}
	svc := NewNotificationService(repo)

	prefID, userID := uuid.New(), uuid.New()
	require.NoError(t, svc.DeletePreference(context.Background(), userID, prefID))

	assert.Equal(t, prefID, repo.deleteArgsID)
	assert.Equal(t, userID, repo.deleteArgsUser,
		"the delete was not scoped to the caller, so any preference id would have been removable")
}

// TestDeletePreference_SomebodyElsesRowIs404: not "forbidden", which would
// confirm that the id exists and whose it is not.
func TestDeletePreference_SomebodyElsesRowIs404(t *testing.T) {
	svc := NewNotificationService(&fakeNotificationRepo{deleted: 0})

	err := svc.DeletePreference(context.Background(), uuid.New(), uuid.New())

	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.StatusCode())
}

func TestDeletePreference_RepositoryErrorIsPropagated(t *testing.T) {
	svc := NewNotificationService(&fakeNotificationRepo{deleteErr: errors.New("connection reset")})

	require.Error(t, svc.DeletePreference(context.Background(), uuid.New(), uuid.New()))
}
