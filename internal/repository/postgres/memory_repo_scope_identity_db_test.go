package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No //go:build integration tag on purpose — see userRepoTestDB's doc comment
// in user_repo_test.go. These are the regression tests for task #4edf3fb5
// (memory identity must follow declared scope): they exercise GetByKey
// directly against real Postgres, since the identity contract lives in SQL,
// not in Go the mocked service-level tests can meaningfully verify.

func setupMemoryScopeIdentityDBTest(t *testing.T) (*MemoryRepo, uuid.UUID) {
	t.Helper()
	db := userRepoTestDB(t)

	wsRepo := NewWorkspaceRepo(db)
	ws := &domain.Workspace{
		ID:      uuid.New(),
		Name:    "scope-identity-db-test-ws",
		Slug:    "scope-identity-db-test-" + uuid.New().String()[:8],
		OwnerID: uuid.New(),
	}
	require.NoError(t, wsRepo.Create(context.Background(), ws))

	return NewMemoryRepo(db), ws.ID
}

func createTestProject(t *testing.T, db interface {
	Create(ctx context.Context, p *domain.Project) error
}, wsID uuid.UUID) uuid.UUID {
	t.Helper()
	proj := &domain.Project{
		ID:                  uuid.New(),
		WorkspaceID:         wsID,
		Name:                "scope-identity-test-project",
		Slug:                "scope-identity-test-" + uuid.New().String()[:8],
		DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, db.Create(context.Background(), proj))
	return proj.ID
}

func createTestAgent(t *testing.T, repo *AgentRepo, wsID uuid.UUID) uuid.UUID {
	t.Helper()
	slug := "scope-identity-agent-" + uuid.New().String()[:8]
	agent := &domain.Agent{
		ID:           uuid.New(),
		WorkspaceID:  wsID,
		Name:         slug,
		Slug:         slug,
		AgentType:    domain.AgentTypeCustom,
		Status:       domain.AgentStatusOffline,
		APIKeyHash:   "irrelevant-hash",
		APIKeyPrefix: "irrelevant",
	}
	require.NoError(t, repo.Create(context.Background(), agent))
	return agent.ID
}

// TestMemoryRepoDB_WorkspaceScope_IdentityIgnoresProjectAndAgent is the
// direct regression test for AC1 (fork-repro): a workspace-scoped memory's
// identity is (workspace_id, key) — GetByKey must find the SAME row
// regardless of which project_id is passed on the lookup, so a second
// remember() call with a different project_id upserts instead of forking.
func TestMemoryRepoDB_WorkspaceScope_IdentityIgnoresProjectAndAgent(t *testing.T) {
	repo, wsID := setupMemoryScopeIdentityDBTest(t)
	projRepo := NewProjectRepo(repo.db)
	ctx := context.Background()

	proj1 := createTestProject(t, projRepo, wsID)
	proj2 := createTestProject(t, projRepo, wsID)
	key := "fork-repro-" + uuid.New().String()[:8]

	m1 := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		ProjectID:   &proj1,
		Key:         key,
		Content:     "v1",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, m1))

	// Same workspace+key, but looked up with a DIFFERENT project_id — must
	// still find m1 (identity does not include project_id for scope=workspace).
	found, err := repo.GetByKey(ctx, wsID, &proj2, nil, key, domain.ScopeWorkspace)
	require.NoError(t, err)
	require.NotNil(t, found, "workspace-scope GetByKey must ignore project_id")
	assert.Equal(t, m1.ID, found.ID)

	// Also must find it with project_id=nil.
	found2, err := repo.GetByKey(ctx, wsID, nil, nil, key, domain.ScopeWorkspace)
	require.NoError(t, err)
	require.NotNil(t, found2)
	assert.Equal(t, m1.ID, found2.ID)

	// And with a differing agent_id.
	agentRepo := NewAgentRepo(repo.db)
	agentID := createTestAgent(t, agentRepo, wsID)
	found3, err := repo.GetByKey(ctx, wsID, nil, &agentID, key, domain.ScopeWorkspace)
	require.NoError(t, err)
	require.NotNil(t, found3, "workspace-scope GetByKey must ignore agent_id")
	assert.Equal(t, m1.ID, found3.ID)

	// Simulate the service-layer upsert: same ID reused, project_id updated —
	// there must still be exactly ONE row for (wsID, key) afterward.
	m1.ProjectID = &proj2
	m1.Content = "v2"
	require.NoError(t, repo.Upsert(ctx, m1))

	var count int
	require.NoError(t, repo.db.GetContext(ctx, &count,
		`SELECT count(*) FROM memories WHERE workspace_id = $1 AND key = $2 AND scope = 'workspace'`,
		wsID, key))
	assert.Equal(t, 1, count, "must be exactly one row — no fork")
}

