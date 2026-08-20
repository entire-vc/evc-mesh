package service

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// An event addressed to one person is dropped by a `continue` in every channel
// loop, so "they have no enabled preference row" and "there was nobody to tell"
// produce byte-identical output: nothing at all. That silence is what let a
// document watcher be recorded as a recipient of a notification that was never
// going to arrive — a preference row exists only if the person visited
// Notification Settings, and on prod (2026-08-20) exactly one existed in the
// whole database, in the wrong workspace.
//
// These tests assert the drop now says so, with the reason. They do not assert
// anything about who is reached: that is deliberately unchanged.

func targetedEvent(wsID, target uuid.UUID, eventType string) domain.NotificationEvent {
	return domain.NotificationEvent{
		WorkspaceID:  wsID,
		TargetUserID: &target,
		EventType:    eventType,
		Title:        "Billing was edited",
		Body:         "Alice made changes",
	}
}

func TestDispatch_TargetWithNoPreferenceIsReportedNotSilent(t *testing.T) {
	wsID := uuid.New()
	watcher := uuid.New()

	// The watcher is a member in good standing and has a row — for another event
	// type. This is the shape that used to vanish without trace.
	repo := &fakeNotificationRepo{
		prefs:   []domain.NotificationPreference{webPushPref(wsID, watcher)},
		members: map[uuid.UUID]bool{watcher: true},
	}
	svc := NewNotificationService(repo).(*notificationService)

	read := captureLog(t)
	svc.dispatch(targetedEvent(wsID, watcher, DocumentChangedEvent))
	out := read()

	require.Empty(t, repo.notifiedUsers(), "nothing was delivered — that part is unchanged")
	assert.Contains(t, out, "reached no channel")
	assert.Contains(t, out, watcher.String(), "the log has to name who was missed")
	assert.Contains(t, out, DocumentChangedEvent)
}

func TestDispatch_TargetOutsideTheWorkspaceIsReportedNotSilent(t *testing.T) {
	wsID := uuid.New()
	stranger := uuid.New()

	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{{
			ID: uuid.New(), WorkspaceID: wsID, UserID: &stranger, Channel: "web_push",
			Events: pq.StringArray{DocumentChangedEvent}, IsEnabled: true,
		}},
		members: map[uuid.UUID]bool{},
	}
	svc := NewNotificationService(repo).(*notificationService)

	read := captureLog(t)
	svc.dispatch(targetedEvent(wsID, stranger, DocumentChangedEvent))
	out := read()

	require.Empty(t, repo.notifiedUsers(), "fail-closed on membership is unchanged")
	assert.Contains(t, out, "not a member")
	assert.Contains(t, out, stranger.String())
}

// TestDispatch_ADeliveredTargetIsNotReportedAsMissed is the control without
// which the two above prove only that the code can print. A log line that fires
// on every dispatch is noise, and noise is how a real one gets ignored.
func TestDispatch_ADeliveredTargetIsNotReportedAsMissed(t *testing.T) {
	wsID := uuid.New()
	watcher := uuid.New()

	repo := &fakeNotificationRepo{
		prefs: []domain.NotificationPreference{{
			ID: uuid.New(), WorkspaceID: wsID, UserID: &watcher, Channel: "web_push",
			Events: pq.StringArray{DocumentChangedEvent}, IsEnabled: true,
		}},
		members: map[uuid.UUID]bool{watcher: true},
	}
	svc := NewNotificationService(repo).(*notificationService)

	read := captureLog(t)
	svc.dispatch(targetedEvent(wsID, watcher, DocumentChangedEvent))
	out := read()

	assert.Equal(t, []uuid.UUID{watcher}, repo.notifiedUsers())
	assert.False(t, strings.Contains(out, "reached no channel") || strings.Contains(out, "not a member"),
		"a delivery that worked must not be reported as a miss: %q", out)
}

// TestDispatch_AnUntargetedEventIsNotReported — a broadcast has no one address
// to miss, so it has nothing to report either.
func TestDispatch_AnUntargetedEventIsNotReported(t *testing.T) {
	wsID := uuid.New()
	repo := &fakeNotificationRepo{members: map[uuid.UUID]bool{}}
	svc := NewNotificationService(repo).(*notificationService)

	read := captureLog(t)
	svc.dispatch(commentEvent(wsID))
	out := read()

	assert.NotContains(t, out, "reached no channel")
}
