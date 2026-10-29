package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// A person's responsibility_zone, capabilities and availability live in
// workspace_members, but the team directory answered constants for every
// human ("", [], "business-hours") and nothing wrote the columns. These tests
// go through the real repository and the real GET /workspaces/:ws_id/team.
func TestTeamDirectory_HumanProfileFromWorkspaceMembers(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL or DATABASE_URL required for real PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	userID, wsID := uuid.New(), uuid.New()
	handle := "teamprof-" + userID.String()[:8]
	name := "Team Profile " + userID.String()[:8]
	_, err = db.Exec(`INSERT INTO users (id, email, password_hash, display_name, username, is_active)
		VALUES ($1, $2, 'not-a-real-hash', $3, $4, true)`, userID, handle+"@example.test", name, handle)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, userID) })
	_, err = db.Exec(`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1, 'Team Profile', $2, $3)`,
		wsID, "teamprof-ws-"+wsID.String()[:8], userID)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, wsID) })
	_, err = db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at)
		VALUES ($1, $2, $3, 'owner', now(), now())`, uuid.New(), wsID, userID)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1`, wsID) })

	memberRepo := postgres.NewWorkspaceMemberRepo(db)
	rulesSvc := service.NewRulesService(nil, nil, nil, postgres.NewAgentRepo(db), memberRepo, postgres.NewWorkspaceRepo(db), nil)
	h := NewRulesHandler(rulesSvc)

	getHuman := func(t *testing.T, format string) domain.TeamDirectoryHuman {
		t.Helper()
		e := echo.New()
		target := "/workspaces/" + wsID.String() + "/team"
		if format != "" {
			target += "?format=" + format
		}
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, target, http.NoBody), rec)
		c.SetParamNames("ws_id")
		c.SetParamValues(wsID.String())
		require.NoError(t, h.GetTeamDirectory(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body struct {
			Humans []domain.TeamDirectoryHuman `json:"humans"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Len(t, body.Humans, 1)
		return body.Humans[0]
	}

	t.Run("stored columns reach both formats", func(t *testing.T) {
		_, err := db.Exec(`UPDATE workspace_members
			SET responsibility_zone = 'X', capabilities = '["go","sql"]', availability = 'async'
			WHERE workspace_id = $1 AND user_id = $2`, wsID, userID)
		require.NoError(t, err)

		for _, format := range []string{"tree", ""} {
			got := getHuman(t, format)
			assert.Equal(t, "X", got.ResponsibilityZone, "format=%q", format)
			assert.JSONEq(t, `["go","sql"]`, string(got.Capabilities), "format=%q", format)
			assert.Equal(t, "async", got.Availability, "format=%q", format)
			assert.Equal(t, handle, got.Username, "format=%q", format)
		}
	})

	t.Run("UpdateProfile is partial", func(t *testing.T) {
		avail := "business-hours"
		found, err := memberRepo.UpdateProfile(context.Background(), wsID, userID, domain.MemberProfileUpdate{Availability: &avail})
		require.NoError(t, err)
		require.True(t, found)

		got := getHuman(t, "tree")
		assert.Equal(t, "business-hours", got.Availability)
		assert.Equal(t, "X", got.ResponsibilityZone, "a field left out must keep its value")
		assert.JSONEq(t, `["go","sql"]`, string(got.Capabilities))

		found, err = memberRepo.UpdateProfile(context.Background(), uuid.New(), userID, domain.MemberProfileUpdate{Availability: &avail})
		require.NoError(t, err)
		assert.False(t, found, "no membership in that workspace")
	})

	t.Run("config import writes the profile", func(t *testing.T) {
		yamlData := []byte("humans:\n  - name: " + name + "\n    responsibility_zone: payments\n    capabilities: [billing]\n    availability: on-call\n")
		result, err := rulesSvc.ImportTeam(context.Background(), wsID, yamlData)
		require.NoError(t, err)
		assert.Empty(t, result.Errors)
		assert.Equal(t, 1, result.HumansUpdated)

		got := getHuman(t, "tree")
		assert.Equal(t, "payments", got.ResponsibilityZone)
		assert.JSONEq(t, `["billing"]`, string(got.Capabilities))
		assert.Equal(t, "on-call", got.Availability)
	})
}
