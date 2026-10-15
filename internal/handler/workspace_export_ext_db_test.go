package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// The whole chain an extension takes to the export: ext_ token -> ExtensionAuth ->
// ExtensionCapabilityGuard -> handler -> real Postgres. DB-backed, skipped when
// Postgres is unreachable (same convention as the other *_db_test.go files).

func TestWorkspaceExport_ExtensionActor_DB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres, skipping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	now := time.Now()

	owner := &domain.User{ID: uuid.New(), Email: "exp-" + suffix + "@example.com", PasswordHash: "x", Name: "Owner", Username: "exp-" + suffix, IsActive: true}
	second := &domain.User{ID: uuid.New(), Email: "exp2-" + suffix + "@example.com", PasswordHash: "x", Name: "Second", Username: "exp2-" + suffix, IsActive: true}
	users := postgres.NewUserRepo(db)
	require.NoError(t, users.Create(ctx, owner))
	require.NoError(t, users.Create(ctx, second))
	ws := &domain.Workspace{ID: uuid.New(), Name: "exp-ws", Slug: "exp-ws-" + suffix, OwnerID: owner.ID}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	members := postgres.NewWorkspaceMemberRepo(db)
	for _, m := range []struct {
		u    uuid.UUID
		role string
	}{{owner.ID, domain.RoleOwner}, {second.ID, domain.RoleMember}} {
		require.NoError(t, members.Create(ctx, &domain.WorkspaceMember{ID: uuid.New(), WorkspaceID: ws.ID, UserID: m.u, Role: m.role, CreatedAt: now, UpdatedAt: now}))
	}
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, &domain.Project{ID: uuid.New(), WorkspaceID: ws.ID, Name: "P1", Slug: "p1-" + suffix,
		Settings: []byte(`{}`), DefaultAssigneeType: domain.DefaultAssigneeUser}))
	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: ws.ID, Name: "Bot", Slug: "bot-" + suffix, AgentType: domain.AgentTypeClaudeCode,
		Status: domain.AgentStatusOffline, APIKeyHash: "$2a$12$" + suffix, APIKeyPrefix: suffix, APIKeySHA256: "d-" + uuid.NewString()}
	require.NoError(t, postgres.NewAgentRepo(db).Create(ctx, agent))
	exportRepo := postgres.NewWorkspaceExportRepo(db)
	require.NoError(t, exportRepo.SetAgentSponsor(ctx, agent.ID, owner.ID))

	integ := postgres.NewIntegrationRepo(db)
	actors := service.NewExtensionActorService(postgres.NewExtensionActorRepo(db), integ, postgres.NewWorkspaceRepo(db))
	token := func(provider domain.IntegrationProvider, consented []string) string {
		conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws.ID, Provider: provider, IsActive: true, CreatedAt: now, UpdatedAt: now}
		require.NoError(t, integ.Upsert(ctx, conn))
		issued, err := actors.Issue(ctx, ws.ID, conn.ID, string(provider), "Keep", consented)
		require.NoError(t, err)
		return issued.Token
	}

	e := echo.New()
	g := e.Group("/api/v1")
	g.Use(middleware.ExtensionAuth(actors))
	g.Use(middleware.ExtensionCapabilityGuard(actors))
	g.GET("/workspaces/:ws_id/export/hierarchy", NewWorkspaceExportHandler(exportRepo).Hierarchy)
	get := func(tok, inm string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/"+ws.ID.String()+"/export/hierarchy", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+tok)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	full := token(domain.IntegrationProviderGitLab, []string{"api:workspaces:read", "api:projects:read", "api:members:read", "api:agents:read"})
	rec := get(full, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Counts map[string]int `json:"counts"`
		Agents []struct {
			SponsorUserID string `json:"sponsor_user_id"`
		} `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, map[string]int{"projects": 1, "members": 2, "agents": 1}, out.Counts)
	require.Len(t, out.Agents, 1)
	assert.Equal(t, owner.ID.String(), out.Agents[0].SponsorUserID)
	etag := rec.Header().Get("ETag")
	assert.Equal(t, http.StatusNotModified, get(full, etag).Code)

	// Red control: an extension without members:read is refused by the guard
	// chain (403) and gets no data.
	noMembers := token(domain.IntegrationProviderGitHub, []string{"api:workspaces:read", "api:projects:read", "api:agents:read"})
	rec = get(noMembers, "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.NotContains(t, rec.Body.String(), owner.Email)

	// Red control: a member removed behind the extension's back shows up in the
	// next export (new cursor, one member fewer).
	require.NoError(t, members.Delete(ctx, ws.ID, second.ID))
	rec = get(full, etag)
	require.Equal(t, http.StatusOK, rec.Code, "the old cursor no longer matches")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, 1, out.Counts["members"])
	assert.NotEqual(t, etag, rec.Header().Get("ETag"))
}
