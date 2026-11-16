package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// DB-backed, no build tag: skipped without Postgres. The receipt's repo
// contract on the comment insert: recorded in the insert's transaction, read
// back by FindReceipt, and a second insert under the same scope rolls back and
// names the first write. The HTTP behaviour on top of it is pinned in
// internal/handler/idempotency_receipt_db_test.go.
func TestIdempotencyRepo_ReceiptBoundToComment_DB(t *testing.T) {
	db := commentCursorTestDB(t)
	fx := newCursorTestFixture(t, db)
	comments := NewCommentRepo(db)
	repo := NewIdempotencyRepo(db)
	author := createCursorTestUser(t, db)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM idempotency_receipts WHERE task_id=$1`, fx.taskID) })

	scope := domain.IdempotencyReceiptScope{
		ActorType: domain.ActorTypeUser, ActorID: author,
		Route: domain.IdemRouteCommentCreate, TaskID: fx.taskID, Key: "k-repo",
	}
	newComment := func(body string, rc *domain.IdempotencyReceipt) *domain.Comment {
		now := time.Now().UTC()
		return &domain.Comment{
			ID: uuid.New(), TaskID: fx.taskID, AuthorID: author, AuthorType: domain.ActorTypeUser,
			Body: body, CreatedAt: now, UpdatedAt: now, Receipt: rc,
		}
	}
	count := func() int {
		var n int
		require.NoError(t, db.Get(&n, `SELECT count(*) FROM comments WHERE task_id=$1`, fx.taskID))
		return n
	}

	got, err := repo.FindReceipt(ctx, scope, "h1")
	require.NoError(t, err)
	assert.Nil(t, got, "no write under the key yet")

	rc := &domain.IdempotencyReceipt{IdempotencyReceiptScope: scope, RequestHash: "h1"}
	first := newComment("first", rc)
	require.NoError(t, comments.Create(ctx, first))

	got, err = repo.FindReceipt(ctx, scope, "h1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, first.ID, got.ResourceID)
	assert.True(t, got.SameRequest)
	var snap domain.Comment
	require.NoError(t, json.Unmarshal(got.Response, &snap))
	assert.Equal(t, first.ID, snap.ID)
	assert.Equal(t, "first", snap.Body)

	got, err = repo.FindReceipt(ctx, scope, "h-other")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.False(t, got.SameRequest, "another fingerprint under the key is not the same request")

	// A second insert under the same scope (a concurrent repeat that lost the
	// race) rolls back and names the first write.
	second := newComment("second", &domain.IdempotencyReceipt{IdempotencyReceiptScope: scope, RequestHash: "h1"})
	err = comments.Create(ctx, second)
	var written *domain.IdempotentWrite
	require.True(t, errors.As(err, &written), "got %v", err)
	assert.Equal(t, first.ID, written.ResourceID)
	assert.Equal(t, 1, count(), "the losing insert left no row")
	gone, err := comments.GetByID(ctx, second.ID)
	require.NoError(t, err)
	assert.Nil(t, gone)

	// Positive control: another key on the same task is a separate write, and
	// so is an insert with no receipt at all.
	other := scope
	other.Key = "k-repo-2"
	require.NoError(t, comments.Create(ctx, newComment("other key", &domain.IdempotencyReceipt{IdempotencyReceiptScope: other, RequestHash: "h1"})))
	require.NoError(t, comments.Create(ctx, newComment("no key", nil)))
	assert.Equal(t, 3, count())
}
