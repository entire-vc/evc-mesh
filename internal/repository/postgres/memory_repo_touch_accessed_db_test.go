package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No //go:build integration tag on purpose — see userRepoTestDB's doc comment in
// user_repo_test.go: the "Go coverage" gate's untagged `go test $pkg` must see
// this behavior, since it is exactly the fix for task #c5b5fb48.

func setupTouchAccessedDBTest(t *testing.T) (*MemoryRepo, uuid.UUID) {
	t.Helper()
	db := userRepoTestDB(t)
	wsRepo := NewWorkspaceRepo(db)
	ws := &domain.Workspace{
		ID:      uuid.New(),
		Name:    "touch-accessed-db-test-ws",
		Slug:    "touch-accessed-db-" + uuid.New().String()[:8],
		OwnerID: uuid.New(),
	}
	require.NoError(t, wsRepo.Create(context.Background(), ws))
	return NewMemoryRepo(db), ws.ID
}

func TestMemoryRepoDB_TouchAccessed_SetsLastAccessedAt(t *testing.T) {
	repo, wsID := setupTouchAccessedDBTest(t)
	ctx := context.Background()

	mem := newMemory(t, repo, wsID, "touch-target")
	require.Nil(t, mem.LastAccessedAt, "a freshly-created memory has no last_accessed_at")

	require.NoError(t, repo.TouchAccessed(ctx, []uuid.UUID{mem.ID}))

	got, err := repo.GetByID(ctx, mem.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastAccessedAt)
	assert.WithinDuration(t, time.Now(), *got.LastAccessedAt, 5*time.Second)
}

func TestMemoryRepoDB_TouchAccessed_EmptyIDsIsNoop(t *testing.T) {
	repo, _ := setupTouchAccessedDBTest(t)
	// Must not error and must not panic on an empty/nil slice — RecallWithStats
	// calls this unconditionally-guarded by len(merged) > 0, but the repo method
	// itself must be safe against a zero-row response either way.
	assert.NoError(t, repo.TouchAccessed(context.Background(), nil))
	assert.NoError(t, repo.TouchAccessed(context.Background(), []uuid.UUID{}))
}

// TestMemoryRepoDB_FullTextSearchRanked_DoesNotTouchLastAccessedAt is the negative
// half of the fix: FullTextSearchRanked's own candidate pool must NOT bump
// last_accessed_at any more — only RecallWithStats' final trimmed set may (see
// TestRecall_TouchAccessed_OnlyFinalTrimmedSet in the service package for that
// side). Before the fix, this same call bumped last_accessed_at directly,
// which is why runReviewTriage's stale branch never fired: a review_needed row
// merely surfacing in an oversized candidate pool renewed its own "not stale"
// window forever.
func TestMemoryRepoDB_FullTextSearchRanked_DoesNotTouchLastAccessedAt(t *testing.T) {
	repo, wsID := setupTouchAccessedDBTest(t)
	ctx := context.Background()

	mem := newMemory(t, repo, wsID, "fts-ranked-no-touch")
	require.Nil(t, mem.LastAccessedAt)

	rows, err := repo.FullTextSearchRanked(ctx, wsID, nil, "content", domain.MemorySearchFilter{}, 10)
	require.NoError(t, err)
	found := false
	for _, r := range rows {
		if r.ID == mem.ID {
			found = true
		}
	}
	require.True(t, found, "sanity: the memory must actually be a hit for this query")

	// Read the raw column directly — NOT via GetByID, which itself touches
	// last_accessed_at as documented behavior and would mask what we're testing.
	var lastAccessedAt *time.Time
	require.NoError(t, repo.db.GetContext(ctx, &lastAccessedAt,
		`SELECT last_accessed_at FROM memories WHERE id = $1`, mem.ID))
	assert.Nil(t, lastAccessedAt, "FullTextSearchRanked must not touch last_accessed_at on its own candidate pool")
}
