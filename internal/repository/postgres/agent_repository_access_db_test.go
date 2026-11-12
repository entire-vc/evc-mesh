package postgres

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

type repoAccessFixture struct {
	db       *sqlx.DB
	repo     *AgentRepositoryAccessRepo
	ws       uuid.UUID
	owner    uuid.UUID // owner of ws, not the operator
	operator uuid.UUID // operator of target and manager, not a member of ws
	target   uuid.UUID
	manager  uuid.UUID // same operator; gets the runtime_manager grant
	plain    uuid.UUID // same workspace, no operator, no grant
}

func seedRepoAccess(t *testing.T) repoAccessFixture {
	t.Helper()
	db := agentDigestTestDB(t)
	ctx := context.Background()
	ws, other := seedGrantWorkspace(t, db), seedGrantWorkspace(t, db)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id IN ($1,$2)`, ws, other)
	})
	f := repoAccessFixture{db: db, repo: NewAgentRepositoryAccessRepo(db), ws: ws}
	require.NoError(t, db.GetContext(ctx, &f.owner, `SELECT owner_id FROM workspaces WHERE id=$1`, ws))
	require.NoError(t, db.GetContext(ctx, &f.operator, `SELECT owner_id FROM workspaces WHERE id=$1`, other))
	f.target, f.manager, f.plain = seedGrantAgent(t, db, ws).ID, seedGrantAgent(t, db, ws).ID, seedGrantAgent(t, db, ws).ID
	_, err := db.ExecContext(ctx, `UPDATE agents SET operator_user_id=$1 WHERE id IN ($2,$3)`, f.operator, f.target, f.manager)
	require.NoError(t, err)
	return f
}

func repoAccessPut(rev int64, refs ...domain.RepositoryRef) AgentRepositoryAccessPut {
	if refs == nil {
		refs = []domain.RepositoryRef{}
	}
	return AgentRepositoryAccessPut{ExpectedRevision: &rev, Repositories: refs}
}

var (
	refMesh   = domain.RepositoryRef{Provider: "gitlab", Host: "git.example.org", ProjectPath: "entire-vc/evc-mesh"}
	refRunner = domain.RepositoryRef{Provider: "gitlab", Host: "git.example.org", ProjectPath: "entire-vc/evc-mesh-runner"}
)

func (f repoAccessFixture) auditRows(t *testing.T, agent uuid.UUID) []AgentRepositoryAccessAudit {
	t.Helper()
	var raw []json.RawMessage
	require.NoError(t, f.db.Select(&raw, `SELECT metadata FROM agent_activity_log
		WHERE agent_id=$1 AND event_type=$2 ORDER BY created_at, id`, agent, domain.AgentRepositoryAccessChangedEvent))
	out := make([]AgentRepositoryAccessAudit, len(raw))
	for i := range raw {
		require.NoError(t, json.Unmarshal(raw[i], &out[i]))
	}
	return out
}

// Legacy (never configured) reads as nil; the first write from revision 0 is
// stored normalized with revision 1; a stale revision is 409 and changes
// nothing; [] is a configured deny-all, distinct from nil; every change leaves
// one audit event with actor and old/new list and revision.
func TestAgentRepositoryAccess_WriteReadCASAndAudit(t *testing.T) {
	f := seedRepoAccess(t)
	ctx := context.Background()
	operator := UserRuntimeActor{UserID: f.operator}

	got, err := f.repo.Get(ctx, f.target)
	require.NoError(t, err)
	require.Nil(t, got, "never configured = legacy = nil")

	saved, err := f.repo.Put(ctx, operator, f.target, repoAccessPut(0,
		domain.RepositoryRef{Provider: "GitLab", Host: "https://Git.Example.org/", ProjectPath: "entire-vc/evc-mesh.git"}))
	require.NoError(t, err)
	require.Equal(t, &domain.AgentRepositoryAccess{Revision: 1, Repositories: []domain.RepositoryRef{refMesh}}, saved)
	got, err = f.repo.Get(ctx, f.target)
	require.NoError(t, err)
	require.Equal(t, saved, got, "re-read returns exactly the saved normalized state")

	_, err = f.repo.Put(ctx, operator, f.target, repoAccessPut(0, refRunner))
	require.Error(t, err)
	require.Equal(t, 409, statusOf(t, err), err)
	got, err = f.repo.Get(ctx, f.target)
	require.NoError(t, err)
	require.Equal(t, saved, got, "a stale write changes nothing")

	// A malformed list is refused before anything is read or written.
	_, err = f.repo.Put(ctx, operator, f.target, repoAccessPut(1, refMesh, domain.RepositoryRef{Provider: "gitlab", Host: "GIT.example.org", ProjectPath: "entire-vc/evc-mesh/"}))
	require.Equal(t, 400, statusOf(t, err), err)
	_, err = f.repo.Put(ctx, operator, f.target, AgentRepositoryAccessPut{Repositories: []domain.RepositoryRef{}})
	require.Equal(t, 400, statusOf(t, err), "expected_revision is required")

	denyAll, err := f.repo.Put(ctx, operator, f.target, repoAccessPut(1))
	require.NoError(t, err)
	require.EqualValues(t, 2, denyAll.Revision)
	got, err = f.repo.Get(ctx, f.target)
	require.NoError(t, err)
	require.NotNil(t, got, "configured [] is not legacy")
	require.NotNil(t, got.Repositories)
	require.Empty(t, got.Repositories)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, `{"revision":2,"repositories":[]}`, string(encoded))

	audit := f.auditRows(t, f.target)
	require.Len(t, audit, 2, "one event per successful write, none for refused ones")
	require.Equal(t, AgentRepositoryAccessAudit{ActorType: "user", ActorID: f.operator, OldRevision: 0, NewRevision: 1,
		OldRepositories: nil, NewRepositories: []domain.RepositoryRef{refMesh}}, audit[0])
	require.Equal(t, AgentRepositoryAccessAudit{ActorType: "user", ActorID: f.operator, OldRevision: 1, NewRevision: 2,
		OldRepositories: []domain.RepositoryRef{refMesh}, NewRepositories: []domain.RepositoryRef{}}, audit[1])
	var rawFirst string
	require.NoError(t, f.db.Get(&rawFirst, `SELECT metadata::text FROM agent_activity_log
		WHERE agent_id=$1 AND event_type=$2 ORDER BY created_at, id LIMIT 1`, f.target, domain.AgentRepositoryAccessChangedEvent))
	require.Contains(t, rawFirst, `"old_repositories": null`, "legacy old state is recorded as null, not []")
}

// Two writers on the same revision: exactly one wins, the other gets 409, and
// after re-reading it lands on top — neither change is lost.
func TestAgentRepositoryAccess_ConcurrentWritersLoseNothing(t *testing.T) {
	f := seedRepoAccess(t)
	ctx := context.Background()
	operator := UserRuntimeActor{UserID: f.operator}
	lists := [][]domain.RepositoryRef{{refMesh}, {refRunner}}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range lists {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.repo.Put(ctx, operator, f.target, repoAccessPut(0, lists[i]...))
		}(i)
	}
	wg.Wait()
	winner, loser := 0, 1
	if errs[0] != nil {
		winner, loser = 1, 0
	}
	require.NoError(t, errs[winner])
	require.Error(t, errs[loser])
	require.Equal(t, 409, statusOf(t, errs[loser]), errs[loser])

	cur, err := f.repo.Get(ctx, f.target)
	require.NoError(t, err)
	require.Equal(t, &domain.AgentRepositoryAccess{Revision: 1, Repositories: lists[winner]}, cur)
	merged := append(append([]domain.RepositoryRef{}, cur.Repositories...), lists[loser]...)
	_, err = f.repo.Put(ctx, operator, f.target, repoAccessPut(cur.Revision, merged...))
	require.NoError(t, err)
	cur, err = f.repo.Get(ctx, f.target)
	require.NoError(t, err)
	require.EqualValues(t, 2, cur.Revision)
	require.ElementsMatch(t, []domain.RepositoryRef{refMesh, refRunner}, cur.Repositories)
	require.Len(t, f.auditRows(t, f.target), 2)
}

// Who may write: the operator, a workspace owner/admin, and another agent's
// live runtime_manager of the same operator. Never: a member, a stranger, an
// agent without the grant, an OAuth connector, a manager of another operator,
// a revoked manager, and a manager on itself. A refusal writes nothing.
func TestAgentRepositoryAccess_WriterMatrix(t *testing.T) {
	f := seedRepoAccess(t)
	ctx := context.Background()
	users := NewUserRuntimeRepo(f.db)
	rev := int64(0)

	allowed := func(actor UserRuntimeActor, agent uuid.UUID, actorType string, actorID uuid.UUID) {
		t.Helper()
		cur, err := f.repo.Get(ctx, agent)
		require.NoError(t, err)
		var at int64
		if cur != nil {
			at = cur.Revision
		}
		saved, err := f.repo.Put(ctx, actor, agent, repoAccessPut(at, refMesh))
		require.NoError(t, err)
		require.Equal(t, at+1, saved.Revision)
		audit := f.auditRows(t, agent)
		require.Equal(t, actorType, audit[len(audit)-1].ActorType)
		require.Equal(t, actorID, audit[len(audit)-1].ActorID)
		if agent == f.target {
			rev = saved.Revision
		}
	}
	refused := func(actor UserRuntimeActor, agent uuid.UUID, reason string) {
		t.Helper()
		before, err := f.repo.Get(ctx, agent)
		require.NoError(t, err)
		var at int64
		if before != nil {
			at = before.Revision
		}
		auditBefore := len(f.auditRows(t, agent))
		_, err = f.repo.Put(ctx, actor, agent, repoAccessPut(at, refRunner))
		require.Error(t, err)
		require.Equal(t, 403, statusOf(t, err), err)
		require.Contains(t, err.Error(), reason)
		after, err := f.repo.Get(ctx, agent)
		require.NoError(t, err)
		require.Equal(t, before, after, "a refused write leaves the access as it was")
		require.Len(t, f.auditRows(t, agent), auditBefore, "a refused write leaves no audit event")
	}

	allowed(UserRuntimeActor{UserID: f.operator}, f.target, "user", f.operator)
	allowed(UserRuntimeActor{UserID: f.owner}, f.target, "user", f.owner)

	member := seedGrantUser(t, f.db)
	_, err := f.db.ExecContext(ctx, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,'member')`, uuid.New(), f.ws, member)
	require.NoError(t, err)
	refused(UserRuntimeActor{UserID: member}, f.target, "operator or a workspace owner/admin")
	_, err = f.db.ExecContext(ctx, `UPDATE workspace_members SET role='admin' WHERE workspace_id=$1 AND user_id=$2`, f.ws, member)
	require.NoError(t, err)
	allowed(UserRuntimeActor{UserID: member}, f.target, "user", member)

	refused(UserRuntimeActor{UserID: seedGrantUser(t, f.db)}, f.target, "operator or a workspace owner/admin")
	refused(UserRuntimeActor{}, f.target, "operator or a workspace owner/admin")
	// An OAuth connector is refused even when its consenting user is the operator.
	refused(UserRuntimeActor{UserID: f.operator, AgentID: f.plain, Connector: true}, f.target, "OAuth connector")
	refused(UserRuntimeActor{AgentID: f.plain}, f.target, "runtime_manager grant")
	refused(UserRuntimeActor{AgentID: f.manager}, f.target, "runtime_manager grant")
	refused(UserRuntimeActor{AgentID: f.target}, f.target, "cannot change its own")

	require.NoError(t, users.GrantManager(ctx, UserRuntimeActor{UserID: f.operator}, f.manager))
	allowed(UserRuntimeActor{AgentID: f.manager}, f.target, "agent", f.manager)
	refused(UserRuntimeActor{AgentID: f.manager}, f.manager, "cannot change its own")
	// The manager's operator does not run plain: another operator's agent.
	refused(UserRuntimeActor{AgentID: f.manager}, f.plain, "runtime_manager grant")
	// A human still changes the manager's own access.
	allowed(UserRuntimeActor{UserID: f.operator}, f.manager, "user", f.operator)

	require.NoError(t, users.RevokeManager(ctx, UserRuntimeActor{UserID: f.operator}, f.manager))
	refused(UserRuntimeActor{AgentID: f.manager}, f.target, "runtime_manager grant")

	got, err := f.repo.Get(ctx, f.target)
	require.NoError(t, err)
	require.Equal(t, rev, got.Revision)

	_, err = f.repo.Put(ctx, UserRuntimeActor{UserID: f.operator}, uuid.New(), repoAccessPut(0))
	require.Equal(t, 404, statusOf(t, err), err)
}

