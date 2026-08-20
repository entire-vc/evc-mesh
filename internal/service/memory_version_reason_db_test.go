package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/embedding"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// No //go:build integration tag, matching the sibling *_db_test.go files: CI's
// untagged `go test ./...` runs these against a migrated DATABASE_URL and
// skip()s when no database is reachable.
//
// Covers Mesh #0ba5e66a acceptance 1-3: a conditional write refuses to
// overwrite a version it did not expect, a reason is required when enforcement
// is on, and prior revisions stay readable with the reasons that produced them.

func versionTestDB(t *testing.T) *sqlx.DB {
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

func versionTestWorkspace(t *testing.T, db *sqlx.DB) *domain.Workspace {
	t.Helper()
	ws := &domain.Workspace{
		ID: uuid.New(), Name: "memver-ws",
		Slug: "memver-" + uuid.New().String()[:8], OwnerID: uuid.New(),
	}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(context.Background(), ws))
	return ws
}

func newVersionTestService(db *sqlx.DB) (MemoryService, *postgres.MemoryRepo) {
	memRepo := postgres.NewMemoryRepo(db)
	edgeRepo := postgres.NewMemoryEdgesRepo(db)
	return NewMemoryService(memRepo, edgeRepo, embedding.NewNoopEmbedder()), memRepo
}

// TestConcurrentWrite_StaleExpectedVersionIsRefused is acceptance 1, and it is
// deliberately a real race rather than two sequential calls.
//
// Both writers read the same version, then start together on a released
// barrier. Sequential calls would prove only that the comparison runs; they
// would not exercise the row lock, which is the part that can actually be
// wrong. The assertion is exactly-one-winner, which holds no matter which
// goroutine the scheduler favours — so the test is deterministic about the
// property while the race it runs is genuine.
func TestConcurrentWrite_StaleExpectedVersionIsRefused(t *testing.T) {
	db := versionTestDB(t)
	svc, _ := newVersionTestService(db)
	ctx := context.Background()
	ws := versionTestWorkspace(t, db)
	key := "memver-race-" + uuid.New().String()[:8]

	first, err := svc.Remember(ctx, &domain.Memory{
		WorkspaceID: ws.ID, Key: key, Content: "original",
		Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent,
	}, domain.MemoryWriteIntent{Reason: "seed"})
	require.NoError(t, err)
	require.Equal(t, 1, first.Version, "a create must start at version 1")

	stale := first.Version // both racers believe they are editing version 1

	// Eight racers rather than two, to make genuine overlap likely.
	//
	// ⚠️ Honest limit of this test, measured rather than assumed: it does NOT
	// demonstrate that the repository's SELECT ... FOR UPDATE is load-bearing.
	// Deleting FOR UPDATE leaves this test green at 2 racers AND at 8, across
	// repeated runs — the writers serialise on this driver and pool anyway, so
	// each one reads a version that has already moved and is refused by the
	// COMPARISON alone. What this test does prove is that a stale conditional
	// write is refused rather than silently applied, which is the acceptance
	// criterion. The lock stays because a read-modify-write across two
	// statements has a real interleaving window, not because anything here
	// turns red without it — and that distinction is recorded so nobody later
	// reads this test as evidence the lock is covered.
	const racers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // barrier: no writer runs until all are parked here
			_, e := svc.Remember(ctx, &domain.Memory{
				WorkspaceID: ws.ID, Key: key,
				Content: fmt.Sprintf("written by racer %d", idx),
				Scope:   domain.ScopeWorkspace, SourceType: domain.SourceAgent,
			}, domain.MemoryWriteIntent{
				Reason:          "concurrent edit",
				ExpectedVersion: &stale,
			})
			errs[idx] = e
		}(i)
	}
	close(start)
	wg.Wait()

	winners, conflicts := 0, 0
	for _, e := range errs {
		var c *domain.MemoryVersionConflictError
		switch {
		case e == nil:
			winners++
		case errors.As(e, &c):
			conflicts++
			assert.Equal(t, 1, c.Expected, "the loser expected the version it read")
			assert.Equal(t, 2, c.Actual, "and the stored version had already moved past it")
			assert.Contains(t, c.Error(), "modified by someone else",
				"the refusal has to say what happened, not just fail")
		default:
			t.Fatalf("unexpected error: %v", e)
		}
	}
	assert.Equal(t, 1, winners, "exactly one conditional write may succeed")
	assert.Equal(t, racers-1, conflicts, "every other writer must be refused, not silently applied")

	// The decisive assertion: the loser's content must not be in the row. A
	// counter saying "one conflict" would still be satisfied by an
	// implementation that reported a conflict and wrote anyway.
	stored, err := svc.GetByID(ctx, mustMemoryID(t, db, ws.ID, key))
	require.NoError(t, err)
	assert.Equal(t, 2, stored.Version,
		"exactly one write landed, so the stored version is 2 — a higher number "+
			"means a loser overwrote the winner after being told it had conflicted")
	assert.Contains(t, stored.Content, "written by racer")
}

