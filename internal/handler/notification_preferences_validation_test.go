package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PUT /notifications/preferences used to store whatever channel/events strings
// arrived, with no whitelist check — see the comment on deliverableChannels in
// notification_handler.go for the incident that motivated the fix (a typo'd or
// invented channel/event saved 200, echoed back, and was silently skipped by
// every dispatch fan-out loop forever). These tests exercise the validation
// added at the edge, using putPreferencesNoTenancyGuard (defined in
// notification_email_test.go) since the tenancy guard itself is out of scope
// here and already covered by notification_prefs_tenancy_test.go.

// --- channel whitelist -------------------------------------------------------

// TestUpdatePreferences_UnknownChannelIsRejected covers a channel nobody ever
// implemented (as opposed to a typo of a real one, covered separately below) —
// the failure mode the whitelist exists to catch.
func TestUpdatePreferences_UnknownChannelIsRejected(t *testing.T) {
	svc := &recordingNotificationService{}
	body := `{"workspace_id":"` + uuid.New().String() + `","channel":"slack","events":["comment.created"]}`

	rec := putPreferencesNoTenancyGuard(t, svc, body)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, svc.upserted, "an unknown channel must not reach UpsertPreferences")
	// The error names the supported channels so the caller can self-correct
	// instead of guessing at the whitelist.
	assert.Contains(t, rec.Body.String(), "web_push")
	assert.Contains(t, rec.Body.String(), "email")
}

// TestUpdatePreferences_TypoedChannelIsRejected is the exact repro from the
// incident: "e-mail" is close enough to "email" to look right in a UI dropdown
// bug, but the dispatcher only ever branches on "email" — so the old
// store-anything behavior produced a subscription that could never deliver,
// with no record anywhere that it never could.
func TestUpdatePreferences_TypoedChannelIsRejected(t *testing.T) {
	svc := &recordingNotificationService{}
	body := `{"workspace_id":"` + uuid.New().String() + `","channel":"e-mail","events":["comment.created"]}`

	rec := putPreferencesNoTenancyGuard(t, svc, body)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, svc.upserted)
}

// TestUpdatePreferences_EachValidChannelIsAccepted is the flip side of the two
// rejection tests above: the whitelist must not accidentally exclude one of
// the channels dispatch actually knows how to deliver on.
func TestUpdatePreferences_EachValidChannelIsAccepted(t *testing.T) {
	for channel := range deliverableChannels {
		t.Run(channel, func(t *testing.T) {
			svc := &recordingNotificationService{}
			body := `{"workspace_id":"` + uuid.New().String() + `","channel":"` + channel + `","events":["comment.created"]}`
			if channel == "telegram" {
				// telegram has its own required-field validation (username) unrelated
				// to the whitelist under test here; disable it by targeting a save
				// that does not enable the channel.
				body = `{"workspace_id":"` + uuid.New().String() + `","channel":"telegram","events":["comment.created"],"is_enabled":false}`
			}

			rec := putPreferencesNoTenancyGuard(t, svc, body)

			require.Equal(t, http.StatusOK, rec.Code, "channel %q should be accepted, got body %s", channel, rec.Body.String())
			require.NotNil(t, svc.upserted)
			assert.Equal(t, channel, svc.upserted.Channel)
		})
	}
}

// TestUpdatePreferences_EmptyChannelDefaultsToWebPush: an absent channel is not
// an error — it means "the default in-app bell", not "unspecified/invalid".
func TestUpdatePreferences_EmptyChannelDefaultsToWebPush(t *testing.T) {
	svc := &recordingNotificationService{}
	body := `{"workspace_id":"` + uuid.New().String() + `","events":["comment.created"]}`

	rec := putPreferencesNoTenancyGuard(t, svc, body)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, svc.upserted)
	assert.Equal(t, "web_push", svc.upserted.Channel)
}

// --- events whitelist ---------------------------------------------------------

// TestUpdatePreferences_UnknownEventIsRejected: subscribing to an event no
// producer ever emits is the same dead end as an unknown channel — stored,
// echoed back, and never delivered, just with the failure mode hidden one
// layer deeper (per-event rather than per-channel).
func TestUpdatePreferences_UnknownEventIsRejected(t *testing.T) {
	svc := &recordingNotificationService{}
	body := `{"workspace_id":"` + uuid.New().String() + `","channel":"web_push","events":["task.exploded"]}`

	rec := putPreferencesNoTenancyGuard(t, svc, body)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, svc.upserted, "an unknown event must not reach UpsertPreferences")
	assert.Contains(t, rec.Body.String(), "task.exploded")
}

