package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No //go:build integration tag — same convention as the other *_db_test.go
// files here: the test runs whenever a Postgres is reachable and skips loudly
// when one is not.
//
// What this file is for: the delivery record's guarantees live half in Go and
// half in the table. A verdict with a blank reason would satisfy every Go test
// in internal/service (they assert on the branches that exist) and still be
// the exact thing the feature promises cannot happen. Only the database can
// refuse it, so only a database test can show the refusal.

func deliveryOutcomeTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("Postgres at %s not accepting connections, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newDeliveryOutcomeComment builds the workspace→project→status→task→comment
// chain and returns the comment id the outcome rows hang off.
func newDeliveryOutcomeComment(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	ws := &domain.Workspace{
		ID: uuid.New(), Name: "delivery-ws", Slug: "delivery-ws-" + suffix, OwnerID: uuid.New(),
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))

	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "delivery-proj",
		Slug: "delivery-proj-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, proj))

	status := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "Open", Slug: "open",
		Color: "#00FF00", Position: 0, Category: domain.StatusCategoryTodo, IsDefault: true,
	}
	require.NoError(t, NewTaskStatusRepo(db).Create(ctx, status))

	task := &domain.Task{
		ID: uuid.New(), ProjectID: proj.ID, StatusID: status.ID,
		Title: "delivery outcome fixture", AssigneeType: domain.AssigneeTypeUnassigned,
		Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	}
	require.NoError(t, NewTaskRepo(db).Create(ctx, task))

	c := &domain.Comment{
		ID: uuid.New(), TaskID: task.ID, AuthorID: uuid.New(),
		AuthorType: domain.ActorTypeAgent, Body: "@somebody take a look",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, NewCommentRepo(db).Create(ctx, c))
	return c.ID
}

// The promise AC1 makes — a skipped or failed verdict always names a reason —
// enforced where it cannot be bypassed. A Go-side guard would only cover the
// call sites that exist today; this covers every call site there will ever be.
func TestDeliveryOutcomeDB_BlankReasonIsRefusedByTheDatabase(t *testing.T) {
	db := deliveryOutcomeTestDB(t)
	ctx := context.Background()
	commentID := newDeliveryOutcomeComment(t, db)
	repo := NewCommentDeliveryOutcomeRepo(db)

	// Positive control first: the same row with a reason inserts cleanly, so a
	// failure below is attributable to the blank reason and not to the fixture.
	require.NoError(t, repo.InsertBatch(ctx, []domain.CommentDeliveryOutcome{{
		CommentID: commentID, RecipientSlug: "control", RecipientKind: domain.RecipientKindUnknown,
		Outcome: domain.DeliverySkipped, Reason: domain.ReasonRecipientUnknown,
		Channel: domain.ChannelNone, RecipientPresence: "offline", DecidedAt: time.Now(),
	}}), "control row with a named reason must insert")

	for _, blank := range []string{"", "   ", "\t\n"} {
		err := repo.InsertBatch(ctx, []domain.CommentDeliveryOutcome{{
			CommentID: commentID, RecipientSlug: "blank-" + uuid.New().String()[:6],
			RecipientKind: domain.RecipientKindUnknown, Outcome: domain.DeliverySkipped,
			Reason: blank, Channel: domain.ChannelNone,
			RecipientPresence: "offline", DecidedAt: time.Now(),
		}})
		require.Error(t, err, "a verdict with reason %q must be refused", blank)
		assert.Contains(t, strings.ToLower(err.Error()), "reason_check",
			"refusal must come from the reason constraint, not from something incidental")
	}
}

