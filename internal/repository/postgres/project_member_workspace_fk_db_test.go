package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No //go:build integration tag — same convention as the other *_db_test.go
// files here.
//
// These prove the schema-level guard added in migration
// 20260819094_project_members_workspace_composite_fk.sql: a project_members row
// naming a principal (agent or user) outside the project's workspace must be
// rejected by the database itself, not merely by the application-level funnel
// (ensureAssigneeProjectMember, PR #527 / task a0cf0c42). The application guard
// already covers every write path in internal/service; this is the second,
// independent layer against direct SQL, a future backfill, or a service that
// writes this table outside internal/service.
//
// The AGENT half of that guard (fk_pm_agent_workspace) was replaced by
// migration 20260911001 with a trigger (check_pm_agent_workspace) — a bare FK
// only knows "agent_id's home workspace_id", and task U1/U3's guest grants
// (agent_workspace_grants) added a second, legitimate way to belong that a FK
// cannot express. TestProjectMemberCreate_ForeignAgent_RejectedBySchema below
// now asserts on the trigger's own exception text instead of the dropped
// constraint's name; TestProjectMemberCreate_GuestAgentWithActiveGrant_*
// and its revoked-grant sibling are new, proving the trigger's OR-branch
// both ways (task #80dfb336 — found live: the mock-backed AddAgentMember
// unit tests passed with the FK still in place; only a real Postgres caught
// that the service-layer fix alone was not sufficient).

func pmFKTestDB(t *testing.T) *sqlx.DB {
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

// pmFKFixture is one workspace with an owner, one project in it, one agent
// native to it, and one project + agent in a SECOND, unrelated workspace to use
// as the "foreign" principal.
type pmFKFixture struct {
	nativeWorkspaceID uuid.UUID
	nativeProjectID   uuid.UUID
	nativeAgentID     uuid.UUID
	nativeUserID      uuid.UUID
	foreignAgentID    uuid.UUID
	foreignUserID     uuid.UUID
}

func newPMFKFixture(t *testing.T, db *sqlx.DB) pmFKFixture {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	nativeOwner := &domain.User{
		ID: uuid.New(), Email: "pmfk-native-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Native Owner", Username: "pmfk-native-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, nativeOwner))
	foreignOwner := &domain.User{
		ID: uuid.New(), Email: "pmfk-foreign-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Foreign Owner", Username: "pmfk-foreign-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, foreignOwner))

	nativeWS := &domain.Workspace{
		ID: uuid.New(), Name: "pmfk-native-ws", Slug: "pmfk-native-ws-" + suffix, OwnerID: nativeOwner.ID,
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, nativeWS))
	foreignWS := &domain.Workspace{
		ID: uuid.New(), Name: "pmfk-foreign-ws", Slug: "pmfk-foreign-ws-" + suffix, OwnerID: foreignOwner.ID,
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, foreignWS))

	require.NoError(t, NewWorkspaceMemberRepo(db).Create(ctx, &domain.WorkspaceMember{
		ID: uuid.New(), WorkspaceID: nativeWS.ID, UserID: nativeOwner.ID, Role: "owner",
	}))
	require.NoError(t, NewWorkspaceMemberRepo(db).Create(ctx, &domain.WorkspaceMember{
		ID: uuid.New(), WorkspaceID: foreignWS.ID, UserID: foreignOwner.ID, Role: "owner",
	}))

	nativeProject := &domain.Project{
		ID: uuid.New(), WorkspaceID: nativeWS.ID, Name: "pmfk-native-proj", Slug: "pmfk-native-proj-" + suffix,
		DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, nativeProject))

	nativeAgent := &domain.Agent{
		ID: uuid.New(), WorkspaceID: nativeWS.ID, Name: "pmfk-native-agent", Slug: "pmfk-native-agent-" + suffix,
		AgentType: domain.AgentTypeClaudeCode, Status: domain.AgentStatusOffline,
		APIKeyHash: "$2a$12$" + suffix, APIKeyPrefix: "n-" + suffix,
	}
	require.NoError(t, NewAgentRepo(db).Create(ctx, nativeAgent))

	foreignAgent := &domain.Agent{
		ID: uuid.New(), WorkspaceID: foreignWS.ID, Name: "pmfk-foreign-agent", Slug: "pmfk-foreign-agent-" + suffix,
		AgentType: domain.AgentTypeClaudeCode, Status: domain.AgentStatusOffline,
		APIKeyHash: "$2a$12$" + suffix, APIKeyPrefix: "f-" + suffix,
	}
	require.NoError(t, NewAgentRepo(db).Create(ctx, foreignAgent))

	return pmFKFixture{
		nativeWorkspaceID: nativeWS.ID,
		nativeProjectID:   nativeProject.ID,
		nativeAgentID:     nativeAgent.ID,
		nativeUserID:      nativeOwner.ID,
		foreignAgentID:    foreignAgent.ID,
		foreignUserID:     foreignOwner.ID,
	}
}

// Positive control: a native agent enrolling in its own workspace's project
// must succeed, and workspace_id must round-trip as the project's own workspace
// — proves the guard isn't rejecting everything.
func TestProjectMemberCreate_NativeAgent_Succeeds(t *testing.T) {
	db := pmFKTestDB(t)
	fx := newPMFKFixture(t, db)
	ctx := context.Background()

	err := NewProjectMemberRepo(db).Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: fx.nativeProjectID, AgentID: &fx.nativeAgentID,
		Role: "member", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	require.NoError(t, err)

	var ws uuid.UUID
	require.NoError(t, db.GetContext(ctx, &ws,
		`SELECT workspace_id FROM project_members WHERE project_id = $1 AND agent_id = $2`,
		fx.nativeProjectID, fx.nativeAgentID))
	assert.Equal(t, fx.nativeWorkspaceID, ws, "workspace_id must be derived from the project, not left unset")
}

