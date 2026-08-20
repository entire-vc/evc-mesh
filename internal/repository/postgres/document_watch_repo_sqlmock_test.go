package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// These tests read the SQL back rather than trusting the method names.
//
// Both of this repository's load-bearing behaviours live entirely inside the
// statements — the coalescing is a partial unique index plus an ON CONFLICT, and
// the "an automatic subscribe may not clear a mute" rule is the difference
// between two ON CONFLICT clauses. A test against an in-memory fake cannot see
// either: it would assert that a re-implementation of the query agrees with
// itself. So what is checked here is the text that goes to Postgres.

func newDocumentWatchRepoMock(t *testing.T) (*DocumentWatchRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewDocumentWatchRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

// captureWatchSQL runs fn against a mock that records the statement instead of
// matching it, and returns what the repository actually sent.
func captureWatchSQL(t *testing.T, isQuery bool, fn func(*DocumentWatchRepo)) string {
	t.Helper()

	var captured string
	rawDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
		sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = actualSQL
			return nil
		})))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })

	if isQuery {
		mock.ExpectQuery(".*").WillReturnRows(sqlmock.NewRows([]string{"x"}))
	} else {
		mock.ExpectExec(".*").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	fn(NewDocumentWatchRepo(sqlx.NewDb(rawDB, "postgres")))
	return captured
}

func TestDocumentWatchRepo_AutomaticSubscribeCannotClearAMute(t *testing.T) {
	// The rule this protects: unwatching a document you comment on has to stick.
	// If the automatic path upserted with DO UPDATE, the next comment would
	// silently re-subscribe the person who just unsubscribed, and the button
	// would look broken to exactly the people auto-subscription exists to help.
	sql := captureWatchSQL(t, false, func(r *DocumentWatchRepo) {
		_ = r.Subscribe(context.Background(), domain.DocumentWatcher{
			DocumentID: uuid.New(), WatcherID: uuid.New(),
			WatcherKind: "user", Source: domain.WatchSourceCommenter,
		}, false)
	})

	assert.Contains(t, sql, "ON CONFLICT (document_id, watcher_id) DO NOTHING")
	assert.NotContains(t, sql, "muted = FALSE",
		"the automatic path must not write muted at all")
}

func TestDocumentWatchRepo_ExplicitSubscribeClearsAMute(t *testing.T) {
	// The other half — a tombstone is not a ban. Pressing Watch again works.
	sql := captureWatchSQL(t, false, func(r *DocumentWatchRepo) {
		_ = r.Subscribe(context.Background(), domain.DocumentWatcher{
			DocumentID: uuid.New(), WatcherID: uuid.New(),
			WatcherKind: "user", Source: domain.WatchSourceExplicit,
		}, true)
	})

	assert.Contains(t, sql, "DO UPDATE")
	assert.Contains(t, sql, "muted = FALSE")
}

func TestDocumentWatchRepo_UnsubscribeWritesATombstoneRatherThanDeleting(t *testing.T) {
	// A DELETE here is the bug: it makes "asked not to be told" and "never
	// subscribed" the same state, and the automatic path may overwrite the
	// second.
	sql := captureWatchSQL(t, false, func(r *DocumentWatchRepo) {
		_ = r.Unsubscribe(context.Background(), uuid.New(), uuid.New(), "user")
	})

	assert.NotContains(t, sql, "DELETE")
	assert.Contains(t, sql, "muted = TRUE")
	// It must also insert: a watcher can be subscribed through a row they never
	// created, and unsubscribing before any row exists has to be expressible.
	assert.Contains(t, sql, "INSERT INTO document_watchers")
}

func TestDocumentWatchRepo_RecordChangeTargetsTheOPENNoticeOnly(t *testing.T) {
	// This one statement IS the coalescing, and the `WHERE dispatched_at IS NULL`
	// on the conflict target is the whole of it: without it the upsert would
	// match a notice that has already been sent, and an edit arriving after
	// delivery would be folded into news nobody will hear again.
	sql := captureWatchSQL(t, false, func(r *DocumentWatchRepo) {
		_ = r.RecordChange(context.Background(), domain.DocumentChangeNotice{
			DocumentID: uuid.New(), WorkspaceID: uuid.New(), ActorID: uuid.New(),
			ActorKind: "user", FromVersion: 1, ToVersion: 2, BodyChanged: true,
		})
	})

	assert.Regexp(t,
		regexp.MustCompile(`ON CONFLICT \(document_id, actor_id, actor_kind\)\s+WHERE dispatched_at IS NULL`),
		sql, "the conflict target must be the PARTIAL index, or dispatched notices get reopened")
	assert.Contains(t, sql, "edit_count    = document_change_notices.edit_count + 1",
		"an autosave must increment the open notice, not create a second one")
	// The span must only ever widen: two writers can arrive out of order, and a
	// span that narrowed would describe less than actually happened.
	assert.Contains(t, sql, "LEAST(document_change_notices.from_version")
	assert.Contains(t, sql, "GREATEST(document_change_notices.to_version")
}