// TestMemoryRepoDB_ProjectScope_IdentityIsPerProject is the direct regression
// test for AC2: the same key under scope=project in two different projects
// must remain two SEPARATE entries (by design, not a bug).
func TestMemoryRepoDB_ProjectScope_IdentityIsPerProject(t *testing.T) {
	repo, wsID := setupMemoryScopeIdentityDBTest(t)
	projRepo := NewProjectRepo(repo.db)
	ctx := context.Background()

	proj1 := createTestProject(t, projRepo, wsID)
	proj2 := createTestProject(t, projRepo, wsID)
	key := "per-project-" + uuid.New().String()[:8]

	m1 := &domain.Memory{
		ID: uuid.New(), WorkspaceID: wsID, ProjectID: &proj1, Key: key,
		Content: "proj1 content", Scope: domain.ScopeProject, SourceType: domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, m1))

	m2 := &domain.Memory{
		ID: uuid.New(), WorkspaceID: wsID, ProjectID: &proj2, Key: key,
		Content: "proj2 content", Scope: domain.ScopeProject, SourceType: domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, m2))

	require.NotEqual(t, m1.ID, m2.ID, "test setup sanity check")

	found1, err := repo.GetByKey(ctx, wsID, &proj1, nil, key, domain.ScopeProject)
	require.NoError(t, err)
	require.NotNil(t, found1)
	assert.Equal(t, m1.ID, found1.ID)
	assert.Equal(t, "proj1 content", found1.Content)

	found2, err := repo.GetByKey(ctx, wsID, &proj2, nil, key, domain.ScopeProject)
	require.NoError(t, err)
	require.NotNil(t, found2)
	assert.Equal(t, m2.ID, found2.ID)
	assert.Equal(t, "proj2 content", found2.Content)

	var count int
	require.NoError(t, repo.db.GetContext(ctx, &count,
		`SELECT count(*) FROM memories WHERE workspace_id = $1 AND key = $2 AND scope = 'project'`,
		wsID, key))
	assert.Equal(t, 2, count, "distinct projects must yield distinct rows")

	// A different agent_id must NOT fork a project-scoped row — identity for
	// scope=project excludes agent_id, same as workspace-scope excludes it.
	agentRepo := NewAgentRepo(repo.db)
	agentID := createTestAgent(t, agentRepo, wsID)
	foundIgnoringAgent, err := repo.GetByKey(ctx, wsID, &proj1, &agentID, key, domain.ScopeProject)
	require.NoError(t, err)
	require.NotNil(t, foundIgnoringAgent)
	assert.Equal(t, m1.ID, foundIgnoringAgent.ID)
}

// TestMemoryRepoDB_AgentScope_IdentityIgnoresProject verifies the third leg
// of the scope-identity decision: scope=agent keys on (workspace, agent_id,
// key) and ignores project_id.
func TestMemoryRepoDB_AgentScope_IdentityIgnoresProject(t *testing.T) {
	repo, wsID := setupMemoryScopeIdentityDBTest(t)
	projRepo := NewProjectRepo(repo.db)
	agentRepo := NewAgentRepo(repo.db)
	ctx := context.Background()

	agent1 := createTestAgent(t, agentRepo, wsID)
	agent2 := createTestAgent(t, agentRepo, wsID)
	proj1 := createTestProject(t, projRepo, wsID)
	proj2 := createTestProject(t, projRepo, wsID)
	key := "agent-scope-" + uuid.New().String()[:8]

	m1 := &domain.Memory{
		ID: uuid.New(), WorkspaceID: wsID, AgentID: &agent1, ProjectID: &proj1, Key: key,
		Content: "agent1 content", Scope: domain.ScopeAgent, SourceType: domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, m1))

	// Different project_id on the lookup must still find m1 — agent-scope
	// identity ignores project_id.
	found, err := repo.GetByKey(ctx, wsID, &proj2, &agent1, key, domain.ScopeAgent)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, m1.ID, found.ID)

	// A different agent with the same key must be a SEPARATE entry.
	m2 := &domain.Memory{
		ID: uuid.New(), WorkspaceID: wsID, AgentID: &agent2, Key: key,
		Content: "agent2 content", Scope: domain.ScopeAgent, SourceType: domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, m2))

	found2, err := repo.GetByKey(ctx, wsID, nil, &agent2, key, domain.ScopeAgent)
	require.NoError(t, err)
	require.NotNil(t, found2)
	assert.Equal(t, m2.ID, found2.ID)

	var count int
	require.NoError(t, repo.db.GetContext(ctx, &count,
		`SELECT count(*) FROM memories WHERE workspace_id = $1 AND key = $2 AND scope = 'agent'`,
		wsID, key))
	assert.Equal(t, 2, count, "distinct agents must yield distinct rows")
}
