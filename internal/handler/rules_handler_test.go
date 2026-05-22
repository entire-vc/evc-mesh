package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// TestTeam_FiltersProjectsByWorkspace verifies that the team directory endpoint
// passes the workspace_id from the route to the service — ensuring only projects
// belonging to that workspace are returned and there is no cross-workspace leakage.
func TestTeam_FiltersProjectsByWorkspace(t *testing.T) {
	wsA := uuid.New()
	wsB := uuid.New()

	// Simulate a user who is a member of both wsA and wsB with different projects.
	// The service should only return projects scoped to the requested workspace.
	directoryForA := &domain.TeamDirectory{
		Workspace: "workspace-a",
		Humans: []domain.TeamDirectoryHuman{
			{
				ID:       uuid.New(),
				Name:     "Alice",
				Email:    "alice@example.com",
				Projects: []string{"Project A1", "Project A2"},
			},
		},
	}
	directoryForB := &domain.TeamDirectory{
		Workspace: "workspace-b",
		Humans: []domain.TeamDirectoryHuman{
			{
				ID:       uuid.New(),
				Name:     "Alice",
				Email:    "alice@example.com",
				Projects: []string{"Project B1"},
			},
		},
	}

	var capturedWorkspaceID uuid.UUID
	mockSvc := &MockRulesService{
		GetTeamDirectoryFunc: func(ctx context.Context, workspaceID uuid.UUID) (*domain.TeamDirectory, error) {
			capturedWorkspaceID = workspaceID
			if workspaceID == wsA {
				return directoryForA, nil
			}
			return directoryForB, nil
		},
	}

	e := echo.New()
	h := NewRulesHandler(mockSvc)

	// --- Request for workspace A ---
	reqA := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	recA := httptest.NewRecorder()
	cA := e.NewContext(reqA, recA)
	cA.SetParamNames("ws_id")
	cA.SetParamValues(wsA.String())

	err := h.GetTeamDirectory(cA)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, recA.Code)
	assert.Equal(t, wsA, capturedWorkspaceID, "service must be called with workspace A id")

	var resultA domain.TeamDirectory
	require.NoError(t, json.Unmarshal(recA.Body.Bytes(), &resultA))
	require.Len(t, resultA.Humans, 1)
	assert.Equal(t, []string{"Project A1", "Project A2"}, resultA.Humans[0].Projects,
		"workspace A must only show its own projects")

	// --- Request for workspace B ---
	reqB := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	recB := httptest.NewRecorder()
	cB := e.NewContext(reqB, recB)
	cB.SetParamNames("ws_id")
	cB.SetParamValues(wsB.String())

	err = h.GetTeamDirectory(cB)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, recB.Code)
	assert.Equal(t, wsB, capturedWorkspaceID, "service must be called with workspace B id")

	var resultB domain.TeamDirectory
	require.NoError(t, json.Unmarshal(recB.Body.Bytes(), &resultB))
	require.Len(t, resultB.Humans, 1)
	assert.Equal(t, []string{"Project B1"}, resultB.Humans[0].Projects,
		"workspace B must only show its own projects, not workspace A projects")

	// Verify the two results are different (cross-workspace leakage guard)
	assert.NotEqual(t, resultA.Humans[0].Projects, resultB.Humans[0].Projects,
		"projects must not leak across workspaces")
}
