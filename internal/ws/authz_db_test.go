package ws

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func wsAuthzDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres, skipping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestDBAuthorizer_MayAccessProject runs the production Authorizer against real
// tables: the socket must answer exactly as RequireProjectMember does, including
// the owner whose workspace_members row was never written.
func TestDBAuthorizer_MayAccessProject(t *testing.T) {
	db := wsAuthzDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	users := postgres.NewUserRepo(db)
	newUser := func(tag string) uuid.UUID {
		u := &domain.User{ID: uuid.New(), Email: tag + "-" + suffix + "@example.com", PasswordHash: "x", Name: tag, Username: tag + "-" + suffix, IsActive: true}
		require.NoError(t, users.Create(ctx, u))
		return u.ID
	}
	owner, admin, member, outsider := newUser("wso"), newUser("wsa"), newUser("wsm"), newUser("wsx")

	wsRepo := postgres.NewWorkspaceRepo(db)
	ws := &domain.Workspace{ID: uuid.New(), Name: "ws-authz", Slug: "ws-authz-" + suffix, OwnerID: owner}
	require.NoError(t, wsRepo.Create(ctx, ws))
	other := &domain.Workspace{ID: uuid.New(), Name: "ws-authz-other", Slug: "ws-authz-o-" + suffix, OwnerID: owner}
	require.NoError(t, wsRepo.Create(ctx, other))

	// The owner keeps no workspace_members row: the fallback must still apply.
	_, err := db.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, ws.ID, owner)
	require.NoError(t, err)
	for u, role := range map[uuid.UUID]string{admin: "admin", member: "member", outsider: "member"} {
		_, insErr := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1, $2, $3, $4)`, uuid.New(), ws.ID, u, role)
		require.NoError(t, insErr)
	}

	newProject := func(wsID uuid.UUID, tag string) uuid.UUID {
		id := uuid.New()
		_, insErr := db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1, $2, $3, $4)`, id, wsID, tag, tag+"-"+suffix)
		require.NoError(t, insErr)
		return id
	}
	project := newProject(ws.ID, "wsp")
	foreign := newProject(other.ID, "wsf")

	newAgent := func(tag string) uuid.UUID {
		id := uuid.New()
		_, insErr := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1, $2, $3, $4, 'h', 'p')`, id, ws.ID, tag, tag+"-"+suffix)
		require.NoError(t, insErr)
		return id
	}
	adminAgent, memberAgent := newAgent("wsaa"), newAgent("wsma")
	// The admin agent connects through an admin-role workspace grant; the rule
	// must still give it only the projects it is a member of.
	_, err = db.Exec(`INSERT INTO agent_workspace_grants (id, agent_id, workspace_id, role, api_key_prefix, api_key_hash, created_at) VALUES ($1, $2, $3, 'admin', $4, $5, NOW())`,
		uuid.New(), adminAgent, ws.ID, "g"+suffix, "synthetic-"+suffix)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO project_members (id, project_id, user_id, role, workspace_id) VALUES ($1, $2, $3, 'member', $4)`, uuid.New(), project, member, ws.ID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO project_members (id, project_id, agent_id, role, workspace_id) VALUES ($1, $2, $3, 'member', $4)`, uuid.New(), project, memberAgent, ws.ID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1, $2, $3, 'member')`, uuid.New(), other.ID, member)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO project_members (id, project_id, user_id, role, workspace_id) VALUES ($1, $2, $3, 'member', $4)`, uuid.New(), foreign, member, other.ID)
	require.NoError(t, err)

	authz := NewDBAuthorizer(db)
	user := func(id uuid.UUID) Principal { return Principal{UserID: id, WorkspaceID: ws.ID, WorkspaceSlug: ws.Slug} }
	agent := func(id uuid.UUID) Principal {
		return Principal{AgentID: id, WorkspaceID: ws.ID, WorkspaceSlug: ws.Slug}
	}

	cases := []struct {
		name    string
		p       Principal
		project uuid.UUID
		want    bool
	}{
		{"workspace member outside the project", user(outsider), project, false},
		{"project member", user(member), project, true},
		{"owner without a membership row", user(owner), project, true},
		{"workspace admin", user(admin), project, true},
		{"agent with an admin role, not a member", agent(adminAgent), project, false},
		{"agent project member", agent(memberAgent), project, true},
		{"project of another workspace, even as its member", user(member), foreign, false},
		{"project of another workspace, as owner", user(owner), foreign, false},
		{"unknown project", user(owner), uuid.New(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, authz.MayAccessProject(ctx, tc.p, tc.project))
		})
	}
}
