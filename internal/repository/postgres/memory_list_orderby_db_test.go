package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No //go:build integration tag, deliberately — same reasoning as the other
// *_db_test.go files: CI's untagged `go test ./...` runs these against a
// migrated DATABASE_URL and skip()s rather than fails when no database is
// reachable.
//
// #655c6d12: `decayed_relevance` had two triggers with two different expected
// strings — the recall service compared against the bare key, this ORDER BY
// switch matched only the suffixed form. The switch now normalises through
// domain.CanonicalOrderBy so one name means one thing on both paths.
//
// The observable is MemoryListResult.DecayApplied, which the switch sets in
// exactly the decay branch. Asserting on it exercises the real SQL — an
// ORDER BY expression that failed to build would surface here as an error
// rather than as a silently different sort.

func listOrderByTestDB(t *testing.T) *sqlx.DB {
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
	// Reachable is not the same as migrated. Without this the test FAILS on an
	// un-migrated database with `relation "memories" does not exist` — an
	// infrastructure fault reported as a verdict on the change, which is the
	// one thing a gate must never do. Connectivity guards alone let that
	// through; the schema is the thing actually being depended on.
	var hasTable bool
	if err := db.Get(&hasTable, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'memories')`); err != nil || !hasTable {
		_ = db.Close()
		t.Skipf("Postgres at %s has no `memories` table (not migrated), skipping", dsn)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestList_OrderByDecayNormalisation(t *testing.T) {
	repo := NewMemoryRepo(listOrderByTestDB(t))
	ctx := context.Background()

	cases := []struct {
		orderBy   string
		wantDecay bool
		why       string
	}{
		{domain.OrderByDecayedRelevanceDesc, true, "the canonical form every producer emits"},
		{"decayed_relevance", true, "the bare key — silently fell through to created_at DESC before #655c6d12"},

		// Discriminating controls. Without them this passes on a switch that
		// reports DecayApplied unconditionally, which would be a worse defect
		// than the one being fixed and equally invisible.
		{domain.OrderByRelevanceDesc, false, "plain relevance is not decayed relevance"},
		{domain.OrderByCreatedAtAsc, false, "explicit non-decay ordering"},
		{"", false, "no ordering requested"},
		{"decayed_relevance_something_else", false, "near-miss must not match — the compare is exact, not a prefix"},
	}

	for _, tc := range cases {
		t.Run("orderBy="+tc.orderBy, func(t *testing.T) {
			// An empty workspace is enough: DecayApplied is decided by the
			// ORDER BY branch, not by the rows, and keeping the fixture empty
			// means this test cannot be perturbed by other tests' data.
			res, err := repo.List(ctx, domain.MemoryListFilter{
				WorkspaceID: uuid.New(),
				OrderBy:     tc.orderBy,
				Limit:       10,
			})
			require.NoError(t, err, "the ORDER BY expression must be valid SQL")
			require.NotNil(t, res)
			assert.Equal(t, tc.wantDecay, res.DecayApplied, "order_by=%q — %s", tc.orderBy, tc.why)
		})
	}
}