func TestDocumentWatchRepo_ClaimIsAtomicAndSkipsLockedRows(t *testing.T) {
	// Two API replicas run this sweeper against one table. A SELECT followed by
	// an UPDATE would let both claim the same notice and send the subscriber two
	// copies of the news the notice exists to send once.
	sql := captureWatchSQL(t, true, func(r *DocumentWatchRepo) {
		_, _ = r.ClaimPendingNotices(context.Background(), time.Now(), 10)
	})

	assert.Contains(t, sql, "FOR UPDATE SKIP LOCKED")
	assert.Contains(t, sql, "SET dispatched_at = NOW()")
	assert.Contains(t, sql, "WHERE dispatched_at IS NULL")
	assert.Contains(t, sql, "RETURNING",
		"the claim and the read are one statement — a second SELECT would reopen the race")
}

func TestDocumentWatchRepo_ClaimAppliesADefaultLimit(t *testing.T) {
	// A zero limit reaching Postgres verbatim would claim nothing and the
	// sweeper would silently never deliver anything.
	repo, mock := newDocumentWatchRepoMock(t)
	mock.ExpectQuery("document_change_notices").
		WithArgs(sqlmock.AnyArg(), 100).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "document_id", "workspace_id", "actor_id", "actor_kind", "actor_name",
			"edit_count", "title_changed", "body_changed", "from_version", "to_version",
			"first_edit_at", "last_edit_at", "dispatched_at", "dispatch_error", "recipients",
		}))

	_, err := repo.ClaimPendingNotices(context.Background(), time.Now(), 0)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDocumentWatchRepo_ListLiveWatchersExcludesMuted(t *testing.T) {
	sql := captureWatchSQL(t, true, func(r *DocumentWatchRepo) {
		_, _ = r.ListLiveWatchers(context.Background(), uuid.New())
	})

	assert.Contains(t, sql, "muted = FALSE",
		"a muted row is a record of a refusal, never a delivery instruction")
}

func TestDocumentWatchRepo_ListLiveWatchersSurfacesFailure(t *testing.T) {
	// Fail loudly: a watcher list that answers "nobody" on a database error
	// would make a broken lookup indistinguishable from an unwatched document,
	// which is the exact silence this feature is supposed to remove.
	repo, mock := newDocumentWatchRepoMock(t)
	mock.ExpectQuery("document_watchers").WillReturnError(errDBUnavailable)

	got, err := repo.ListLiveWatchers(context.Background(), uuid.New())

	require.ErrorIs(t, err, errDBUnavailable)
	assert.Nil(t, got)
}

func TestDocumentWatchRepo_GetStateCountsOnlyLiveWatchers(t *testing.T) {
	sql := captureWatchSQL(t, true, func(r *DocumentWatchRepo) {
		_, _ = r.GetState(context.Background(), uuid.New(), uuid.New(), "user")
	})

	assert.Contains(t, sql, "WHERE document_id = $1 AND muted = FALSE")
	assert.Contains(t, sql, "watcher_kind = $3",
		"a watcher is an (id, kind) pair — matching on the id alone can hit the wrong principal")
}

func TestDocumentWatchRepo_GetStateReturnsAMutedRowAsNotWatching(t *testing.T) {
	repo, mock := newDocumentWatchRepoMock(t)
	mock.ExpectQuery("document_watchers").
		WillReturnRows(sqlmock.NewRows([]string{"watching", "muted", "source", "watcher_count"}).
			AddRow(false, true, "explicit", 2))

	st, err := repo.GetState(context.Background(), uuid.New(), uuid.New(), "user")

	require.NoError(t, err)
	assert.False(t, st.Watching)
	assert.True(t, st.Muted)
	assert.Empty(t, st.Source,
		"source describes a LIVE subscription — reporting one for a muted row would have the UI explain a subscription that is not there")
	assert.Equal(t, 2, st.WatcherCount)
}

func TestDocumentWatchRepo_FinishNoticeStoresBlankErrorAsNull(t *testing.T) {
	// recipients = 0 with a NULL error is the ordinary "nobody was watching"
	// answer. Storing an empty string instead would make every successful
	// dispatch look like it carried an error message of zero length, and the
	// column would stop separating the two cases it exists to separate.
	sql := captureWatchSQL(t, false, func(r *DocumentWatchRepo) {
		_ = r.FinishNotice(context.Background(), uuid.New(), 0, "")
	})

	assert.Contains(t, sql, "NULLIF($3, '')")
}
