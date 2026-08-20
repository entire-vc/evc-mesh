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

// No //go:build integration tag — same convention as the other *_db_test.go
// files here.
//
// The service-level tests prove the dual-read logic against a mock. These prove
// the half a mock cannot: that the column round-trips through Postgres, that
// "not populated" is NULL rather than '' (the partial unique index depends on
// it), that the guarded backfill really is guarded, and that the uniqueness
// invariant is enforced by the database rather than by hope.

func agentDigestTestDB(t *testing.T) *sqlx.DB {
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

func newDigestAgent(t *testing.T, db *sqlx.DB, digest string) *domain.Agent {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	owner := &domain.User{
		ID: uuid.New(), Email: "digest-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Digest Owner", Username: "digest-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, owner))

	ws := &domain.Workspace{
		ID: uuid.New(), Name: "digest-ws", Slug: "digest-ws-" + suffix, OwnerID: owner.ID,
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))

	agent := &domain.Agent{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "Digest Agent", Slug: "digest-" + suffix,
		AgentType: domain.AgentTypeClaudeCode, Status: domain.AgentStatusOffline,
		APIKeyHash: "$2a$12$" + suffix, APIKeyPrefix: suffix, APIKeySHA256: digest,
	}
	require.NoError(t, NewAgentRepo(db).Create(ctx, agent))
	return agent
}

func TestAgentAPIKeySHA256_RoundTrips(t *testing.T) {
	db := agentDigestTestDB(t)
	digest := "deadbeef-" + uuid.New().String()
	agent := newDigestAgent(t, db, digest)

	got, err := NewAgentRepo(db).GetByAPIKeyPrefix(context.Background(), agent.WorkspaceID, agent.APIKeyPrefix)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, digest, got.APIKeySHA256)
}

// "Not populated" must reach the column as NULL. If it arrived as the empty string, the
// partial unique index would treat every un-populated agent as a duplicate of
// every other one, and the second Create would fail.
func TestAgentAPIKeySHA256_EmptyIsStoredAsNull(t *testing.T) {
	db := agentDigestTestDB(t)
	first := newDigestAgent(t, db, "")
	second := newDigestAgent(t, db, "")

	var isNull bool
	require.NoError(t, db.GetContext(context.Background(), &isNull,
		`SELECT api_key_sha256 IS NULL FROM agents WHERE id = $1`, first.ID))
	assert.True(t, isNull, "an unset digest must be NULL, not the empty string")

	got, err := NewAgentRepo(db).GetByID(context.Background(), second.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Empty(t, got.APIKeySHA256, "NULL must read back as the empty string")
}

// Two live agents sharing a key digest would mean one key authenticating as two
// identities. The database refuses it.
func TestAgentAPIKeySHA256_IsUniqueAcrossAgents(t *testing.T) {
	db := agentDigestTestDB(t)
	shared := "collision-digest-" + uuid.New().String()
	newDigestAgent(t, db, shared)

	ctx := context.Background()
	owner := &domain.User{
		ID: uuid.New(), Email: "dup-" + uuid.New().String()[:8] + "@example.com", PasswordHash: "x",
		Name: "Dup", Username: "dup-" + uuid.New().String()[:8], IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, owner))
	ws := &domain.Workspace{
		ID: uuid.New(), Name: "dup-ws", Slug: "dup-ws-" + uuid.New().String()[:8], OwnerID: owner.ID,
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))

	err := NewAgentRepo(db).Create(ctx, &domain.Agent{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "Dup Agent", Slug: "dup-" + uuid.New().String()[:8],
		AgentType: domain.AgentTypeClaudeCode, Status: domain.AgentStatusOffline,
		APIKeyHash: "$2a$12$other", APIKeyPrefix: uuid.New().String()[:8],
		APIKeySHA256: shared,
	})
	assert.Error(t, err, "the partial unique index must reject a duplicate digest")
}

func TestSetAPIKeySHA256_FillsAnEmptyRow(t *testing.T) {
	db := agentDigestTestDB(t)
	agent := newDigestAgent(t, db, "")
	repo := NewAgentRepo(db)

	fresh := "fresh-digest-" + uuid.New().String()
	require.NoError(t, repo.SetAPIKeySHA256(
		context.Background(), agent.ID, fresh, agent.APIKeyHash))

	got, err := repo.GetByID(context.Background(), agent.ID)
	require.NoError(t, err)
	assert.Equal(t, fresh, got.APIKeySHA256)
}

// The guard is the point: a rotation that landed between the bcrypt check and
// this write must make the UPDATE match nothing, rather than stamping the
// digest of the SUPERSEDED key onto the row and locking the new key out of the
// fast path while letting the old one through it.
func TestSetAPIKeySHA256_RefusesWhenTheBcryptHashMovedUnderIt(t *testing.T) {
	db := agentDigestTestDB(t)
	agent := newDigestAgent(t, db, "")
	repo := NewAgentRepo(db)

	require.NoError(t, repo.SetAPIKeySHA256(
		context.Background(), agent.ID, "digest-of-old-key-"+uuid.New().String(), "$2a$12$a-different-hash"))

	got, err := repo.GetByID(context.Background(), agent.ID)
	require.NoError(t, err)
	assert.Empty(t, got.APIKeySHA256,
		"the write must not land once the key it was verified against is gone")
}

// The backfill must not touch anything else on the row.
func TestSetAPIKeySHA256_LeavesTheRestOfTheRowAlone(t *testing.T) {
	db := agentDigestTestDB(t)
	agent := newDigestAgent(t, db, "")
	repo := NewAgentRepo(db)
	ctx := context.Background()

	before, err := repo.GetByID(ctx, agent.ID)
	require.NoError(t, err)

	require.NoError(t, repo.SetAPIKeySHA256(ctx, agent.ID, "d-"+uuid.New().String(), agent.APIKeyHash))

	after, err := repo.GetByID(ctx, agent.ID)
	require.NoError(t, err)

	before.APIKeySHA256 = after.APIKeySHA256
	assert.Equal(t, before, after)
}

// Update must carry the digest, not blank it — an agent read, modified and
// written back would otherwise silently return to the bcrypt path.
func TestAgentUpdate_PreservesTheDigest(t *testing.T) {
	db := agentDigestTestDB(t)
	kept := "kept-digest-" + uuid.New().String()
	agent := newDigestAgent(t, db, kept)
	repo := NewAgentRepo(db)
	ctx := context.Background()

	loaded, err := repo.GetByID(ctx, agent.ID)
	require.NoError(t, err)
	loaded.Name = "Renamed"
	require.NoError(t, repo.Update(ctx, loaded))

	got, err := repo.GetByID(ctx, agent.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed", got.Name)
	assert.Equal(t, kept, got.APIKeySHA256)
}
