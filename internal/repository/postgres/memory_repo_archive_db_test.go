package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No //go:build integration tag on purpose — see userRepoTestDB's doc comment.
// Covers the repository half of the hand archive: the archive
// transaction and its revision, the hand-archived key lookup remember relies on,
// the exact-key read and the expiry grace period of CleanExpired.

type memoryArchiveRepoEnv struct {
	db      *sqlx.DB
	repo    *MemoryRepo
	ws      uuid.UUID
	project uuid.UUID
}

func setupMemoryArchiveRepoDBTest(t *testing.T) *memoryArchiveRepoEnv {
	t.Helper()
	db := userRepoTestDB(t)
	ws := &domain.Workspace{
		ID:      uuid.New(),
		Name:    "memory-archive-repo-db-test",
		Slug:    "memory-archive-repo-" + uuid.New().String()[:8],
		OwnerID: uuid.New(),
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(context.Background(), ws))
	project := uuid.New()
	_, err := db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1, $2, 'archive', $3)`,
		project, ws.ID, "archive-"+project.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM memory_revisions WHERE workspace_id = $1`, ws.ID)
		_, _ = db.Exec(`DELETE FROM memories WHERE workspace_id = $1`, ws.ID)
		_, _ = db.Exec(`DELETE FROM projects WHERE workspace_id = $1`, ws.ID)
	})
	return &memoryArchiveRepoEnv{db: db, repo: NewMemoryRepo(db), ws: ws.ID, project: project}
}

func (e *memoryArchiveRepoEnv) put(t *testing.T, key string, scope domain.MemoryScope, project *uuid.UUID) *domain.Memory {
	t.Helper()
	mem := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: e.ws,
		ProjectID:   project,
		Key:         key,
		Content:     "content of " + key,
		Scope:       scope,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, e.repo.Upsert(context.Background(), mem, domain.MemoryWriteIntent{Reason: "create"}))
	return mem
}

func keysOfMemories(ms []domain.Memory) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func TestMemoryRepoDB_SetArchivedWithRevision(t *testing.T) {
	e := setupMemoryArchiveRepoDBTest(t)
	ctx := context.Background()
	actor := uuid.New()
	mem := e.put(t, "archive-tx-"+uuid.New().String()[:8], domain.ScopeProject, &e.project)

	res, err := e.repo.SetArchivedWithRevision(ctx, uuid.New(), true, "x", &actor, nil)
	require.NoError(t, err)
	require.Nil(t, res, "an unknown id is nil, nil")

	res, err = e.repo.SetArchivedWithRevision(ctx, mem.ID, true, "  merged long ago \n", &actor, nil)
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.True(t, res.Memory.Archived)
	require.NotNil(t, res.RevisionID)
	require.NotNil(t, res.RevisionVersion)
	require.Equal(t, res.Memory.Version, *res.RevisionVersion)

	var rev struct {
		Action  string         `db:"action"`
		Reason  sql.NullString `db:"reason"`
		Actor   uuid.NullUUID  `db:"actor_agent_id"`
		Version int            `db:"version"`
	}
	require.NoError(t, e.db.Get(&rev, `SELECT action, reason, actor_agent_id, version FROM memory_revisions WHERE id = $1`, *res.RevisionID))
	require.Equal(t, domain.MemoryActionArchived, rev.Action)
	require.Equal(t, "merged long ago", rev.Reason.String, "the reason is stored trimmed")
	require.Equal(t, actor, rev.Actor.UUID)
	require.Equal(t, *res.RevisionVersion, rev.Version)

	// Already archived: untouched, no revision.
	again, err := e.repo.SetArchivedWithRevision(ctx, mem.ID, true, "again", &actor, nil)
	require.NoError(t, err)
	require.False(t, again.Changed)
	require.Nil(t, again.RevisionID)
	require.Equal(t, res.Memory.Version, again.Memory.Version)

	// A user restores it with a blank reason: the reason column is NULL, never an
	// empty string, and the user (not an agent) is the recorded actor.
	user := uuid.New()
	short := user.String()[:8]
	_, err = e.db.Exec(`INSERT INTO users (id, email, password_hash, display_name, username) VALUES ($1, $2, 'x', 'Archive Repo Test', $3)`,
		user, "archive-repo-"+short+"@example.invalid", "archive-repo-"+short)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = e.db.Exec(`DELETE FROM users WHERE id = $1`, user) })
	back, err := e.repo.SetArchivedWithRevision(ctx, mem.ID, false, " ", nil, &user)
	require.NoError(t, err)
	require.True(t, back.Changed)
	require.False(t, back.Memory.Archived)
	require.NoError(t, e.db.Get(&rev, `SELECT action, reason, actor_agent_id, version FROM memory_revisions WHERE id = $1`, *back.RevisionID))
	require.Equal(t, domain.MemoryActionUnarchived, rev.Action)
	require.False(t, rev.Reason.Valid)
	require.False(t, rev.Actor.Valid)

	revs, err := e.repo.ListRevisions(ctx, mem.ID, 10)
	require.NoError(t, err)
	require.Equal(t, domain.MemoryActionUnarchived, revs[0].Action)
	require.Equal(t, &user, revs[0].ActorUserID, "the user actor is read back with the history")
	require.Nil(t, revs[0].ActorAgentID)
	require.Equal(t, domain.MemoryActionArchived, revs[1].Action)
	require.Equal(t, &actor, revs[1].ActorAgentID)
	require.Nil(t, revs[1].ActorUserID)
}

