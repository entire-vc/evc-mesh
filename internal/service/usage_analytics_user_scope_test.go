package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// userScopeQ is the /me/analytics/usage query: no workspace, every workspace of the viewer.
func userScopeQ(v AnalyticsViewer, metric, groupBy string) UsageQuery {
	return UsageQuery{UserScope: true, Viewer: v, Metric: metric, GroupBy: groupBy, From: usageFrom, To: usageTo,
		Step: UsageStepDay, Loc: time.UTC, Top: 8}
}

func insertDoneCards(t *testing.T, db *sqlx.DB, project, agent uuid.UUID, n int) {
	t.Helper()
	done := uuid.New()
	_, err := db.Exec(`INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default, auto_transition)
		VALUES ($1,$2,'Done','done','#0000ff',1,'done',false,'{}')`, done, project)
	require.NoError(t, err)
	completed := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		_, err = db.Exec(`INSERT INTO tasks (id, project_id, status_id, title, description, assignee_type, assignee_id, priority, position, custom_fields, labels, task_number, created_by, created_by_type, created_at, updated_at, completed_at)
			VALUES ($1,$2,$3,'c','','agent',$4,'medium',0,'{}','{}',$5,$6,'user',$7,$7,$7)`,
			uuid.New(), project, done, agent, i+1, uuid.New(), completed)
		require.NoError(t, err)
	}
}