// TestRemember_ReasonRequiredOnlyWhenEnabled is acceptance 2. Both directions
// matter: the rejection must happen when enforcement is on, and must NOT happen
// when it is off — the flag defaulting to off is what keeps the fleet writing
// while the `remember` tool learns to send a reason.
func TestRemember_ReasonRequiredOnlyWhenEnabled(t *testing.T) {
	db := versionTestDB(t)
	svc, _ := newVersionTestService(db)
	ctx := context.Background()
	ws := versionTestWorkspace(t, db)

	newMem := func() *domain.Memory {
		return &domain.Memory{
			WorkspaceID: ws.ID, Key: "memver-reason-" + uuid.New().String()[:8],
			Content: "some durable fact", Scope: domain.ScopeWorkspace,
			SourceType: domain.SourceAgent,
		}
	}

	t.Run("enforcement off: a write with no reason is accepted", func(t *testing.T) {
		t.Setenv(requireMemoryReasonEnv, "")
		_, err := svc.Remember(ctx, newMem(), domain.MemoryWriteIntent{})
		require.NoError(t, err, "with the flag off the fleet must keep writing")
	})

	t.Run("enforcement on: a write with no reason is refused by name", func(t *testing.T) {
		t.Setenv(requireMemoryReasonEnv, "1")
		_, err := svc.Remember(ctx, newMem(), domain.MemoryWriteIntent{})
		require.Error(t, err)
		// Assert on the STRUCTURED field, not err.Error(): the flattened string
		// is only "[400] Validation failed" — the per-field reason travels in
		// Validation and is what the transport renders into the response body.
		// Asserting on the string would pass against an implementation that
		// refused without ever saying why.
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		require.Contains(t, apiErr.Validation, "reason",
			"the refusal must name the missing field, not fail generically")
		assert.Contains(t, apiErr.Validation["reason"], "memory was not written",
			"and must say the write did not happen, or the caller cannot tell "+
				"refusal from acceptance-with-a-note")
	})

	t.Run("enforcement on: whitespace is not a reason", func(t *testing.T) {
		t.Setenv(requireMemoryReasonEnv, "1")
		_, err := svc.Remember(ctx, newMem(), domain.MemoryWriteIntent{Reason: "   \t "})
		require.Error(t, err, "blank and absent read identically to a human")
	})

	t.Run("enforcement on: a real reason passes", func(t *testing.T) {
		t.Setenv(requireMemoryReasonEnv, "1")
		_, err := svc.Remember(ctx, newMem(), domain.MemoryWriteIntent{
			Reason: "records the deploy ordering decision for the next migration",
		})
		require.NoError(t, err, "the gate must not block writes that comply")
	})
}

// TestRevisionHistory_KeepsWhatItSaidAndWhy is acceptance 3.
func TestRevisionHistory_KeepsWhatItSaidAndWhy(t *testing.T) {
	db := versionTestDB(t)
	svc, _ := newVersionTestService(db)
	ctx := context.Background()
	ws := versionTestWorkspace(t, db)
	key := "memver-hist-" + uuid.New().String()[:8]

	write := func(content, reason string) {
		_, err := svc.Remember(ctx, &domain.Memory{
			WorkspaceID: ws.ID, Key: key, Content: content,
			Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent,
		}, domain.MemoryWriteIntent{Reason: reason})
		require.NoError(t, err)
	}
	write("prod runs on tw-billing", "initial note after the cutover meeting")
	write("prod runs in Helsinki", "corrected: the host moved on 2026-07-09")

	id := mustMemoryID(t, db, ws.ID, key)
	revs, err := svc.ListRevisions(ctx, id, 10)
	require.NoError(t, err)
	require.Len(t, revs, 2, "one revision per write")

	assert.Equal(t, 2, revs[0].Version, "newest first")
	assert.Equal(t, domain.MemoryActionUpdated, revs[0].Action)
	require.NotNil(t, revs[0].Reason)
	assert.Contains(t, *revs[0].Reason, "the host moved")

	assert.Equal(t, 1, revs[1].Version)
	assert.Equal(t, domain.MemoryActionCreated, revs[1].Action)
	// The point of the whole table: what the memory USED to assert is still
	// readable after it has been corrected.
	assert.Equal(t, "prod runs on tw-billing", revs[1].Content,
		"the superseded claim must survive, or 'when did this stop being true' "+
			"stays unanswerable")
	require.NotNil(t, revs[1].Reason)
	assert.Contains(t, *revs[1].Reason, "initial note")
}

// TestForget_RecordsWhatItRemoved guards the schema decision that
// memory_revisions has no cascading foreign key. With one, this history would
// be deleted by the very operation it exists to record.
func TestForget_RecordsWhatItRemoved(t *testing.T) {
	db := versionTestDB(t)
	svc, _ := newVersionTestService(db)
	ctx := context.Background()
	ws := versionTestWorkspace(t, db)
	key := "memver-forget-" + uuid.New().String()[:8]

	// No AgentID: memories.agent_id is a real FK, and inventing a UUID here
	// fails on the constraint rather than on anything this test is about.
	// The delete therefore goes through the admin path.
	_, err := svc.Remember(ctx, &domain.Memory{
		WorkspaceID: ws.ID, Key: key, Content: "a claim somebody later deleted",
		Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent,
	}, domain.MemoryWriteIntent{Reason: "seed"})
	require.NoError(t, err)

	id := mustMemoryID(t, db, ws.ID, key)
	require.NoError(t, svc.Forget(ctx, id, nil, true, "superseded by the Helsinki cutover note"))

	// The memory is gone...
	_, err = svc.GetByID(ctx, id)
	require.Error(t, err, "forget must actually delete")

	// ...but the record of what it said, and why it went, is not.
	var count int
	require.NoError(t, db.GetContext(ctx, &count,
		`SELECT count(*) FROM memory_revisions WHERE memory_id = $1 AND action = 'forgotten'`, id))
	assert.Equal(t, 1, count, "the deletion itself must leave a trace")

	var content, reason string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT content, reason FROM memory_revisions WHERE memory_id=$1 AND action='forgotten'`, id).
		Scan(&content, &reason))
	assert.Equal(t, "a claim somebody later deleted", content)
	assert.Contains(t, reason, "Helsinki cutover")
}

func mustMemoryID(t *testing.T, db *sqlx.DB, wsID uuid.UUID, key string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT id FROM memories WHERE workspace_id=$1 AND key=$2 AND scope='workspace'`,
		wsID, key).Scan(&id))
	return id
}