// An unresolved handle has no recipient id, and the table must store that as
// the recorded fact it is. A NOT NULL here would push the caller back into
// inventing a placeholder id, which is how the miss became invisible in
// comment_mentions in the first place.
func TestDeliveryOutcomeDB_UnresolvedHandleStoresNullRecipient(t *testing.T) {
	db := deliveryOutcomeTestDB(t)
	ctx := context.Background()
	commentID := newDeliveryOutcomeComment(t, db)
	repo := NewCommentDeliveryOutcomeRepo(db)

	require.NoError(t, repo.InsertBatch(ctx, []domain.CommentDeliveryOutcome{{
		CommentID: commentID, RecipientSlug: "daedalus", RecipientID: nil,
		RecipientKind: domain.RecipientKindUnknown, Outcome: domain.DeliverySkipped,
		Reason: domain.ReasonRecipientUnknown, Channel: domain.ChannelNone,
		RecipientPresence: "offline", DecidedAt: time.Now(),
	}}))

	byComment, err := repo.ListByCommentIDs(ctx, []uuid.UUID{commentID})
	require.NoError(t, err)
	rows := byComment[commentID]
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].RecipientID)
	assert.Equal(t, "daedalus", rows[0].RecipientSlug)
	assert.Equal(t, domain.ReasonRecipientUnknown, rows[0].Reason)
}

// Editing a comment re-runs the mention pass. The second verdict is the
// current one and must replace the first, or the record would freeze at
// whatever was true the first time and quietly go stale.
func TestDeliveryOutcomeDB_ReRecordingAHandleReplacesTheEarlierVerdict(t *testing.T) {
	db := deliveryOutcomeTestDB(t)
	ctx := context.Background()
	commentID := newDeliveryOutcomeComment(t, db)
	repo := NewCommentDeliveryOutcomeRepo(db)

	first := domain.CommentDeliveryOutcome{
		CommentID: commentID, RecipientSlug: "linus", RecipientKind: domain.RecipientKindAgent,
		Outcome: domain.DeliverySkipped, Reason: domain.ReasonNoQueuePath,
		Channel: domain.ChannelNone, RecipientPresence: "online", DecidedAt: time.Now(),
	}
	require.NoError(t, repo.InsertBatch(ctx, []domain.CommentDeliveryOutcome{first}))

	second := first
	second.Outcome = domain.DeliveryDelivered
	second.Reason = domain.ReasonTaskQueue
	second.Channel = domain.ChannelTaskQueue
	second.DecidedAt = time.Now().Add(time.Minute)
	require.NoError(t, repo.InsertBatch(ctx, []domain.CommentDeliveryOutcome{second}))

	byComment, err := repo.ListByCommentIDs(ctx, []uuid.UUID{commentID})
	require.NoError(t, err)
	rows := byComment[commentID]
	require.Len(t, rows, 1, "re-recording one handle must update in place, not add a row")
	assert.Equal(t, domain.DeliveryDelivered, rows[0].Outcome)
	assert.Equal(t, domain.ReasonTaskQueue, rows[0].Reason)
}

// MarkFailed is the async downgrade: dispatch discovers the durable write
// failed long after the verdict was recorded optimistically.
func TestDeliveryOutcomeDB_MarkFailedDowngradesAndDoesNotResurrect(t *testing.T) {
	db := deliveryOutcomeTestDB(t)
	ctx := context.Background()
	commentID := newDeliveryOutcomeComment(t, db)
	repo := NewCommentDeliveryOutcomeRepo(db)

	require.NoError(t, repo.InsertBatch(ctx, []domain.CommentDeliveryOutcome{{
		CommentID: commentID, RecipientSlug: "bart", RecipientKind: domain.RecipientKindAgent,
		Outcome: domain.DeliveryDelivered, Reason: domain.ReasonTaskQueue,
		Channel: domain.ChannelTaskQueue, RecipientPresence: "online", DecidedAt: time.Now(),
	}}))

	require.NoError(t, repo.MarkFailed(ctx, commentID, "bart", domain.RecipientKindAgent, domain.ReasonEventPersistFailed))

	byComment, err := repo.ListByCommentIDs(ctx, []uuid.UUID{commentID})
	require.NoError(t, err)
	require.Len(t, byComment[commentID], 1)
	assert.Equal(t, domain.DeliveryFailed, byComment[commentID][0].Outcome)
	assert.Equal(t, domain.ReasonEventPersistFailed, byComment[commentID][0].Reason)

	// Idempotent under a retry, and a second call must not blank the reason.
	require.NoError(t, repo.MarkFailed(ctx, commentID, "bart", domain.RecipientKindAgent, domain.ReasonEventPersistFailed))
	byComment, err = repo.ListByCommentIDs(ctx, []uuid.UUID{commentID})
	require.NoError(t, err)
	require.Len(t, byComment[commentID], 1)
	assert.Equal(t, domain.DeliveryFailed, byComment[commentID][0].Outcome)
	assert.NotEmpty(t, byComment[commentID][0].Reason)

	// A handle nobody recorded is a no-op, not an error and not a new row.
	require.NoError(t, repo.MarkFailed(ctx, commentID, "never-recorded", domain.RecipientKindAgent, domain.ReasonEventPersistFailed))
	byComment, err = repo.ListByCommentIDs(ctx, []uuid.UUID{commentID})
	require.NoError(t, err)
	assert.Len(t, byComment[commentID], 1, "MarkFailed must not invent rows for unknown handles")
}