// Generic writers of the agents row keep repository_access as it was: they
// read through agentSelectCols and UPDATE named columns that do not include it.
func TestAgentRepositoryAccess_GenericAgentWritesKeepIt(t *testing.T) {
	f := seedRepoAccess(t)
	ctx := context.Background()
	saved, err := f.repo.Put(ctx, UserRuntimeActor{UserID: f.operator}, f.target, repoAccessPut(0, refMesh))
	require.NoError(t, err)

	agents := NewAgentRepo(f.db)
	a, err := agents.GetByID(ctx, f.target)
	require.NoError(t, err)
	require.Nil(t, a.RepositoryAccess, "the generic read does not load it")
	a.RepositoryAccess = &domain.AgentRepositoryAccess{Revision: 99, Repositories: []domain.RepositoryRef{refRunner}}
	a.Name += " renamed"
	require.NoError(t, agents.Update(ctx, a))
	require.NoError(t, agents.UpdateHeartbeat(ctx, f.target, &repository.UpdateHeartbeatParams{
		Status: "ok", Metadata: json.RawMessage(`{"repository_access":{"revision":99,"repositories":[]}}`)}))
	require.NoError(t, agents.UpdateStatus(ctx, f.target, domain.AgentStatusBusy))

	got, err := f.repo.Get(ctx, f.target)
	require.NoError(t, err)
	require.Equal(t, saved, got)
}