// Positive control: a native user enrolling in its own workspace's project
// must succeed.
func TestProjectMemberCreate_NativeUser_Succeeds(t *testing.T) {
	db := pmFKTestDB(t)
	fx := newPMFKFixture(t, db)
	ctx := context.Background()

	err := NewProjectMemberRepo(db).Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: fx.nativeProjectID, UserID: &fx.nativeUserID,
		Role: "member", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	assert.NoError(t, err)
}

// The defect this migration closes: an agent from a DIFFERENT workspace must be
// rejected by the schema, through the repo's own Create path — not just by raw
// SQL (see the migration's own psql negative controls) and not just by the
// application-level funnel this test deliberately bypasses.
func TestProjectMemberCreate_ForeignAgent_RejectedBySchema(t *testing.T) {
	db := pmFKTestDB(t)
	fx := newPMFKFixture(t, db)
	ctx := context.Background()

	err := NewProjectMemberRepo(db).Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: fx.nativeProjectID, AgentID: &fx.foreignAgentID,
		Role: "member", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	require.Error(t, err, "check_pm_agent_workspace must reject a foreign agent with no home match and no grant")
	assert.Contains(t, err.Error(), "does not belong to workspace")
}

// The guest half of the SAME invariant: an agent whose HOME is elsewhere but
// who holds an ACTIVE grant into the native workspace (task U1/U3,
// agent_workspace_grants) must be let through by the trigger — this is the
// exact case fk_pm_agent_workspace used to reject unconditionally, which is
// why migration 20260911001 exists.
func TestProjectMemberCreate_GuestAgentWithActiveGrant_Succeeds(t *testing.T) {
	db := pmFKTestDB(t)
	fx := newPMFKFixture(t, db)
	ctx := context.Background()

	grant := &domain.AgentWorkspaceGrant{
		ID: uuid.New(), AgentID: fx.foreignAgentID, WorkspaceID: fx.nativeWorkspaceID,
		Role: "member", APIKeyPrefix: "g-" + uuid.New().String()[:8], APIKeyHash: "$2a$12$grant",
	}
	require.NoError(t, NewAgentWorkspaceGrantRepo(db).Create(ctx, grant))

	err := NewProjectMemberRepo(db).Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: fx.nativeProjectID, AgentID: &fx.foreignAgentID,
		Role: "member", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	require.NoError(t, err, "an agent with an ACTIVE grant into the project's workspace must be allowed, even though its home workspace is elsewhere")
}

// Same setup, but the grant is REVOKED — the trigger's revoked_at IS NULL
// clause is exactly what a plain FK could never express, so this is the one
// case that most needs its own regression test: a trigger that forgot that
// clause would pass TestProjectMemberCreate_GuestAgentWithActiveGrant_Succeeds
// too, silently.
func TestProjectMemberCreate_GuestAgentWithRevokedGrant_RejectedBySchema(t *testing.T) {
	db := pmFKTestDB(t)
	fx := newPMFKFixture(t, db)
	ctx := context.Background()

	grant := &domain.AgentWorkspaceGrant{
		ID: uuid.New(), AgentID: fx.foreignAgentID, WorkspaceID: fx.nativeWorkspaceID,
		Role: "member", APIKeyPrefix: "r-" + uuid.New().String()[:8], APIKeyHash: "$2a$12$revoked",
	}
	require.NoError(t, NewAgentWorkspaceGrantRepo(db).Create(ctx, grant))
	found, err := NewAgentWorkspaceGrantRepo(db).Revoke(ctx, grant.ID, fx.nativeWorkspaceID)
	require.NoError(t, err)
	require.True(t, found)

	err = NewProjectMemberRepo(db).Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: fx.nativeProjectID, AgentID: &fx.foreignAgentID,
		Role: "member", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	require.Error(t, err, "a REVOKED grant must not satisfy the trigger — revoke has to actually mean something")
	assert.Contains(t, err.Error(), "does not belong to workspace")
}

// Same defect, the user path — this is the shape of the one real violation the
// 08.08 audit found in prod (task a0cf0c42 §5), not a hypothetical.
func TestProjectMemberCreate_ForeignUser_RejectedBySchema(t *testing.T) {
	db := pmFKTestDB(t)
	fx := newPMFKFixture(t, db)
	ctx := context.Background()

	err := NewProjectMemberRepo(db).Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: fx.nativeProjectID, UserID: &fx.foreignUserID,
		Role: "member", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	require.Error(t, err, "fk_pm_user_workspace_member must reject a foreign user")
	assert.Contains(t, err.Error(), "fk_pm_user_workspace_member")
}

// A project_id that does not exist must be reported as an error, not silently
// insert zero rows — Create's subquery-based INSERT has no FK violation to fall
// back on for this case, so RowsAffected has to be checked explicitly.
func TestProjectMemberCreate_UnknownProject_Errors(t *testing.T) {
	db := pmFKTestDB(t)
	fx := newPMFKFixture(t, db)
	ctx := context.Background()

	err := NewProjectMemberRepo(db).Create(ctx, &domain.ProjectMember{
		ID: uuid.New(), ProjectID: uuid.New(), AgentID: &fx.nativeAgentID,
		Role: "member", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