// MarkFailed must address the row by kind too, not just comment+slug — a
// colliding slug can carry both an agent row and a user row for the same
// comment_id + recipient_slug, and downgrading the wrong one would silently
// mark the untouched side as failed while leaving the actually-broken side
// standing as delivered.
func TestDeliveryOutcomeDB_MarkFailedTargetsOnlyItsOwnKind(t *testing.T) {
	db := deliveryOutcomeTestDB(t)
	ctx := context.Background()
	commentID := newDeliveryOutcomeComment(t, db)
	repo := NewCommentDeliveryOutcomeRepo(db)

	require.NoError(t, repo.InsertBatch(ctx, []domain.CommentDeliveryOutcome{
		{
			CommentID: commentID, RecipientSlug: "hugh", RecipientKind: domain.RecipientKindAgent,
			Outcome: domain.DeliveryDelivered, Reason: domain.ReasonTaskQueue,
			Channel: domain.ChannelTaskQueue, RecipientPresence: "online", DecidedAt: time.Now(),
		},
		{
			CommentID: commentID, RecipientSlug: "hugh", RecipientKind: domain.RecipientKindUser,
			Outcome: domain.DeliveryDelivered, Reason: domain.ReasonNotification,
			Channel: domain.ChannelNotification, RecipientPresence: "unknown", DecidedAt: time.Now(),
		},
	}))

	require.NoError(t, repo.MarkFailed(ctx, commentID, "hugh", domain.RecipientKindAgent, domain.ReasonEventPersistFailed))

	byComment, err := repo.ListByCommentIDs(ctx, []uuid.UUID{commentID})
	require.NoError(t, err)
	rows := byComment[commentID]
	require.Len(t, rows, 2, "the downgrade must not remove or merge either row")

	var agentRow, userRow *domain.CommentDeliveryOutcome
	for i := range rows {
		switch rows[i].RecipientKind {
		case domain.RecipientKindAgent:
			agentRow = &rows[i]
		case domain.RecipientKindUser:
			userRow = &rows[i]
		}
	}
	require.NotNil(t, agentRow)
	require.NotNil(t, userRow)
	assert.Equal(t, domain.DeliveryFailed, agentRow.Outcome, "the kind actually named must be downgraded")
	assert.Equal(t, domain.DeliveryDelivered, userRow.Outcome, "the other kind sharing the slug must be untouched")
}

