package service

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// captureLog redirects the standard logger for the duration of the test and
// returns a reader for what was written to it. The dispatcher reports an
// undeliverable channel through the log and nowhere else, so the log is the
// observable under test here, not incidental output.
func captureLog(t *testing.T) func() string {
	t.Helper()

	var mu sync.Mutex
	buf := &bytes.Buffer{}
	// log.Logger is not safe for concurrent Write from the dispatcher's
	// goroutines plus the test's read, so the sink does its own locking.
	sink := writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	})

	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(sink)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// mentionPref is an email subscriber who asked to hear about mentions
// specifically.
func mentionEmailPref(wsID, userID uuid.UUID) domain.NotificationPreference {
	return domain.NotificationPreference{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		UserID:      &userID,
		Channel:     "email",
		Events:      pq.StringArray{"task.mentioned"},
		IsEnabled:   true,
	}
}

// TestDispatch_UndeliverableEmailChannelIsLoggedNotSilent is the regression for
// the reason "email notifications don't arrive" went unnoticed for as long as it
// did: the rows were in the table, the events were firing, and the dispatcher
// skipped the channel without leaving a single line saying it had. Nobody was
// wrong about anything they could see.
func TestDispatch_UndeliverableEmailChannelIsLoggedNotSilent(t *testing.T) {
	readLog := captureLog(t)

	wsID := uuid.New()
	subscriber := uuid.New()

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{emailPref(wsID, subscriber)},
		members: map[uuid.UUID]bool{subscriber: true},
	}
	// SMTP is not configured on this instance — Enabled() is false.
	svc := NewNotificationService(repo,
		WithEmailService(&fakeEmailService{enabled: false}, &fakeUserRepoForEmail{}, "https://mesh.example.com"),
	).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	out := readLog()
	assert.Contains(t, out, "[notification][email]",
		"an undeliverable email channel with a live subscriber must say so")
	assert.Contains(t, out, "1 subscriber(s)",
		"the log must name how many people were actually affected")
	assert.Contains(t, out, wsID.String())
}

// TestDispatch_UndeliverableEmailChannelStaysQuietWithNoSubscribers: the log
// line above is diagnostic, not noise. An instance with no SMTP and nobody
// subscribed to email has nothing to report, and every dispatch saying so would
// bury the case that matters.
func TestDispatch_UndeliverableEmailChannelStaysQuietWithNoSubscribers(t *testing.T) {
	readLog := captureLog(t)

	wsID := uuid.New()
	member := uuid.New()

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{webPushPref(wsID, member)},
		members: map[uuid.UUID]bool{member: true},
	}
	svc := NewNotificationService(repo,
		WithEmailService(&fakeEmailService{enabled: false}, &fakeUserRepoForEmail{}, ""),
	).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	assert.NotContains(t, readLog(), "[notification][email]")
}

// TestDispatch_UndeliverableEmailChannelIgnoresStrangers: the count in the log
// is filtered by membership like the delivery loop is, so a stranger's stray
// preference row does not manufacture a report about a person who would never
// have been emailed anyway.
func TestDispatch_UndeliverableEmailChannelIgnoresStrangers(t *testing.T) {
	readLog := captureLog(t)

	wsID := uuid.New()
	stranger := uuid.New()

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{emailPref(wsID, stranger)},
		members: map[uuid.UUID]bool{}, // not a member
	}
	svc := NewNotificationService(repo,
		WithEmailService(&fakeEmailService{enabled: false}, &fakeUserRepoForEmail{}, ""),
	).(*notificationService)

	svc.dispatch(commentEvent(wsID))

	assert.NotContains(t, readLog(), "[notification][email]")
}

// TestDispatch_EmailSendFailureIsLoggedWithRecipientAndEvent: a transport
// refusal is the other half of the silence. The partner instance's invite mail
// had been failing with a 501 on the sender address; the same rejection on a
// notification produced nothing an operator could grep for.
func TestDispatch_EmailSendFailureIsLoggedWithRecipientAndEvent(t *testing.T) {
	readLog := captureLog(t)

	wsID := uuid.New()
	subscriber := uuid.New()

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{emailPref(wsID, subscriber)},
		members: map[uuid.UUID]bool{subscriber: true},
	}
	mailer := &failingEmailService{}
	svc := NewNotificationService(repo,
		WithEmailService(mailer, &fakeUserRepoForEmail{
			emails: map[uuid.UUID]string{subscriber: "member@example.com"},
		}, "https://mesh.example.com"),
	).(*notificationService)

	svc.dispatch(commentEvent(wsID))
	waitFor(t, func() bool { return strings.Contains(readLog(), "NOT DELIVERED") })

	out := readLog()
	assert.Contains(t, out, "[notification][email] NOT DELIVERED")
	assert.Contains(t, out, "member@example.com", "the refused recipient is the point of the line")
	assert.Contains(t, out, "comment.created")
}