func TestMemoryRepoDB_GetHandArchivedByKey(t *testing.T) {
	e := setupMemoryArchiveRepoDBTest(t)
	ctx := context.Background()
	key := "hand-archived-" + uuid.New().String()[:8]
	mem := e.put(t, key, domain.ScopeProject, &e.project)

	got, err := e.repo.GetHandArchivedByKey(ctx, e.ws, &e.project, nil, key, domain.ScopeProject)
	require.NoError(t, err)
	require.Nil(t, got, "a live row is not hand-archived")

	_, err = e.repo.SetArchivedWithRevision(ctx, mem.ID, true, "stale", nil, nil)
	require.NoError(t, err)
	got, err = e.repo.GetHandArchivedByKey(ctx, e.ws, &e.project, nil, key, domain.ScopeProject)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, mem.ID, got.ID)
	live, err := e.repo.GetByKey(ctx, e.ws, &e.project, nil, key, domain.ScopeProject)
	require.NoError(t, err)
	require.Nil(t, live, "GetByKey still treats it as not current")

	_, err = e.repo.SetArchivedWithRevision(ctx, mem.ID, false, "back", nil, nil)
	require.NoError(t, err)
	got, err = e.repo.GetHandArchivedByKey(ctx, e.ws, &e.project, nil, key, domain.ScopeProject)
	require.NoError(t, err)
	require.Nil(t, got, "the latest archive-state revision is an unarchive")

	// Archived by automation (no archive revision): not a hand archive.
	auto := e.put(t, "auto-archived-"+uuid.New().String()[:8], domain.ScopeProject, &e.project)
	require.NoError(t, e.repo.SetArchived(ctx, auto.ID, true))
	got, err = e.repo.GetHandArchivedByKey(ctx, e.ws, &e.project, nil, auto.Key, domain.ScopeProject)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestMemoryRepoDB_ListByKey(t *testing.T) {
	e := setupMemoryArchiveRepoDBTest(t)
	ctx := context.Background()
	key := "by-key-" + uuid.New().String()[:8]
	live := e.put(t, key, domain.ScopeProject, &e.project)
	wsRow := e.put(t, key, domain.ScopeWorkspace, nil)
	other := uuid.New()
	_, err := e.db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1, $2, 'other', $3)`, other, e.ws, "other-"+other.String())
	require.NoError(t, err)
	archived := e.put(t, key, domain.ScopeProject, &other)
	_, err = e.repo.SetArchivedWithRevision(ctx, archived.ID, true, "stale", nil, nil)
	require.NoError(t, err)
	_, err = e.db.Exec(`UPDATE memories SET expires_at = now() - interval '1 hour' WHERE id = $1`, wsRow.ID)
	require.NoError(t, err)

	list := func(l domain.MemoryKeyLookup) []uuid.UUID {
		t.Helper()
		l.WorkspaceID, l.Key = e.ws, key
		rows, lerr := e.repo.ListByKey(ctx, l)
		require.NoError(t, lerr)
		return keysOfMemories(rows)
	}

	require.Equal(t, []uuid.UUID{live.ID}, list(domain.MemoryKeyLookup{}), "archived and expired are hidden by default")
	require.ElementsMatch(t, []uuid.UUID{live.ID, wsRow.ID}, list(domain.MemoryKeyLookup{IncludeExpired: true}))
	all := list(domain.MemoryKeyLookup{IncludeExpired: true, IncludeArchived: true, Limit: 500})
	require.Len(t, all, 3, "an out-of-range limit falls back to the default")
	require.Equal(t, archived.ID, all[2], "archived rows sort last")
	require.Equal(t, []uuid.UUID{wsRow.ID}, list(domain.MemoryKeyLookup{Scope: domain.ScopeWorkspace, IncludeExpired: true, IncludeArchived: true}))
	require.Equal(t, []uuid.UUID{archived.ID}, list(domain.MemoryKeyLookup{ProjectID: &other, IncludeArchived: true}))
	require.Equal(t, []uuid.UUID{live.ID}, list(domain.MemoryKeyLookup{ProjectID: &e.project, IncludeExpired: true, IncludeArchived: true}))
	require.Len(t, list(domain.MemoryKeyLookup{IncludeExpired: true, IncludeArchived: true, Limit: 1}), 1)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = e.repo.ListByKey(cancelled, domain.MemoryKeyLookup{WorkspaceID: e.ws, Key: key})
	require.Error(t, err)
}

// CleanExpired keeps an expired row through the grace period and deletes it after.
func TestMemoryRepoDB_CleanExpiredKeepsGracePeriod(t *testing.T) {
	e := setupMemoryArchiveRepoDBTest(t)
	ctx := context.Background()
	recent := e.put(t, "expired-recent-"+uuid.New().String()[:8], domain.ScopeWorkspace, nil)
	old := e.put(t, "expired-old-"+uuid.New().String()[:8], domain.ScopeWorkspace, nil)
	setExpired := func(id uuid.UUID, ago time.Duration) {
		_, err := e.db.Exec(`UPDATE memories SET expires_at = $2 WHERE id = $1`, id, time.Now().Add(-ago))
		require.NoError(t, err)
	}
	setExpired(recent.ID, time.Hour)
	setExpired(old.ID, domain.ExpiredMemoryGracePeriod+time.Hour)

	_, err := e.repo.CleanExpired(ctx)
	require.NoError(t, err)
	exists := func(id uuid.UUID) bool {
		var n int
		require.NoError(t, e.db.Get(&n, `SELECT count(*) FROM memories WHERE id = $1`, id))
		return n == 1
	}
	require.True(t, exists(recent.ID), "expired an hour ago: still inside the grace period")
	require.False(t, exists(old.ID), "expired beyond the grace period: deleted")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = e.repo.CleanExpired(cancelled)
	require.Error(t, err)
	_, err = e.repo.SetArchivedWithRevision(cancelled, recent.ID, true, "x", nil, nil)
	require.Error(t, err)
}