// This is the acceptance criterion the addendum names directly: a comment
// naming a colliding slug must leave TWO rows on a real database, one per
// side, and re-recording (an edit re-running the mention pass) must update
// each in place rather than one clobbering the other.
//
// RED CONTROL (run manually before this migration existed, recorded here so
// the claim is checkable and not just asserted): against the pre-20260827001
// schema (PRIMARY KEY (comment_id, recipient_slug)), this same InsertBatch
// call left exactly ONE row — RecipientKind == "user", the second write in
// the batch — reproducing the exact defect from task f4f47938's addendum
// comment. `require.Len(t, rows, 2, ...)` below failed with "1 != 2" against
// that schema and passes against this migration.
func TestDeliveryOutcomeDB_CollidingSlugPersistsBothKinds(t *testing.T) {
	db := deliveryOutcomeTestDB(t)
	ctx := context.Background()
	commentID := newDeliveryOutcomeComment(t, db)
	repo := NewCommentDeliveryOutcomeRepo(db)

	agentID, userID := uuid.New(), uuid.New()

	// One batch, same shape notifyMentions produces for a colliding slug: two
	// rows, same comment_id + recipient_slug, different recipient_kind.
	require.NoError(t, repo.InsertBatch(ctx, []domain.CommentDeliveryOutcome{
		{
			CommentID: commentID, RecipientSlug: "hugh", RecipientID: &agentID,
			RecipientKind: domain.RecipientKindAgent, Outcome: domain.DeliverySkipped,
			Reason: domain.ReasonNoQueuePath, Channel: domain.ChannelNone,
			RecipientPresence: "online", DecidedAt: time.Now(),
		},
		{
			CommentID: commentID, RecipientSlug: "hugh", RecipientID: &userID,
			RecipientKind: domain.RecipientKindUser, Outcome: domain.DeliveryDelivered,
			Reason: domain.ReasonNotification, Channel: domain.ChannelNotification,
			RecipientPresence: "unknown", DecidedAt: time.Now(),
		},
	}))

	byComment, err := repo.ListByCommentIDs(ctx, []uuid.UUID{commentID})
	require.NoError(t, err)
	rows := byComment[commentID]
	require.Len(t, rows, 2, "a colliding slug must leave one row per addressed party — this is the bug the addendum found: the second write was silently upserting over the first")

	var agentRow, userRow *domain.CommentDeliveryOutcome
	for i := range rows {
		switch rows[i].RecipientKind {
		case domain.RecipientKindAgent:
			agentRow = &rows[i]
		case domain.RecipientKindUser:
			userRow = &rows[i]
		}
	}
	require.NotNil(t, agentRow, "agent side of the collision has no recorded verdict")
	require.NotNil(t, userRow, "human side of the collision has no recorded verdict")
	assert.Equal(t, agentID, *agentRow.RecipientID)
	assert.Equal(t, userID, *userRow.RecipientID)

	// Re-recording (an edit re-running the mention pass) must update each
	// side in place, not add a third row and not merge the two.
	updatedAgentID := agentID
	require.NoError(t, repo.InsertBatch(ctx, []domain.CommentDeliveryOutcome{{
		CommentID: commentID, RecipientSlug: "hugh", RecipientID: &updatedAgentID,
		RecipientKind: domain.RecipientKindAgent, Outcome: domain.DeliveryDelivered,
		Reason: domain.ReasonTaskQueue, Channel: domain.ChannelTaskQueue,
		RecipientPresence: "online", DecidedAt: time.Now().Add(time.Minute),
	}}))

	byComment, err = repo.ListByCommentIDs(ctx, []uuid.UUID{commentID})
	require.NoError(t, err)
	rows = byComment[commentID]
	require.Len(t, rows, 2, "updating one side of the collision must not add a row or drop the other side")
	for _, r := range rows {
		if r.RecipientKind == domain.RecipientKindAgent {
			assert.Equal(t, domain.DeliveryDelivered, r.Outcome, "the updated side must reflect the new verdict")
		} else {
			assert.Equal(t, domain.DeliveryDelivered, r.Outcome, "the untouched side must be unaffected by the other side's update")
		}
	}
}

// Deleting the comment must take its delivery record with it — the record is
// about that comment and means nothing without it.
func TestDeliveryOutcomeDB_RecordIsCascadedWithItsComment(t *testing.T) {
	db := deliveryOutcomeTestDB(t)
	ctx := context.Background()
	commentID := newDeliveryOutcomeComment(t, db)
	repo := NewCommentDeliveryOutcomeRepo(db)

	require.NoError(t, repo.InsertBatch(ctx, []domain.CommentDeliveryOutcome{{
		CommentID: commentID, RecipientSlug: "wally", RecipientKind: domain.RecipientKindAgent,
		Outcome: domain.DeliverySkipped, Reason: domain.ReasonRecipientOffline,
		Channel: domain.ChannelNone, RecipientPresence: "offline", DecidedAt: time.Now(),
	}}))

	_, err := db.ExecContext(ctx, `DELETE FROM comments WHERE id = $1`, commentID)
	require.NoError(t, err)

	var count int
	require.NoError(t, db.GetContext(ctx, &count,
		`SELECT count(*) FROM comment_delivery_outcomes WHERE comment_id = $1`, commentID))
	assert.Zero(t, count)
}