// TestUpdatePreferences_UnknownEventAmongValidOnesIsRejected: the whole request
// is refused, not just the one bad entry silently dropped — a partial save
// would hide from the caller that "task.exploded" was never going to arrive.
func TestUpdatePreferences_UnknownEventAmongValidOnesIsRejected(t *testing.T) {
	svc := &recordingNotificationService{}
	body := `{"workspace_id":"` + uuid.New().String() + `","channel":"web_push","events":["comment.created","task.exploded","task.assigned"]}`

	rec := putPreferencesNoTenancyGuard(t, svc, body)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, svc.upserted)
}

// TestUpdatePreferences_EmptyEventsDefaultsToAllDispatchable: the caller asked
// to be notified and named no exclusions, so an empty list means "everything",
// not "nothing" — this is what stops the settings page from silently
// subscribing someone to zero events on their very first save.
func TestUpdatePreferences_EmptyEventsDefaultsToAllDispatchable(t *testing.T) {
	svc := &recordingNotificationService{}
	body := `{"workspace_id":"` + uuid.New().String() + `","channel":"web_push"}`

	rec := putPreferencesNoTenancyGuard(t, svc, body)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, svc.upserted)
	assert.ElementsMatch(t, sortedEvents(), []string(svc.upserted.Events),
		"an empty events list must default to every dispatchable event, not a subset that quietly drops some")
	assert.Len(t, svc.upserted.Events, len(dispatchableEvents))
}

// TestUpdatePreferences_ValidEventSubsetIsStoredUnchanged: naming a subset is
// an explicit choice and must survive intact — no silent expansion or
// reordering that would make the stored row not match what was requested.
func TestUpdatePreferences_ValidEventSubsetIsStoredUnchanged(t *testing.T) {
	svc := &recordingNotificationService{}
	body := `{"workspace_id":"` + uuid.New().String() + `","channel":"web_push","events":["task.mentioned","comment.created"]}`

	rec := putPreferencesNoTenancyGuard(t, svc, body)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, svc.upserted)
	assert.Equal(t, []string{"task.mentioned", "comment.created"}, []string(svc.upserted.Events))
}

// --- sortedKeys / sortedChannels / sortedEvents -------------------------------

// TestSortedKeys_StableSortedOrder: the error messages built from these slices
// must not reshuffle between identical requests — sort.Strings is what
// guarantees that, and this pins the guarantee against a future change to the
// whitelist maps that happens to rely on Go's randomized map iteration order.
func TestSortedKeys_StableSortedOrder(t *testing.T) {
	set := map[string]bool{"zebra": true, "apple": true, "mango": true}

	got := sortedKeys(set)

	assert.Equal(t, []string{"apple", "mango", "zebra"}, got)
	// Called twice to guard against relying on map iteration order (which Go
	// deliberately randomizes) rather than the explicit sort.
	assert.Equal(t, sortedKeys(set), sortedKeys(set))
}

func TestSortedChannels_MatchesWhitelistSorted(t *testing.T) {
	got := sortedChannels()

	assert.Equal(t, []string{"browser_push", "email", "telegram", "web_push"}, got)
	assert.Len(t, got, len(deliverableChannels), "sortedChannels must reflect every entry in deliverableChannels")
}

func TestSortedEvents_MatchesWhitelistSorted(t *testing.T) {
	got := sortedEvents()

	assert.Equal(t, []string{
		"comment.created",
		"document.changed",
		"document.commented",
		"document.deleted",
		"document.mentioned",
		"task.assigned",
		"task.blocking_triage",
		"task.mentioned",
		"task.ready_for_review",
		"task.reviewer_assigned",
		"task.status_changed",
	}, got)
	assert.Len(t, got, len(dispatchableEvents), "sortedEvents must reflect every entry in dispatchableEvents")
}

// TestDefaultSubscribedEvents_IsAllDispatchableSorted pins defaultSubscribedEvents
// to sortedEvents() directly, independent of the HTTP round-trip covered by
// TestUpdatePreferences_EmptyEventsDefaultsToAllDispatchable above.
func TestDefaultSubscribedEvents_IsAllDispatchableSorted(t *testing.T) {
	assert.Equal(t, sortedEvents(), defaultSubscribedEvents())
}
