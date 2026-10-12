package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// S3 of the card-hang epic (#da77a9df, item 6). A mention of the card's own
// assignee while the card is in_progress/review used to be recorded as
// skipped/status_not_fed: the author read "not delivered" on a comment that
// sits in the recipient's own thread. The verdict now says what is true —
// it lies in the thread — and says just as plainly that nobody was woken, so
// an author does not take "delivered" for "notified". The rule that a mention
// never wakes a fiddler lane is not touched: these tests pin that no status
// move happens either.

func (e liftEnv) addStatus(cat domain.StatusCategory) uuid.UUID {
	id := uuid.New()
	e.svc.statusRepo.(*MockTaskStatusRepository).items[id] = &domain.TaskStatus{
		ID: id, ProjectID: e.projID, Category: cat, Name: string(cat),
	}
	return id
}

// Cases shaped like the three the epic names: a lead's active card mentioned
// by an agent (in_progress and review), and the same card mentioned by a
// person. Each must end as a thread-only verdict with the status untouched.
func TestThreadOnlyDelivery_ActiveCardOfRecipient(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cat    domain.StatusCategory
		author domain.ActorType
	}{
		{"agent mentions assignee on in_progress card", domain.StatusCategoryInProgress, domain.ActorTypeAgent},
		{"agent mentions assignee on review card", domain.StatusCategoryReview, domain.ActorTypeAgent},
		{"person mentions assignee on in_progress card", domain.StatusCategoryInProgress, domain.ActorTypeUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setupLiftEnv(t)
			task := e.seed(e.addStatus(tc.cat), nil)
			before := task.StatusID

			c := e.comment(t, task.ID, tc.author, "@howard посмотри, пожалуйста")

			row := deliveryFor(c, "howard")
			require.NotNil(t, row)
			assert.Equal(t, domain.DeliveryDelivered, row.Outcome)
			assert.Equal(t, domain.ReasonThreadOnly, row.Reason,
				"must not read as task_queue: the card is not in the feed the lane polls")
			assert.Equal(t, domain.ChannelThread, row.Channel)
			assert.Contains(t, row.Hint, "starts no turn", "the author must be told no turn is started for the lane")

			assert.Empty(t, e.mover.calls(), "a mention must not move the card")
			assert.Equal(t, before, e.taskRepo.items[task.ID].StatusID, "status must stay as is")
		})
	}
}

// Negative controls: the new verdict must not leak onto cases that are not
// "recipient's own active card".
func TestThreadOnlyDelivery_DoesNotApplyElsewhere(t *testing.T) {
	// Backlog from an agent stays skipped (Garfield's decision on #da77a9df).
	e := setupLiftEnv(t)
	task := e.seed(e.backlogID, nil)
	c := e.comment(t, task.ID, domain.ActorTypeAgent, "@howard дорабатывай")
	row := deliveryFor(c, "howard")
	require.NotNil(t, row)
	assert.Equal(t, domain.DeliverySkipped, row.Outcome)
	assert.Equal(t, domain.ReasonStatusNotFed, row.Reason)

	// Done stays skipped: shipped work is not reopened or "read" by a comment.
	task = e.seed(e.doneID, nil)
	c = e.comment(t, task.ID, domain.ActorTypeAgent, "@howard дорабатывай")
	row = deliveryFor(c, "howard")
	require.NotNil(t, row)
	assert.Equal(t, domain.ReasonStatusNotFed, row.Reason)

	// In-progress card of SOMEBODY ELSE: not their thread.
	task = e.seed(e.addStatus(domain.StatusCategoryInProgress), func(tk *domain.Task) {
		tk.AssigneeID = &e.other.ID
	})
	c = e.comment(t, task.ID, domain.ActorTypeUser, "@howard глянь")
	row = deliveryFor(c, "howard")
	require.NotNil(t, row)
	assert.Equal(t, domain.DeliverySkipped, row.Outcome)
	assert.Equal(t, domain.ReasonNotAssignee, row.Reason)
}

func TestDecideDelivery_ThreadOnly_ActiveStatusesOnly(t *testing.T) {
	live := agentWithHeartbeat(time.Second)
	for cat, want := range map[string]string{
		"in_progress": domain.ReasonThreadOnly,
		"review":      domain.ReasonThreadOnly,
		"backlog":     domain.ReasonStatusNotFed,
		"done":        domain.ReasonStatusNotFed,
		"cancelled":   domain.ReasonStatusNotFed,
		"":            domain.ReasonStatusNotFed,
	} {
		_, reason, _, _ := decideDelivery(deliveryFacts{
			Slug: "howard", Agent: live, IsAssignee: true, StatusCategory: cat,
			Presence: domain.ComputedStatusOnline,
		})
		assert.Equal(t, want, reason, "category %q", cat)
	}
}
