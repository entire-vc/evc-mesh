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

	created  []domain.Notification
	upserted []domain.NotificationPreference

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
func (f *fakeNotificationRepo) UpsertPreference(_ context.Context, pref *domain.NotificationPreference) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserted = append(f.upserted, *pref)
	return nil
}

func (f *fakeNotificationRepo) upsertedPrefs() []domain.NotificationPreference {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.NotificationPreference, len(f.upserted))
	copy(out, f.upserted)
	return out
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

// --- fake email service + user repo ------------------------------------------

type sentEmail struct {
	to, subject, body string
}

type fakeEmailService struct {
	mu      sync.Mutex
	enabled bool
	sent    []sentEmail
}

func (f *fakeEmailService) Enabled() bool { return f.enabled }
func (f *fakeEmailService) SendInvite(context.Context, string, string, string) error {
	return nil
}
func (f *fakeEmailService) SendNotification(_ context.Context, to, subject, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentEmail{to: to, subject: subject, body: body})
	return nil
}
func (f *fakeEmailService) recipients() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.sent))
	for _, s := range f.sent {
		out = append(out, s.to)
	}
	return out
}

var _ EmailService = (*fakeEmailService)(nil)

// fakeUserRepoForEmail resolves a user's account email — the default delivery
// address a preference row's Config falls back to when it has none of its own.
type fakeUserRepoForEmail struct {
	emails map[uuid.UUID]string
}

func (f *fakeUserRepoForEmail) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	email, ok := f.emails[id]
	if !ok {
		return nil, errors.New("user not found")
	}
	return &domain.User{ID: id, Email: email}, nil
}

func emailPref(wsID, userID uuid.UUID) domain.NotificationPreference {
	return domain.NotificationPreference{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		UserID:      &userID,
		Channel:     "email",
		Events:      pq.StringArray{"comment.created"},
		IsEnabled:   true,
	}
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

// TestDispatch_TargetUserIDSkipsOtherWorkspaceSubscribers_TaskAssigned is the
// same repro as TestDispatch_TargetUserIDSkipsOtherWorkspaceSubscribers, for
// task.assigned instead of task.reviewer_assigned: that event used to go
// through the workspace-wide dispatchUserNotification instead of a targeted
// one, so every subscriber learned about every assignment regardless of whose
// task it was.
func TestDispatch_TargetUserIDSkipsOtherWorkspaceSubscribers_TaskAssigned(t *testing.T) {
	wsID := uuid.New()
	assignee := uuid.New()
	bystander := uuid.New()

	assigneePref := webPushPref(wsID, assignee)
	assigneePref.Events = pq.StringArray{"task.assigned"}
	bystanderPref := webPushPref(wsID, bystander)
	bystanderPref.Events = pq.StringArray{"task.assigned"}

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{assigneePref, bystanderPref},
		members: map[uuid.UUID]bool{assignee: true, bystander: true},
	}
	svc := NewNotificationService(repo).(*notificationService)

	event := domain.NotificationEvent{
		WorkspaceID:  wsID,
		EventType:    "task.assigned",
		Title:        "Task assigned",
		TargetUserID: &assignee,
	}
	svc.dispatch(event)

	assert.Equal(t, []uuid.UUID{assignee}, repo.notifiedUsers(),
		"a targeted task.assigned event was broadcast to a bystander subscribed to the same event type")
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

// --- email channel ------------------------------------------------------

// TestDispatch_EmailUsesAccountAddressByDefault: a preference row with no
// custom address (empty Config) falls back to the subscriber's account email
// — the default the settings page shows before anyone edits the field.
func TestDispatch_EmailUsesAccountAddressByDefault(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()

	email := &fakeEmailService{enabled: true}
	users := &fakeUserRepoForEmail{emails: map[uuid.UUID]string{member: "member@example.com"}}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{emailPref(wsID, member)},
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo, WithEmailService(email, users, "https://mesh.example.com")).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	require.Eventually(t, func() bool { return len(email.recipients()) > 0 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"member@example.com"}, email.recipients())
}

