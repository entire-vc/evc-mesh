package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No //go:build integration tag — same convention as comment_cursor_tiebreak_db_test.go.
//
// i18n L3.2a: comments carry an optional system_code/system_params. A comment
// written without them (every human comment, every row from before the
// migration) must read back as NULL, not as "" or an empty object.
func TestCommentRepo_SystemCodeRoundTrip(t *testing.T) {
	db := commentCursorTestDB(t)
	fx := newCursorTestFixture(t, db)
	repo := NewCommentRepo(db)
	author := createCursorTestUser(t, db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	plain := &domain.Comment{ID: uuid.New(), TaskID: fx.taskID, AuthorID: author, AuthorType: domain.ActorTypeUser, Body: "plain", CreatedAt: now, UpdatedAt: now}
	require.NoError(t, repo.Create(ctx, plain))

	code := "moved_to_triage"
	coded := &domain.Comment{
		ID: uuid.New(), TaskID: fx.taskID, AuthorID: author, AuthorType: domain.ActorTypeUser, Body: "coded",
		SystemCode: &code, SystemParams: domain.SystemParams(`{"target":"pavel"}`),
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}
	require.NoError(t, repo.Create(ctx, coded))

	got, err := repo.GetByID(ctx, plain.ID)
	require.NoError(t, err)
	assert.Nil(t, got.SystemCode)
	assert.Nil(t, got.SystemParams)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"system_code":null`)
	assert.Contains(t, string(raw), `"system_params":null`)

	got, err = repo.GetByID(ctx, coded.ID)
	require.NoError(t, err)
	require.NotNil(t, got.SystemCode)
	assert.Equal(t, "moved_to_triage", *got.SystemCode)
	assert.JSONEq(t, `{"target":"pavel"}`, string(got.SystemParams))
}
