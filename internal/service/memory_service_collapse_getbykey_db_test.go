package service

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/embedding"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// No //go:build integration tag, deliberately — same reasoning as
// push_membership_db_test.go / memory_repo_scope_identity_db_test.go: CI's
// untagged `go test ./...` runs these against a migrated DATABASE_URL, and
// skip()s rather than fails when no database is reachable.
//
// Regression tests for task #2c0154db, finding F1: after cmd/collapse-memories
// leaves a winner `active` and a loser `superseded` under the SAME natural
// key, Remember() on that key must resolve to the WINNER (via GetByKey's now
// status-aware, deterministically-ordered lookup) — not silently resurrect
// the retired row and zero its superseded_by audit trail, and not 23505 on
// the live unique index.

func collapseGetByKeyTestDB(t *testing.T) *sqlx.DB {
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

// newRealMemoryService wires a MemoryService against the real Postgres
// repositories — the thing under test (GetByKey's SQL predicate/ordering)
// lives in the repo layer, so a mocked MemoryRepository would not exercise it.
func newRealMemoryService(db *sqlx.DB) (MemoryService, *postgres.MemoryRepo) {
	memRepo := postgres.NewMemoryRepo(db)
	edgeRepo := postgres.NewMemoryEdgesRepo(db)
	return NewMemoryService(memRepo, edgeRepo, embedding.NewNoopEmbedder()), memRepo
}

func TestRemember_CollapsedKey_ResolvesToWinnerNotRetired(t *testing.T) {
	db := collapseGetByKeyTestDB(t)
	svc, memRepo := newRealMemoryService(db)
	ctx := context.Background()

	wsRepo := postgres.NewWorkspaceRepo(db)
	ws := &domain.Workspace{ID: uuid.New(), Name: "collapse-getbykey-ws", Slug: "collapse-getbykey-" + uuid.New().String()[:8], OwnerID: uuid.New()}
	require.NoError(t, wsRepo.Create(ctx, ws))

	key := "collapsed-key-" + uuid.New().String()[:8]
	winner := &domain.Memory{ID: uuid.New()}
	loserID := uuid.New()

	// Two ACTIVE rows can never coexist under the same (workspace,key) even
	// transiently — the partial unique index this same epic shipped
	// (uq_mem_ws_key, WHERE status='active') forbids it by construction. So
	// the loser is inserted directly as already-superseded, in a single raw
	// INSERT, and — critically — inserted FIRST, before the winner. This
	// mirrors what actually happened on prod: the loser's row long predates
	// the winner's (it was the original write; the winner came from a LATER
	// remember() call that should have upserted onto it but forked instead),
	// so the loser has the physically-earlier heap position. Without an
	// ORDER BY, that is exactly the condition under which Postgres's scan
	// returned the retired row first in every group Garfield measured on
	// prod — insert order alone reproduces it; a same-transaction UPDATE
	// does not reliably relocate a row's scan position the way a distinct,
	// earlier INSERT does.
	// superseded_by is a real FK to memories(id), so the loser's link to the
	// winner can only be set AFTER the winner row exists — insert the loser
	// first with a NULL link (still physically the earlier row), insert the
	// winner second, then attach the link with a small in-place UPDATE.
	_, err := db.ExecContext(ctx, `
		INSERT INTO memories (id, workspace_id, key, scope, content, source_type, status, tags)
		VALUES ($1, $2, $3, 'workspace', 'retired content', 'agent', 'superseded', '{kind:learning}')`,
		loserID, ws.ID, key)
	require.NoError(t, err)

	winner.WorkspaceID, winner.Key, winner.Content = ws.ID, key, "winner content"
	winner.Scope, winner.SourceType = domain.ScopeWorkspace, domain.SourceAgent
	winner.Status, winner.Tags = domain.MemoryStatusActive, []string{"kind:learning"}
	require.NoError(t, memRepo.Upsert(ctx, winner))

	_, err = db.ExecContext(ctx, `UPDATE memories SET superseded_by=$1 WHERE id=$2`, winner.ID, loserID)
	require.NoError(t, err)

	// The collapse group must be genuinely ambiguous without a status filter —
	// verify the setup itself before trusting the assertions below.
	var rawCount int
	require.NoError(t, db.GetContext(ctx, &rawCount,
		`SELECT count(*) FROM memories WHERE workspace_id=$1 AND key=$2 AND scope='workspace'`, ws.ID, key))
	require.Equal(t, 2, rawCount, "test setup sanity: both rows must exist under the same natural key")

	result, err := svc.Remember(ctx, &domain.Memory{
		WorkspaceID: ws.ID, Key: key, Content: "third write",
		Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent,
		Tags: []string{"kind:learning"},
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", result.Outcome, "must upsert the live row, not create a third")

	// The retired row must be untouched: still superseded, superseded_by intact.
	var afterLoser struct {
		Status       string     `db:"status"`
		SupersededBy *uuid.UUID `db:"superseded_by"`
	}
	require.NoError(t, db.GetContext(ctx, &afterLoser,
		`SELECT status, superseded_by FROM memories WHERE id=$1`, loserID))
	assert.Equal(t, "superseded", afterLoser.Status, "retired row must NOT resurrect to active")
	require.NotNil(t, afterLoser.SupersededBy, "superseded_by must NOT be zeroed — it is the audit trail")
	assert.Equal(t, winner.ID, *afterLoser.SupersededBy)

	// The winner must have been the one updated (new content), and still active.
	var afterWinner struct {
		Content string `db:"content"`
		Status  string `db:"status"`
	}
	require.NoError(t, db.GetContext(ctx, &afterWinner,
		`SELECT content, status FROM memories WHERE id=$1`, winner.ID))
	assert.Equal(t, "third write", afterWinner.Content, "the winner row must be the one that got updated")
	assert.Equal(t, "active", afterWinner.Status)

	// No third row was created, and the unique index did not reject the write.
	var finalCount int
	require.NoError(t, db.GetContext(ctx, &finalCount,
		`SELECT count(*) FROM memories WHERE workspace_id=$1 AND key=$2 AND scope='workspace'`, ws.ID, key))
	assert.Equal(t, 2, finalCount, "still exactly winner+retired — no third row, no resurrection")
}

// TestRemember_CollapsedKey_ReviewNeededIsAlsoCurrent is F1/F2's other edge:
// a `review_needed` row is what reads treat as current (memory_repo.go's other
// `status != 'superseded' AND archived = false` sites), so GetByKey must find
// it too — `status = 'active'` would wrongly treat it as gone and fork.
func TestRemember_CollapsedKey_ReviewNeededIsAlsoCurrent(t *testing.T) {
	db := collapseGetByKeyTestDB(t)
	svc, memRepo := newRealMemoryService(db)
	ctx := context.Background()

	wsRepo := postgres.NewWorkspaceRepo(db)
	ws := &domain.Workspace{ID: uuid.New(), Name: "collapse-getbykey-rn-ws", Slug: "collapse-getbykey-rn-" + uuid.New().String()[:8], OwnerID: uuid.New()}
	require.NoError(t, wsRepo.Create(ctx, ws))

	key := "review-needed-key-" + uuid.New().String()[:8]
	m := &domain.Memory{
		ID: uuid.New(), WorkspaceID: ws.ID, Key: key, Content: "v1",
		Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent,
		Status: domain.MemoryStatusReviewNeeded, Tags: []string{"kind:learning"},
	}
	require.NoError(t, memRepo.Upsert(ctx, m))

	result, err := svc.Remember(ctx, &domain.Memory{
		WorkspaceID: ws.ID, Key: key, Content: "v2",
		Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent,
		Tags: []string{"kind:learning"},
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", result.Outcome, "review_needed must be found by GetByKey — status='active' would miss it and fork")

	var count int
	require.NoError(t, db.GetContext(ctx, &count,
		`SELECT count(*) FROM memories WHERE workspace_id=$1 AND key=$2 AND scope='workspace'`, ws.ID, key))
	assert.Equal(t, 1, count, "no fork — the review_needed row was found and updated")
}