// History is the audit read for the editor: newest first, actor with its name,
// old = null for the first write, pages joined by next_cursor without gaps or
// repeats, 400 for a bad limit or cursor, 404 for an unknown agent. Entries of
// another agent and other event types never show.
func TestAgentRepositoryAccess_History(t *testing.T) {
	f := seedRepoAccess(t)
	ctx := context.Background()
	operator := UserRuntimeActor{UserID: f.operator}
	users := NewUserRuntimeRepo(f.db)
	require.NoError(t, users.GrantManager(ctx, operator, f.manager))

	empty, err := f.repo.History(ctx, f.target, 0, "")
	require.NoError(t, err)
	require.NotNil(t, empty.Items, "no history is [], not null")
	require.Empty(t, empty.Items)
	require.Nil(t, empty.NextCursor)

	_, err = f.repo.Put(ctx, operator, f.target, repoAccessPut(0, refMesh))
	require.NoError(t, err)
	_, err = f.repo.Put(ctx, UserRuntimeActor{UserID: f.owner}, f.target, repoAccessPut(1, refMesh, refRunner))
	require.NoError(t, err)
	_, err = f.repo.Put(ctx, UserRuntimeActor{AgentID: f.manager}, f.target, repoAccessPut(2))
	require.NoError(t, err)
	// Noise that must not show: another agent's write and another event type.
	_, err = f.repo.Put(ctx, operator, f.manager, repoAccessPut(0, refRunner))
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `INSERT INTO agent_activity_log (id, agent_id, workspace_id, event_type, message, metadata, created_at)
		VALUES ($1, $2, $3, 'status_changed', 'noise', '{}', now())`, uuid.New(), f.target, f.ws)
	require.NoError(t, err)

	var operatorName, ownerName, managerName string
	require.NoError(t, f.db.Get(&operatorName, `SELECT COALESCE(NULLIF(display_name,''), username) FROM users WHERE id=$1`, f.operator))
	require.NoError(t, f.db.Get(&ownerName, `SELECT COALESCE(NULLIF(display_name,''), username) FROM users WHERE id=$1`, f.owner))
	require.NoError(t, f.db.Get(&managerName, `SELECT name FROM agents WHERE id=$1`, f.manager))
	require.NotEmpty(t, operatorName)
	require.NotEmpty(t, managerName)

	all, err := f.repo.History(ctx, f.target, 0, "")
	require.NoError(t, err)
	require.Nil(t, all.NextCursor)
	require.Len(t, all.Items, 3)
	newest, middle, first := all.Items[0], all.Items[1], all.Items[2]

	require.Equal(t, RepositoryAccessActor{Type: "agent", ID: f.manager, Name: managerName}, newest.Actor)
	require.Equal(t, &RepositoryAccessState{Revision: 2, Repositories: []domain.RepositoryRef{refMesh, refRunner}}, newest.Old)
	require.Equal(t, RepositoryAccessState{Revision: 3, Repositories: []domain.RepositoryRef{}}, newest.New)

	require.Equal(t, RepositoryAccessActor{Type: "user", ID: f.owner, Name: ownerName}, middle.Actor)
	require.EqualValues(t, 1, middle.Old.Revision)
	require.EqualValues(t, 2, middle.New.Revision)

	require.Equal(t, RepositoryAccessActor{Type: "user", ID: f.operator, Name: operatorName}, first.Actor)
	require.Nil(t, first.Old, "the first write had no configured state before it")
	require.Equal(t, RepositoryAccessState{Revision: 1, Repositories: []domain.RepositoryRef{refMesh}}, first.New)
	require.False(t, first.CreatedAt.After(middle.CreatedAt))

	encoded, err := json.Marshal(first)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"old":null`)

	// Paging by one returns the same three items in the same order, then stops.
	var paged []uuid.UUID
	cursor := ""
	for i := 0; i < 5; i++ {
		page, perr := f.repo.History(ctx, f.target, 1, cursor)
		require.NoError(t, perr)
		require.Len(t, page.Items, 1)
		paged = append(paged, page.Items[0].ID)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	require.Equal(t, []uuid.UUID{newest.ID, middle.ID, first.ID}, paged)

	for _, limit := range []int{-1, RepositoryAccessHistoryMaxLimit + 1} {
		_, err = f.repo.History(ctx, f.target, limit, "")
		require.Equal(t, 400, statusOf(t, err), "limit %d", limit)
	}
	for _, bad := range []string{"%%%", "bm90LWEtY3Vyc29y", "eA"} {
		_, err = f.repo.History(ctx, f.target, 1, bad)
		require.Equal(t, 400, statusOf(t, err), "cursor %q", bad)
	}
	_, err = f.repo.History(ctx, uuid.New(), 0, "")
	require.Equal(t, 404, statusOf(t, err))
}
