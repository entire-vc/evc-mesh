package postgres

import (
	"context"
	"errors"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type uploadFixture struct {
	db    *sqlx.DB
	repo  *UploadSessionRepo
	ws    uuid.UUID
	agent uuid.UUID
}

func newUploadSessionFixture(t *testing.T) *uploadFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the upload-session repository tests")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	f := &uploadFixture{db: db, repo: NewUploadSessionRepo(db), ws: uuid.New(), agent: uuid.New()}
	short := f.ws.String()[:8]
	owner := uuid.New()
	chatExec(t, db, `INSERT INTO users (id, email, username, password_hash, display_name) VALUES ($1,$2,$3,'x','owner')`,
		owner, "repo-upl-"+short+"@example.com", "repo-upl-"+short)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, owner) })
	chatExec(t, db, `INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1,'upload repo',$2,$3)`, f.ws, "upl-repo-"+short, owner)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, f.ws) })
	return f
}

// uploadSweepTestLock is the advisory lock key every sweep test holds (here
// and in internal/handler/upload_session_handler_db_test.go): a sweep removes
// every expired session in the shared test database, and packages test in
// parallel, so two sweeps at once would take each other's rows.
const uploadSweepTestLock int64 = 0x75706c5377656570

// lockUploadSweepForTest holds uploadSweepTestLock until the test ends.
func lockUploadSweepForTest(t *testing.T, db *sqlx.DB) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Connx(ctx)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, uploadSweepTestLock)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, uploadSweepTestLock)
		_ = conn.Close()
	})
}

// session inserts an upload session and then moves its clock fields to the
// given ages, so the sweep sees it as a real old row.
func (f *uploadFixture) session(t *testing.T, expiresIn time.Duration) *UploadSession {
	t.Helper()
	id := uuid.New()
	s := &UploadSession{
		ID: id, WorkspaceID: f.ws, Kind: UploadKindDocumentAttachment, TargetID: uuid.New(),
		Name: "f.png", MimeType: "image/png", SizeBytes: 10,
		SHA256:     "0000000000000000000000000000000000000000000000000000000000000000",
		StagingKey: "uploads/" + f.ws.String() + "/" + id.String(),
		CreatedBy:  f.agent, CreatedByType: "agent", ExpiresAt: time.Now().Add(expiresIn),
	}
	require.NoError(t, f.repo.Create(context.Background(), s))
	return s
}

func (f *uploadFixture) exists(t *testing.T, id uuid.UUID) bool {
	t.Helper()
	var n int
	require.NoError(t, f.db.GetContext(context.Background(), &n, `SELECT count(*) FROM upload_sessions WHERE id = $1`, id))
	return n == 1
}

// sweepOnlyMine runs Sweep until it removes nothing more, recording the keys
// it deleted. Other packages' rows may share the table in CI, so the
// assertions look only at this fixture's keys.
func (f *uploadFixture) sweep(t *testing.T, fail map[string]bool) []string {
	t.Helper()
	var deleted []string
	for i := 0; i < 50; i++ {
		n, err := f.repo.Sweep(context.Background(), 5*time.Minute, 7*24*time.Hour, 200,
			func(_ context.Context, key string) error {
				if fail[key] {
					return errors.New("storage down")
				}
				deleted = append(deleted, key)
				return nil
			})
		require.NoError(t, err)
		if n == 0 {
			break
		}
	}
	mine := deleted[:0]
	for _, k := range deleted {
		if len(k) > 44 && k[8:44] == f.ws.String() {
			mine = append(mine, k)
		}
	}
	sort.Strings(mine)
	return mine
}

