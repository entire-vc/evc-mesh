package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// ---------------------------------------------------------------------------
// ensureMentionDelivery (#4e1d249f)
//
// Root cause measured on prod 2026-08-23: notification_preferences had no row
// for any of the humans being @-mentioned in "❓ Blocking" comments, so every
// one of them recorded outcome skipped/no_subscription — the mention worked,
// nothing was ever reachable to receive it. These tests pin the fix: a first
// @-mention provisions an email row, an existing explicit choice (disabled,
// or already-covered by another channel) is left alone, and self-mentions
// never touch anything.
// ---------------------------------------------------------------------------

func newCommentForMention(taskID uuid.UUID, body string) *domain.Comment {
	return &domain.Comment{ID: uuid.New(), TaskID: taskID, Body: body}
}

// TestEnsureMentionDelivery_NoPriorRow_ProvisionsEnabledEmail is the core
// case this change fixes: a person who has never opened notification
// settings gets a usable channel out of being named, instead of a mention
// that is recorded and delivers to nowhere.
func TestEnsureMentionDelivery_NoPriorRow_ProvisionsEnabledEmail(t *testing.T) {
	env := setupCommentServiceWithUserMentions()

	user := &domain.User{ID: uuid.New(), Username: "pavel", Name: "Pavel"}
	env.userRepo.AddUser(env.wsID, user)

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID, Title: "Rotate the leaked key"}
	task := env.taskRepo.items[taskID]

	comment := newCommentForMention(taskID, "❓ Blocking @pavel: which value?")
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	env.svc.notifyMentions(ctx, comment, task, "", env.wsID)

	prefs := env.notifySvc.Preferences()
	require.Len(t, prefs, 1, "a first mention with no prior preferences must provision exactly one row")
	p := prefs[0]
	assert.Equal(t, "email", p.Channel)
	assert.True(t, p.IsEnabled)
	require.NotNil(t, p.UserID)
	assert.Equal(t, user.ID, *p.UserID)
	assert.Equal(t, env.wsID, p.WorkspaceID)
	assert.Contains(t, []string(p.Events), "task.mentioned")

	// And the mention now actually dispatches through NotificationService —
	// unaffected by this change, but the point of provisioning is exactly
	// that the same call now has somewhere to land.
	calls := env.notifySvc.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "task.mentioned", calls[0].EventType)
}

// TestEnsureMentionDelivery_ExistingDisabledEmailRow_NotOverridden is the
// negative control: an explicit "no email" must outrank the implicit request
// inside being named in a comment, exactly like ensureInAppDelivery already
// guarantees for Watch. Without this guard, provisioning would silently
// re-enable a channel a person turned off.
func TestEnsureMentionDelivery_ExistingDisabledEmailRow_NotOverridden(t *testing.T) {
	env := setupCommentServiceWithUserMentions()

	user := &domain.User{ID: uuid.New(), Username: "pavel", Name: "Pavel"}
	env.userRepo.AddUser(env.wsID, user)

	existingID := uuid.New()
	env.notifySvc.SeedPreference(domain.NotificationPreference{
		ID:          existingID,
		WorkspaceID: env.wsID,
		UserID:      &user.ID,
		Channel:     "email",
		Events:      []string{"task.mentioned"},
		IsEnabled:   false, // explicit prior opt-out
	})

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID}
	task := env.taskRepo.items[taskID]

	comment := newCommentForMention(taskID, "@pavel status?")
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	env.svc.notifyMentions(ctx, comment, task, "", env.wsID)

	prefs := env.notifySvc.Preferences()
	require.Len(t, prefs, 1, "the mention must not create a second row alongside the disabled one")
	assert.Equal(t, existingID, prefs[0].ID)
	assert.False(t, prefs[0].IsEnabled, "an explicit opt-out must survive being @-mentioned")
}