// TestDispatch_EmailAddressResolutionFailureIsLogged: the subscriber has no
// custom address and no account email, so there is nowhere to send. Previously
// this returned from the goroutine with a log line that named neither the event
// nor the workspace.
func TestDispatch_EmailAddressResolutionFailureIsLogged(t *testing.T) {
	readLog := captureLog(t)

	wsID := uuid.New()
	subscriber := uuid.New()

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{emailPref(wsID, subscriber)},
		members: map[uuid.UUID]bool{subscriber: true},
	}
	svc := NewNotificationService(repo,
		// The user repo knows nobody, so GetByID fails.
		WithEmailService(&fakeEmailService{enabled: true}, &fakeUserRepoForEmail{}, ""),
	).(*notificationService)

	svc.dispatch(commentEvent(wsID))
	waitFor(t, func() bool { return strings.Contains(readLog(), "NOT DELIVERED") })

	out := readLog()
	assert.Contains(t, out, "no usable address")
	assert.Contains(t, out, subscriber.String())
}

// TestDispatch_MentionEventReachesTheMentionedUsersEmail is the delivery half of
// the mention fix: task.mentioned is a real dispatchable event now, and an email
// subscriber who selected it gets mail.
func TestDispatch_MentionEventReachesTheMentionedUsersEmail(t *testing.T) {
	wsID := uuid.New()
	mentioned := uuid.New()
	taskID := uuid.New()

	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{mentionEmailPref(wsID, mentioned)},
		members: map[uuid.UUID]bool{mentioned: true},
	}
	mailer := &fakeEmailService{enabled: true}
	svc := NewNotificationService(repo,
		WithEmailService(mailer, &fakeUserRepoForEmail{
			emails: map[uuid.UUID]string{mentioned: "mentioned@example.com"},
		}, "https://mesh.example.com/"),
	).(*notificationService)

	svc.dispatch(domain.NotificationEvent{
		WorkspaceID:  wsID,
		TaskID:       &taskID,
		TargetUserID: &mentioned,
		EventType:    "task.mentioned",
		Title:        "Ann mentioned you on: Ship the thing",
		Body:         "@bob can you look at this",
	})

	waitFor(t, func() bool { return len(mailer.recipients()) > 0 })
	require.Equal(t, []string{"mentioned@example.com"}, mailer.recipients())

	mailer.mu.Lock()
	sent := mailer.sent[0]
	mailer.mu.Unlock()

	assert.Equal(t, "Ann mentioned you on: Ship the thing", sent.subject)
	assert.Contains(t, sent.body, "https://mesh.example.com/t/"+taskID.String(),
		"the email must carry a direct link to the task")
}

// TestDispatch_MentionIsNotBroadcastToOtherSubscribers: a mention names one
// person. Everybody else's task.mentioned subscription is not an instruction to
// send them the comment body.
func TestDispatch_MentionIsNotBroadcastToOtherSubscribers(t *testing.T) {
	wsID := uuid.New()
	mentioned := uuid.New()
	bystander := uuid.New()

	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{
			mentionEmailPref(wsID, mentioned),
			mentionEmailPref(wsID, bystander),
		},
		members: map[uuid.UUID]bool{mentioned: true, bystander: true},
	}
	mailer := &fakeEmailService{enabled: true}
	svc := NewNotificationService(repo,
		WithEmailService(mailer, &fakeUserRepoForEmail{emails: map[uuid.UUID]string{
			mentioned: "mentioned@example.com",
			bystander: "bystander@example.com",
		}}, "https://mesh.example.com"),
	).(*notificationService)

	taskID := uuid.New()
	svc.dispatch(domain.NotificationEvent{
		WorkspaceID:  wsID,
		TaskID:       &taskID,
		TargetUserID: &mentioned,
		EventType:    "task.mentioned",
		Title:        "mentioned",
		Body:         "text only the mentioned person should see",
	})

	waitFor(t, func() bool { return len(mailer.recipients()) > 0 })
	time.Sleep(50 * time.Millisecond) // give a wrong second send a chance to land
	assert.Equal(t, []string{"mentioned@example.com"}, mailer.recipients())
}

// failingEmailService is a configured mailer whose transport refuses.
type failingEmailService struct{}

func (f *failingEmailService) Enabled() bool { return true }
func (f *failingEmailService) SendInvite(context.Context, string, string, string) error {
	return nil
}
func (f *failingEmailService) SendNotification(context.Context, string, string, string) error {
	return errSMTPRefused
}

// errSMTPRefused stands in for the transport rejection an operator actually
// sees — e.g. the 501 the partner instance's mail server returned on the
// sender address.
var errSMTPRefused = errors.New("501 Bad sender address syntax")

var _ EmailService = (*failingEmailService)(nil)

// waitFor polls cond until it holds or the test times out — the fan-out is
// deliberately asynchronous, so assertions have to wait for it rather than
// assume it already ran.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}
