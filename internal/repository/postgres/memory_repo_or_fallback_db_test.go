package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Same convention as the other *_db_test.go files: untagged, real Postgres in CI,
// graceful skip locally.

// The ranked 'english' OR fallback counts matched lexemes per term through the GIN
// index and keeps rows matching at least ceil(n/2) of them. Nobody carries all four
// words, so the AND arm is empty and the fallback answers.
func TestMemoryRepoDB_FullTextSearchRanked_ORFallbackKeepsHalfOfTheLexemes(t *testing.T) {
	repo, wsID := setupTouchAccessedDBTest(t)
	ctx := context.Background()

	mk := func(key, content string) uuid.UUID {
		m := &domain.Memory{
			ID: uuid.New(), WorkspaceID: wsID, Key: key, Content: content,
			Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent,
		}
		require.NoError(t, repo.Upsert(ctx, m, domain.MemoryWriteIntent{}))
		return m.ID
	}
	three := mk("or-three", "zorblax quindle frumpet were seen together")
	two := mk("or-two", "quindle and snarfle appear here")
	one := mk("or-one", "only zorblax is mentioned")

	got, err := repo.FullTextSearchRanked(ctx, wsID, nil, "zorblax quindle frumpet snarfle", domain.MemorySearchFilter{}, 10)
	require.NoError(t, err)
	ids := map[uuid.UUID]bool{}
	for _, r := range got {
		ids[r.ID] = true
	}
	assert.True(t, ids[three], "3 of 4 lexemes must be a candidate")
	assert.True(t, ids[two], "2 of 4 lexemes is the ceil(n/2) threshold")
	assert.False(t, ids[one], "1 of 4 lexemes is below the threshold")
}
