package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// userRepoTestDB connects to the same live Postgres instance CI's "Test" and
// "Go coverage" jobs already run migrations against (DATABASE_URL). Unlike
// integration_test.go's testDB, this file carries no build tag on purpose —
// Count's new bootstrap-invariant check (auth.Service.RegistrationOpen) needs
// to be measured by the unencumbered `go test $pkg` the coverage gate runs,
// which excludes anything behind `//go:build integration`. Skips instead of
// failing when no DB is reachable, so a plain local `go test ./...` still runs.
func userRepoTestDB(t *testing.T) *sqlx.DB {
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
		db.Close()
		t.Skipf("Postgres at %s not accepting connections, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestUserRepo_Count(t *testing.T) {
	db := userRepoTestDB(t)
	repo := NewUserRepo(db)
	ctx := context.Background()

	before, err := repo.Count(ctx)
	require.NoError(t, err)

	u := &domain.User{
		ID:           uuid.New(),
		Email:        "count-test-" + uuid.New().String()[:8] + "@example.com",
		PasswordHash: "irrelevant-hash",
		Name:         "Count Test User",
		Username:     "count-test-" + uuid.New().String()[:8],
		IsActive:     true,
		CreatedAt:    time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:    time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, repo.Create(ctx, u))
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM users WHERE id = $1", u.ID) })

	after, err := repo.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after, "Count must reflect the newly created user")
}
