package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// No //go:build integration tag, deliberately — same reasoning as
// memory_service_collapse_getbykey_db_test.go: CI's untagged `go test ./...`
// runs these against a migrated DATABASE_URL and skip()s rather than fails
// when no database is reachable.
//
// Regression test for #a2e00afd: Remember() commits the memory row
// synchronously but fires embedding via `go s.embedAndStore(...)` — a
// fire-and-forget goroutine with no completion signal. Until that goroutine
// finishes, the row fails vectorCandidateIDs's predicate and is invisible to
// the dense arm even though search_mode still reports "hybrid" and nothing
// errored. This test pins the race down deterministically with a gated
// embedder (blocks on a channel) instead of relying on timing, then asserts
// the caller receives an explicit signal that the row isn't dense-findable
// yet — since the row itself provably isn't.

// gatedEmbedder blocks every Embed/EmbedBatch call on release until the test
// closes it, so embedAndStore's goroutine is deterministically still in
// flight at the moment the test inspects VectorSearch and RememberResult.
type gatedEmbedder struct {
	dim     int
	release chan struct{}
}

func (e *gatedEmbedder) Embed(ctx context.Context, _ string) ([]float32, error) {
	select {
	case <-e.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return make([]float32, e.dim), nil
}

func (e *gatedEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case <-e.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, e.dim)
	}
	return out, nil
}

func (e *gatedEmbedder) Model() string   { return "gated-test" }
func (e *gatedEmbedder) Dimensions() int { return e.dim }

func embeddingPendingTestDB(t *testing.T) *sqlx.DB {
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

func TestRemember_EmbeddingPending_SignaledWhileGoroutineInFlight(t *testing.T) {
	db := embeddingPendingTestDB(t)
	ctx := context.Background()

	memRepo := postgres.NewMemoryRepo(db)
	edgeRepo := postgres.NewMemoryEdgesRepo(db)
	gated := &gatedEmbedder{dim: 4, release: make(chan struct{})}
	svc := NewMemoryService(memRepo, edgeRepo, gated)

	wsRepo := postgres.NewWorkspaceRepo(db)
	ws := &domain.Workspace{ID: uuid.New(), Name: "embed-pending-ws", Slug: "embed-pending-" + uuid.New().String()[:8], OwnerID: uuid.New()}
	require.NoError(t, wsRepo.Create(ctx, ws))

	mem := &domain.Memory{
		WorkspaceID: ws.ID,
		Key:         "embed-pending-" + uuid.New().String()[:8],
		Content:     "content that will be embedded asynchronously",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
		Tags:        []string{"kind:learning"},
	}

	result, err := svc.Remember(ctx, mem)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, mem.ID, "Remember must assign an ID before returning")

	// Sanity: the embed goroutine is provably still blocked on gated.release —
	// so the row MUST NOT be dense-findable right now. This is the bug's
	// mechanism, unaffected by whichever fix variant lands.
	queryVec := make([]float32, gated.dim)
	rows, err := memRepo.VectorSearch(ctx, queryVec, ws.ID, nil, domain.MemorySearchFilter{}, 20)
	require.NoError(t, err)
	assert.False(t, containsMemoryID(rows, mem.ID),
		"sanity check failed: row was dense-findable while the embed goroutine was still gated — test no longer pins the race")

	// The actual assertion: since the row is NOT dense-findable, the caller
	// must have an explicit way to know that — RememberResult.EmbeddingPending.
	// Before the fix this field does not exist / is always false: RED.
	assert.True(t, result.EmbeddingPending,
		"caller received no signal that the row is not yet dense-findable — remember() lied by omission")

	close(gated.release)

	// Eventual consistency: once the goroutine is unblocked, the row must
	// become dense-findable (proves the gate wasn't just permanently broken).
	require.Eventually(t, func() bool {
		rows, err := memRepo.VectorSearch(ctx, queryVec, ws.ID, nil, domain.MemorySearchFilter{}, 20)
		return err == nil && containsMemoryID(rows, mem.ID)
	}, 2*time.Second, 20*time.Millisecond, "row never became dense-findable after the embed goroutine was released")
}

func containsMemoryID(rows []domain.ScoredMemory, id uuid.UUID) bool {
	for _, r := range rows {
		if r.ID == id {
			return true
		}
	}
	return false
}