// TestUploadSessionSweep proves unconfirmed sessions and their staged objects
// do not accumulate: an expired open session past the grace and a confirmed
// one past retention are removed with their object; an open session still in
// its TTL or grace, one under a live confirm lease, and a fresh confirmed one
// stay. A session whose object delete fails stays for the next sweep.
func TestUploadSessionSweep(t *testing.T) {
	f := newUploadSessionFixture(t)
	lockUploadSweepForTest(t, f.db)
	ctx := context.Background()

	live := f.session(t, 15*time.Minute)
	inGrace := f.session(t, -time.Minute)
	expired := f.session(t, -time.Hour)
	leased := f.session(t, -time.Hour)
	chatExec(t, f.db, `UPDATE upload_sessions SET claimed_until = now() + interval '10 minutes' WHERE id = $1`, leased.ID)
	oldConfirmed := f.session(t, time.Hour)
	require.NoError(t, f.repo.MarkConfirmed(ctx, oldConfirmed.ID, uuid.New()))
	chatExec(t, f.db, `UPDATE upload_sessions SET confirmed_at = now() - interval '8 days' WHERE id = $1`, oldConfirmed.ID)
	freshConfirmed := f.session(t, time.Hour)
	require.NoError(t, f.repo.MarkConfirmed(ctx, freshConfirmed.ID, uuid.New()))
	storageDown := f.session(t, -time.Hour)

	deleted := f.sweep(t, map[string]bool{storageDown.StagingKey: true})

	want := []string{expired.StagingKey, oldConfirmed.StagingKey}
	sort.Strings(want)
	assert.Equal(t, want, deleted, "the sweep deletes exactly the abandoned objects")
	assert.False(t, f.exists(t, expired.ID), "expired open session swept")
	assert.False(t, f.exists(t, oldConfirmed.ID), "confirmed session past retention swept")
	assert.True(t, f.exists(t, live.ID), "open session inside its TTL kept")
	assert.True(t, f.exists(t, inGrace.ID), "session expired less than the grace ago kept")
	assert.True(t, f.exists(t, leased.ID), "session under a live confirm lease kept")
	assert.True(t, f.exists(t, freshConfirmed.ID), "recently confirmed session kept for replay")
	assert.True(t, f.exists(t, storageDown.ID), "a session whose object delete failed stays for the next sweep")

	// Storage back: the next sweep finishes it.
	deleted = f.sweep(t, nil)
	assert.Equal(t, []string{storageDown.StagingKey}, deleted)
	assert.False(t, f.exists(t, storageDown.ID))
}

// TestUploadSessionClaimAndOwnership covers the confirm's guards: only the
// creator reads a session, one lease at a time, none on an expired or
// confirmed session, and a released lease can be taken again.
func TestUploadSessionClaimAndOwnership(t *testing.T) {
	f := newUploadSessionFixture(t)
	ctx := context.Background()
	s := f.session(t, 15*time.Minute)

	got, err := f.repo.GetOwned(ctx, s.ID, f.agent, "agent")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, s.StagingKey, got.StagingKey)
	for _, other := range []struct {
		id  uuid.UUID
		typ string
	}{{uuid.New(), "agent"}, {f.agent, "user"}} {
		got, err = f.repo.GetOwned(ctx, s.ID, other.id, other.typ)
		require.NoError(t, err)
		assert.Nil(t, got, "a session is invisible to %s %s", other.typ, other.id)
	}

	first := uuid.New()
	pending, ok, err := f.repo.Claim(ctx, s.ID, time.Minute, first)
	require.NoError(t, err)
	assert.True(t, ok, "first claim")
	assert.Equal(t, first, pending, "the first claim fixes the result id")
	_, ok, err = f.repo.Claim(ctx, s.ID, time.Minute, uuid.New())
	require.NoError(t, err)
	assert.False(t, ok, "a second concurrent claim is refused")
	require.NoError(t, f.repo.Release(ctx, s.ID))
	pending, ok, err = f.repo.Claim(ctx, s.ID, time.Minute, uuid.New())
	require.NoError(t, err)
	assert.True(t, ok, "claim after release")
	assert.Equal(t, first, pending, "a later claim keeps the result id the first one fixed")
	got, err = f.repo.GetOwned(ctx, s.ID, f.agent, "agent")
	require.NoError(t, err)
	require.NotNil(t, got.PendingResultID)
	assert.Equal(t, first, *got.PendingResultID)

	result := uuid.New()
	require.NoError(t, f.repo.MarkConfirmed(ctx, s.ID, result))
	_, ok, err = f.repo.Claim(ctx, s.ID, time.Minute, uuid.New())
	require.NoError(t, err)
	assert.False(t, ok, "no claim on a confirmed session")
	got, err = f.repo.GetOwned(ctx, s.ID, f.agent, "agent")
	require.NoError(t, err)
	require.NotNil(t, got.ResultID)
	assert.Equal(t, result, *got.ResultID)

	expired := f.session(t, -time.Second)
	_, ok, err = f.repo.Claim(ctx, expired.ID, time.Minute, uuid.New())
	require.NoError(t, err)
	assert.False(t, ok, "no claim on an expired session")
}
