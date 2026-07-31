package postgres

import (
	"context"
	"os"
	"strings"
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

// TestUserRepo_GetByEmail_IsCaseAndWhitespaceInsensitive pins the lookup to the
// same canonical form the unique index ix_users_email_lower enforces
// (migration 20260728083). Before that, GetByEmail compared the raw column, so
// an address that differed only in case resolved to "no such user" and the
// caller returned 401 for an account that plainly existed.
func TestUserRepo_GetByEmail_IsCaseAndWhitespaceInsensitive(t *testing.T) {
	db := userRepoTestDB(t)
	repo := NewUserRepo(db)
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	stored := "getbyemail-" + suffix + "@example.com"
	u := &domain.User{
		ID:           uuid.New(),
		Email:        stored,
		PasswordHash: "irrelevant-hash",
		Name:         "GetByEmail Test User",
		Username:     "getbyemail-" + suffix,
		IsActive:     true,
		CreatedAt:    time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:    time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, repo.Create(ctx, u))
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM users WHERE id = $1", u.ID) })

	for _, spelling := range []string{
		stored,
		strings.ToUpper(stored),
		"  " + stored + "  ",
		"  " + strings.ToUpper(stored) + "\t",
	} {
		got, err := repo.GetByEmail(ctx, spelling)
		require.NoError(t, err, "GetByEmail(%q)", spelling)
		require.NotNil(t, got, "GetByEmail(%q) must find the account", spelling)
		require.Equal(t, u.ID, got.ID)
	}

	// A genuinely absent address is still (nil, nil), not an error.
	missing, err := repo.GetByEmail(ctx, "definitely-not-here-"+suffix+"@example.com")
	require.NoError(t, err)
	require.Nil(t, missing)
}

// TestUserRepo_UpdateKeepsUsernameWhenBlank is the regression for a live 500.
//
// GetByID did not select username, so every read-modify-write through it
// carried Username="" back into Update, which wrote `username = NULLIF($5,”)`
// — NULL into a NOT NULL column. PATCH /api/v1/auth/me is the only path users
// can reach that does exactly this, so editing your own display name failed on
// the constraint every time. Both halves are pinned here: the projection must
// include username, and Update must treat a blank one as "unchanged".
func TestUserRepo_UpdateKeepsUsernameWhenBlank(t *testing.T) {
	db := userRepoTestDB(t)
	repo := NewUserRepo(db)
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	u := &domain.User{
		ID:           uuid.New(),
		Email:        "keepuser-" + suffix + "@example.com",
		PasswordHash: "irrelevant-hash",
		Name:         "keepuser-" + suffix + "@example.com",
		Username:     "keepuser-" + suffix,
		IsActive:     true,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	require.NoError(t, repo.Create(ctx, u))
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM users WHERE id = $1", u.ID) })

	loaded, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, u.Username, loaded.Username, "GetByID must project username")
	require.False(t, loaded.DisplayNameSelfSet, "a freshly provisioned account has an unowned name")
	require.True(t, loaded.NameIsPlaceholder())

	// The self-edit: a new name, no username supplied.
	loaded.Name = "Keep User"
	loaded.DisplayNameSelfSet = true
	require.NoError(t, repo.Update(ctx, loaded), "updating only the name must not violate the NOT NULL username")

	after, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "Keep User", after.Name)
	require.Equal(t, u.Username, after.Username, "an omitted username means unchanged, never cleared")
	require.True(t, after.DisplayNameSelfSet, "provenance must round-trip")
	require.False(t, after.NameIsPlaceholder())

	// An explicitly blank username on a struct that was never loaded must also
	// leave the stored one alone rather than abort the statement.
	blank := *after
	blank.Username = ""
	require.NoError(t, repo.Update(ctx, &blank))
	still, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, u.Username, still.Username)
}

// GetByEmail feeds the add-member path, which builds the member payload
// straight off this struct — so it has to project the same columns GetByID does.
func TestUserRepo_GetByEmailProjectsUsernameAndProvenance(t *testing.T) {
	db := userRepoTestDB(t)
	repo := NewUserRepo(db)
	ctx := context.Background()

	suffix := uuid.New().String()[:8]
	u := &domain.User{
		ID:                 uuid.New(),
		Email:              "projemail-" + suffix + "@example.com",
		PasswordHash:       "irrelevant-hash",
		Name:               "Proj Email",
		Username:           "projemail-" + suffix,
		IsActive:           true,
		DisplayNameSelfSet: true,
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}
	require.NoError(t, repo.Create(ctx, u))
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM users WHERE id = $1", u.ID) })

	got, err := repo.GetByEmail(ctx, strings.ToUpper(u.Email))
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, u.Username, got.Username)
	require.True(t, got.DisplayNameSelfSet, "Create must persist the flag and GetByEmail must project it")
}
