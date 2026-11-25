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
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// This contract also compiles unchanged on main: the assertion reads the JSON
// response rather than relying on any newly introduced production type.
func TestMemoryRecallSourceContractDB(t *testing.T) {
	db := reservedTagTestDB(t)
	ws := newReservedTagTestWorkspace(t, db, false)
	agent := newReservedTagTestAgent(t, db, ws)
	repo := postgres.NewMemoryRepo(db)
	memory := &domain.Memory{ID: uuid.New(), WorkspaceID: ws, Key: "read-source-control", Content: "copperjournal source contract", Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent, Relevance: 1, ImportanceScore: 1, Tags: []string{}}
	require.NoError(t, repo.Upsert(context.Background(), memory, domain.MemoryWriteIntent{}))
	e := echo.New()
	// mock: external boundary — authenticated identity, not the memory service.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { asAgent(c, agent, ws); return next(c) }
	})
	e.GET("/api/v1/memories/search", NewMemoryHandler(service.NewMemoryService(repo, postgres.NewMemoryEdgesRepo(db), nil), postgres.NewWorkspaceMemberRepo(db)).Search)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/memories/search?q=copperjournal", http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Items []struct {
			ID     uuid.UUID `json:"id"`
			Source string    `json:"source"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Items, 1, "positive control: the stored memory is served")
	require.Equal(t, memory.ID, body.Items[0].ID)
	require.Equal(t, "memory", body.Items[0].Source, "served recall rows identify their source")
}
