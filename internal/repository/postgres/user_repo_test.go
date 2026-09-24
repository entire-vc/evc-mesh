package postgres

import (
	"context"
	"errors"
	"net/url"
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
	dsn := userRepoTestDSN()
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

func userRepoTestDSN() string {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn
	}
	return "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
}

// dsnWithSearchPath returns dsn with the connection's search_path pinned to
// schema alone. public is left out on purpose: an unqualified `users` then
// either resolves to the private table or fails, and can never fall through
// to the shared one.
func dsnWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return dsn + " search_path=" + schema // key=value form
	}
	u, err := url.Parse(dsn)
	if err != nil {
		// url.Error carries the whole DSN, password included; report only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

// usersIsolatedDB returns a handle on which the unqualified name `users` is a
// private, initially empty copy of the table, so a test can assert exact row
// counts instead of a delta over a table other tests are also writing to.
//
// Why a private table and not just a tighter assertion: `go test ./...` runs
// packages in parallel against one DATABASE_URL, and internal/handler,
// internal/service and this package all insert into and delete from the shared
// `users`. Any before/after read of the global count can be split by another
// package's write, in either direction (measured: `before+1` off by +1 and -1).
// Serialising tests inside this package would not help — the writers are in
// other processes.
func usersIsolatedDB(t *testing.T) *sqlx.DB {
	t.Helper()
	admin := userRepoTestDB(t)
	ctx := context.Background()

	schema := "usercount_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	_, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err, "create private schema")
	t.Cleanup(func() {
		if _, dropErr := admin.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); dropErr != nil {
			t.Logf("could not drop private schema %s (it is left behind, harmless): %v", schema, dropErr)
		}
	})
	_, err = admin.ExecContext(ctx,
		"CREATE TABLE "+schema+".users (LIKE users INCLUDING DEFAULTS INCLUDING CONSTRAINTS INCLUDING INDEXES)")
	require.NoError(t, err, "create private users table")

	db, err := sqlx.Connect("postgres", dsnWithSearchPath(t, userRepoTestDSN(), schema))
	require.NoError(t, err, "connect with private search_path")
	t.Cleanup(func() { db.Close() })

	// Prove the isolation is real rather than assume it: if the search_path
	// were ignored, every assertion below would silently measure the shared
	// table again.
	var current string
	require.NoError(t, db.GetContext(ctx, &current, "SELECT current_schema()"))
	require.Equal(t, schema, current, "connection must resolve `users` in the private schema")
	return db
}

func newCountTestUser() *domain.User {
	suffix := uuid.New().String()[:8]
	return &domain.User{
		ID:           uuid.New(),
		Email:        "count-test-" + suffix + "@example.com",
		PasswordHash: "irrelevant-hash",
		Name:         "Count Test User",
		Username:     "count-test-" + suffix,
		IsActive:     true,
		CreatedAt:    time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:    time.Now().UTC().Truncate(time.Microsecond),
	}
}

func TestUserRepo_Count(t *testing.T) {
	db := usersIsolatedDB(t)
	repo := NewUserRepo(db)
	ctx := context.Background()

	count := func() int {
		t.Helper()
		n, err := repo.Count(ctx)
		require.NoError(t, err)
		return n
	}

	require.Equal(t, 0, count(), "no users yet: Count must be 0, not some leftover row count")

	u1, u2 := newCountTestUser(), newCountTestUser()
	require.NoError(t, repo.Create(ctx, u1))
	require.Equal(t, 1, count(), "Count must reflect the newly created user")
	require.NoError(t, repo.Create(ctx, u2))
	require.Equal(t, 2, count(), "Count must reflect every created user")

	_, err := db.ExecContext(ctx, "DELETE FROM users WHERE id = $1", u1.ID)
	require.NoError(t, err)
	require.Equal(t, 1, count(), "Count must reflect a removed user")
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