// TestEnsureMentionDelivery_AlreadyReachableViaAnotherChannel_NoEmailAdded:
// someone already subscribed by Telegram does not also need email pushed on
// them — mirrors ensureInAppDelivery's "someone who subscribed by email does
// not also need the bell".
func TestEnsureMentionDelivery_AlreadyReachableViaAnotherChannel_NoEmailAdded(t *testing.T) {
	env := setupCommentServiceWithUserMentions()

	user := &domain.User{ID: uuid.New(), Username: "pavel", Name: "Pavel"}
	env.userRepo.AddUser(env.wsID, user)

	env.notifySvc.SeedPreference(domain.NotificationPreference{
		ID:          uuid.New(),
		WorkspaceID: env.wsID,
		UserID:      &user.ID,
		Channel:     "telegram",
		Events:      []string{"task.mentioned"},
		IsEnabled:   true,
	})

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID}
	task := env.taskRepo.items[taskID]

	comment := newCommentForMention(taskID, "@pavel ping")
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	env.svc.notifyMentions(ctx, comment, task, "", env.wsID)

	prefs := env.notifySvc.Preferences()
	require.Len(t, prefs, 1, "already reachable via telegram — no email row should be added")
	assert.Equal(t, "telegram", prefs[0].Channel)
}

// TestEnsureMentionDelivery_ExistingEmailRowMissingEvent_NotWidened pins the
// fix for the regression found live on #4e1d249f (2026-09-06): this used to
// union task.mentioned into ANY existing email row that didn't cover it. That
// silently undid Pavel's own "только блокеры" narrowing (events pared down to
// {task.blocking_triage}) within hours of setting it — the very next plain
// @-mention re-added task.mentioned right back. There is no way to tell
// "never configured this" apart from "deliberately narrowed" from the events
// array alone, so an existing row of any shape is now left untouched, exactly
// like the already-disabled case below.
func TestEnsureMentionDelivery_ExistingEmailRowMissingEvent_NotWidened(t *testing.T) {
	env := setupCommentServiceWithUserMentions()

	user := &domain.User{ID: uuid.New(), Username: "pavel", Name: "Pavel"}
	env.userRepo.AddUser(env.wsID, user)

	existingID := uuid.New()
	env.notifySvc.SeedPreference(domain.NotificationPreference{
		ID:          existingID,
		WorkspaceID: env.wsID,
		UserID:      &user.ID,
		Channel:     "email",
		Events:      []string{"task.assigned"},
		IsEnabled:   true,
	})

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID}
	task := env.taskRepo.items[taskID]

	comment := newCommentForMention(taskID, "@pavel please look")
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	env.svc.notifyMentions(ctx, comment, task, "", env.wsID)

	prefs := env.notifySvc.Preferences()
	require.Len(t, prefs, 1, "must not create a second row alongside the existing one")
	assert.Equal(t, existingID, prefs[0].ID)
	assert.Equal(t, []string{"task.assigned"}, []string(prefs[0].Events), "an existing row's events must be left exactly as configured, not widened")
}

// TestEnsureMentionDelivery_SelfMention_DoesNotProvision: naming yourself
// must not touch your own settings — mirrors the existing self-mention
// notify guard, one layer earlier.
func TestEnsureMentionDelivery_SelfMention_DoesNotProvision(t *testing.T) {
	env := setupCommentServiceWithUserMentions()

	userID := uuid.New()
	user := &domain.User{ID: userID, Username: "pavel", Name: "Pavel"}
	env.userRepo.AddUser(env.wsID, user)

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID}
	task := env.taskRepo.items[taskID]

	comment := newCommentForMention(taskID, "@pavel note to self")
	ctx := actorctx.WithActor(context.Background(), userID, domain.ActorTypeUser)

	env.svc.notifyMentions(ctx, comment, task, "", env.wsID)

	assert.Empty(t, env.notifySvc.Preferences(), "a self-mention must not provision anything")
}

// TestEnsureMentionDelivery_PreferenceReadFails_DoesNotPanicOrProvision is
// the fail-closed counterpart: if preferences cannot be read, this must not
// crash the comment path (best-effort, like the rest of this file) and must
// not guess by writing a row anyway.
func TestEnsureMentionDelivery_PreferenceReadFails_DoesNotPanicOrProvision(t *testing.T) {
	env := setupCommentServiceWithUserMentions()

	user := &domain.User{ID: uuid.New(), Username: "pavel", Name: "Pavel"}
	env.userRepo.AddUser(env.wsID, user)
	env.notifySvc.FailPreferenceReads(assert.AnError)

	taskID := uuid.New()
	env.taskRepo.items[taskID] = &domain.Task{ID: taskID, ProjectID: env.projID}
	task := env.taskRepo.items[taskID]

	comment := newCommentForMention(taskID, "@pavel ping")
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)

	assert.NotPanics(t, func() {
		env.svc.notifyMentions(ctx, comment, task, "", env.wsID)
	})
	assert.Empty(t, env.notifySvc.Preferences(), "a failed read must not be papered over with a guessed row")
}