// Every fixture workspace holds 12 turns in the period, 10 of them in Shared.
func TestUsage_UserScopeAcrossWorkspaces(t *testing.T) {
	db := testDB(t)
	f := seedUsageFixture(t, db, true)  // f.owner owns it, runner on
	g := seedUsageFixture(t, db, false) // f.owner is admin here, no runner
	h := seedUsageFixture(t, db, true)  // foreign to f.owner
	_, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,'admin')`, uuid.New(), g.ws, f.owner)
	require.NoError(t, err)
	svc := &analyticsService{db: db}
	ctx := context.Background()
	owner := AnalyticsViewer{ActorID: f.owner}

	t.Run("workspace cut covers only the viewer's workspaces and totals equal the series", func(t *testing.T) {
		res, err := svc.GetUsage(ctx, userScopeQ(owner, UsageMetricTurns, UsageDimWorkspace))
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{f.ws.String(), g.ws.String()}, rowKeys(res))
		assert.Equal(t, 24.0, *res.Totals.Value)
		assert.Equal(t, 24.0, seriesSum(res))
		assert.Equal(t, "Usage WS", rowByKey(res, g.ws.String()).Label)
		assert.Equal(t, &UsageLink{Kind: UsageDimWorkspace, ID: f.ws.String()}, rowByKey(res, f.ws.String()).Link)
		assert.Contains(t, res.Capabilities.GroupBy, UsageDimWorkspace)
		assert.Contains(t, res.Capabilities.CardsDoneGroupBy, UsageDimWorkspace)
		assert.Contains(t, res.Capabilities.GroupBy, UsageDimAccount)
		assert.False(t, res.Capabilities.Quota, "quota stays a workspace capability")
	})

	t.Run("a member sees only projects it may open", func(t *testing.T) {
		member := AnalyticsViewer{ActorID: f.member, AllProjects: true} // a workspace role never widens /me
		res, err := svc.GetUsage(ctx, userScopeQ(member, UsageMetricTurns, UsageDimProject))
		require.NoError(t, err)
		assert.Equal(t, []string{f.shared.String()}, rowKeys(res))
		assert.Equal(t, "Shared usage", res.Rows[0].Label)
		assert.Equal(t, 10.0, *res.Totals.Value)
		assert.NotContains(t, res.Capabilities.GroupBy, UsageDimAccount)

		_, err = svc.GetUsage(ctx, userScopeQ(member, UsageMetricTurns, UsageDimAccount))
		requireUsageStatus(t, err, http.StatusForbidden)

		q := userScopeQ(member, UsageMetricTurns, UsageDimWorkspace)
		q.ProjectIDs = []uuid.UUID{f.shared}
		res, err = svc.GetUsage(ctx, q)
		require.NoError(t, err)
		assert.Equal(t, 10.0, *res.Totals.Value)
		q.ProjectIDs = []uuid.UUID{f.hidden}
		_, err = svc.GetUsage(ctx, q)
		requireUsageStatus(t, err, http.StatusForbidden)
	})

	t.Run("account cut needs owner/admin and an active runner in the same workspace", func(t *testing.T) {
		res, err := svc.GetUsage(ctx, userScopeQ(owner, UsageMetricTurns, UsageDimAccount))
		require.NoError(t, err)
		assert.Equal(t, 12.0, *res.Totals.Value, "the admin workspace without the runner is excluded")
		assert.ElementsMatch(t, []string{"claude/bob-team", "codex/alex", ""}, rowKeys(res))

		gAdmin := AnalyticsViewer{ActorID: g.owner}
		_, err = svc.GetUsage(ctx, userScopeQ(gAdmin, UsageMetricTurns, UsageDimAccount))
		requireUsageStatus(t, err, http.StatusForbidden)
	})

	t.Run("workspace filter", func(t *testing.T) {
		q := userScopeQ(owner, UsageMetricTurns, UsageDimWorkspace)
		q.WorkspaceIDs = []uuid.UUID{g.ws}
		res, err := svc.GetUsage(ctx, q)
		require.NoError(t, err)
		assert.Equal(t, []string{g.ws.String()}, rowKeys(res))
		assert.Equal(t, 12.0, *res.Totals.Value)

		q.WorkspaceIDs = []uuid.UUID{g.ws, h.ws}
		_, err = svc.GetUsage(ctx, q)
		requireUsageStatus(t, err, http.StatusForbidden)
	})

	t.Run("cards done by workspace", func(t *testing.T) {
		insertDoneCards(t, db, f.shared, f.agentA, 2)
		insertDoneCards(t, db, g.shared, g.agentA, 1)
		insertDoneCards(t, db, h.shared, h.agentA, 4)
		res, err := svc.GetUsage(ctx, userScopeQ(owner, UsageMetricCardsDone, UsageDimWorkspace))
		require.NoError(t, err)
		assert.Equal(t, 3.0, *res.Totals.Value)
		assert.Equal(t, 2.0, *rowByKey(res, f.ws.String()).Value)
	})

	t.Run("an agent key reads its operator's workspaces", func(t *testing.T) {
		_, err := db.Exec(`UPDATE agents SET operator_user_id=$1 WHERE id=$2`, f.owner, h.agentA)
		require.NoError(t, err)
		res, err := svc.GetUsage(ctx, userScopeQ(AnalyticsViewer{ActorID: h.agentA, Agent: true}, UsageMetricTurns, UsageDimWorkspace))
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{f.ws.String(), g.ws.String()}, rowKeys(res))
		assert.Equal(t, 24.0, *res.Totals.Value)

		for name, id := range map[string]uuid.UUID{"no operator": h.agentB, "unknown agent": uuid.New()} {
			t.Run(name, func(t *testing.T) {
				_, err := svc.GetUsage(ctx, userScopeQ(AnalyticsViewer{ActorID: id, Agent: true}, UsageMetricTurns, UsageDimWorkspace))
				requireUsageStatus(t, err, http.StatusForbidden)
			})
		}
	})

	t.Run("the workspace cut exists only at /me", func(t *testing.T) {
		_, err := svc.GetUsage(ctx, usageQ(f, admin(), UsageMetricTurns, UsageDimWorkspace))
		requireUsageStatus(t, err, http.StatusBadRequest)
		q := usageQ(f, admin(), UsageMetricTurns, UsageDimModel)
		q.WorkspaceIDs = []uuid.UUID{f.ws}
		_, err = svc.GetUsage(ctx, q)
		requireUsageStatus(t, err, http.StatusBadRequest)
	})
}
