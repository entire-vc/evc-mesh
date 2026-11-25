package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// TestProjectKnowledgeDB_SupersededHiddenByDefault drives GET
// /api/v1/projects/:proj_id/knowledge through the real handler, memory
// service, repository SQL and read journal. By default the response carries
// no row whose status is 'superseded' (and the journal records
// superseded_count=0, the production metric); an explicit
// include_superseded=true restores the former output with both rows.
func TestProjectKnowledgeDB_SupersededHiddenByDefault(t *testing.T) {
	db := reservedTagTestDB(t)
	ws := newReservedTagTestWorkspace(t, db, false)
	agent := newReservedTagTestAgent(t, db, ws)
	ctx := context.Background()
	repo := postgres.NewMemoryRepo(db)
	project := &domain.Project{ID: uuid.New(), WorkspaceID: ws, Name: "knowledge", Slug: "knowledge-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, project))

	ids := map[string]uuid.UUID{}
	for _, tc := range []struct {
		key    string
		scope  domain.MemoryScope
		status domain.MemoryStatus
	}{
		{"pk-project-active", domain.ScopeProject, domain.MemoryStatusActive},
		{"pk-project-old", domain.ScopeProject, domain.MemoryStatusSuperseded},
		{"pk-workspace-active", domain.ScopeWorkspace, domain.MemoryStatusActive},
		{"pk-workspace-old", domain.ScopeWorkspace, domain.MemoryStatusSuperseded},
	} {
		m := &domain.Memory{ID: uuid.New(), WorkspaceID: ws, Key: tc.key, Content: "knowledge control " + tc.key, Scope: tc.scope, SourceType: domain.SourceAgent, Relevance: 1, ImportanceScore: 1, Tags: []string{}}
		if tc.scope == domain.ScopeProject {
			m.ProjectID = &project.ID
		}
		require.NoError(t, repo.Upsert(ctx, m, domain.MemoryWriteIntent{}))
		_, err := db.Exec(`UPDATE memories SET status=$1 WHERE id=$2`, tc.status, m.ID)
		require.NoError(t, err)
		ids[tc.key] = m.ID
	}
	var stored int
	require.NoError(t, db.Get(&stored, `SELECT count(*) FROM memories WHERE workspace_id=$1 AND status='superseded'`, ws))
	require.Equal(t, 2, stored, "fixture: both superseded rows are stored")

	journal := mw.NewMemoryReadLog(postgres.NewMemoryReadEventRepo(db), true)
	h := NewMemoryHandler(service.NewMemoryService(repo, postgres.NewMemoryEdgesRepo(db), nil), postgres.NewWorkspaceMemberRepo(db))
	e := echo.New()
	// mock: external boundary — identity normally supplied by the auth provider.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { asAgent(c, agent, ws); return next(c) }
	})
	e.Use(journal.Middleware())
	e.GET("/api/v1/projects/:proj_id/knowledge", h.GetProjectKnowledge)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM memory_read_events WHERE workspace_id=$1`, ws) })

	type knowledgeRow struct {
		ID     uuid.UUID `json:"id"`
		Key    string    `json:"key"`
		Status string    `json:"status"`
	}
	get := func(t *testing.T, query string) (projRows, wsRows []knowledgeRow, superseded int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+project.ID.String()+"/knowledge"+query, http.NoBody)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body struct {
			Project   []knowledgeRow `json:"project_memories"`
			Workspace []knowledgeRow `json:"workspace_memories"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		journal.Flush(ctx)
		require.NoError(t, db.Get(&superseded, `SELECT superseded_count FROM memory_read_events WHERE workspace_id=$1 AND op='get_project_knowledge' ORDER BY ts DESC LIMIT 1`, ws))
		return body.Project, body.Workspace, superseded
	}
	keys := func(rows []knowledgeRow) []string {
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.Key)
		}
		return out
	}

	t.Run("default hides superseded", func(t *testing.T) {
		proj, wsRows, superseded := get(t, "")
		require.ElementsMatch(t, []string{"pk-project-active"}, keys(proj))
		require.ElementsMatch(t, []string{"pk-workspace-active"}, keys(wsRows))
		for _, r := range append(proj, wsRows...) {
			require.NotEqual(t, string(domain.MemoryStatusSuperseded), r.Status)
		}
		require.Zero(t, superseded, "journal records no superseded row served")
	})

	t.Run("include_superseded=false hides superseded", func(t *testing.T) {
		proj, wsRows, superseded := get(t, "?include_superseded=false")
		require.ElementsMatch(t, []string{"pk-project-active"}, keys(proj))
		require.ElementsMatch(t, []string{"pk-workspace-active"}, keys(wsRows))
		require.Zero(t, superseded)
	})

	t.Run("include_superseded=true returns both", func(t *testing.T) {
		proj, wsRows, superseded := get(t, "?include_superseded=true")
		require.ElementsMatch(t, []string{"pk-project-active", "pk-project-old"}, keys(proj))
		require.ElementsMatch(t, []string{"pk-workspace-active", "pk-workspace-old"}, keys(wsRows))
		require.Equal(t, 2, superseded, "positive control: the journal does count superseded rows when they are served")
		for _, r := range proj {
			if r.Key == "pk-project-old" {
				require.Equal(t, ids["pk-project-old"], r.ID)
				require.Equal(t, string(domain.MemoryStatusSuperseded), r.Status)
			}
		}
	})

	t.Run("invalid include_superseded is rejected", func(t *testing.T) {
		for _, query := range []string{"?include_superseded=maybe", "?include_superseded=", "?include_superseded"} {
			t.Run(query, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+project.ID.String()+"/knowledge"+query, http.NoBody)
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			})
		}
	})
}