// TestDispatch_EmailUsesCustomAddressFromConfig: a saved custom address in
// Config overrides the account email as the delivery target.
func TestDispatch_EmailUsesCustomAddressFromConfig(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()

	pref := emailPref(wsID, member)
	pref.Config = []byte(`{"email":"custom@example.com"}`)

	email := &fakeEmailService{enabled: true}
	users := &fakeUserRepoForEmail{emails: map[uuid.UUID]string{member: "member@example.com"}}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{pref},
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo, WithEmailService(email, users, "")).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	require.Eventually(t, func() bool { return len(email.recipients()) > 0 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"custom@example.com"}, email.recipients())
}

// TestDispatch_EmailSkippedWhenSMTPNotConfigured: the settings page tells the
// user email is unavailable via EmailAvailable(); dispatch itself must not
// attempt a send it cannot make, and must not error the other channels doing
// so.
func TestDispatch_EmailSkippedWhenSMTPNotConfigured(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()

	email := &fakeEmailService{enabled: false}
	users := &fakeUserRepoForEmail{emails: map[uuid.UUID]string{member: "member@example.com"}}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{emailPref(wsID, member)},
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo, WithEmailService(email, users, "")).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	time.Sleep(20 * time.Millisecond)
	assert.Empty(t, email.recipients(), "an email was sent despite SMTP being unconfigured")
}

// TestDispatch_EmailSkipsStrangers: same membership rule as the other two
// channels — a preference row is a claim, not an entitlement.
func TestDispatch_EmailSkipsStrangers(t *testing.T) {
	wsID := uuid.New()
	member := uuid.New()
	stranger := uuid.New()

	email := &fakeEmailService{enabled: true}
	users := &fakeUserRepoForEmail{emails: map[uuid.UUID]string{
		member:   "member@example.com",
		stranger: "stranger@example.com",
	}}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{emailPref(wsID, member), emailPref(wsID, stranger)},
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo, WithEmailService(email, users, "")).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	require.Eventually(t, func() bool { return len(email.recipients()) > 0 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"member@example.com"}, email.recipients())
}

// TestDispatch_EmailTargetUserIDGatesDeliveryToo: same targeted-event rule as
// web_push/browser_push — see TestDispatch_TargetUserIDGatesBrowserPushToo.
func TestDispatch_EmailTargetUserIDGatesDeliveryToo(t *testing.T) {
	wsID := uuid.New()
	reviewer := uuid.New()
	bystander := uuid.New()

	reviewerPref := emailPref(wsID, reviewer)
	reviewerPref.Events = pq.StringArray{"task.reviewer_assigned"}
	bystanderPref := emailPref(wsID, bystander)
	bystanderPref.Events = pq.StringArray{"task.reviewer_assigned"}

	email := &fakeEmailService{enabled: true}
	users := &fakeUserRepoForEmail{emails: map[uuid.UUID]string{
		reviewer:  "reviewer@example.com",
		bystander: "bystander@example.com",
	}}
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{reviewerPref, bystanderPref},
		members: map[uuid.UUID]bool{reviewer: true, bystander: true},
	}
	svc := NewNotificationService(repo, WithEmailService(email, users, "")).(*notificationService)

	event := domain.NotificationEvent{
		WorkspaceID:  wsID,
		EventType:    "task.reviewer_assigned",
		Title:        "Review requested",
		TargetUserID: &reviewer,
	}
	svc.dispatch(event)

	require.Eventually(t, func() bool { return len(email.recipients()) > 0 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"reviewer@example.com"}, email.recipients())
}

// TestEmailAvailable reflects the wired EmailService's own Enabled() state —
// the settings page's source of truth for whether to let anyone subscribe.
func TestEmailAvailable(t *testing.T) {
	repo := &fakeNotificationRepo{}

	t.Run("no email service wired", func(t *testing.T) {
		svc := NewNotificationService(repo)
		assert.False(t, svc.EmailAvailable())
	})

	t.Run("wired but SMTP not configured", func(t *testing.T) {
		svc := NewNotificationService(repo, WithEmailService(&fakeEmailService{enabled: false}, &fakeUserRepoForEmail{}, ""))
		assert.False(t, svc.EmailAvailable())
	})

	t.Run("wired and configured", func(t *testing.T) {
		svc := NewNotificationService(repo, WithEmailService(&fakeEmailService{enabled: true}, &fakeUserRepoForEmail{}, ""))
		assert.True(t, svc.EmailAvailable())
	})
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
