package handler

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// Real handler -> service -> PostgreSQL, including CSV. Private project names,
// totals and pricing metadata must all follow the same visibility predicate.
func TestUsageHandler_ProjectVisibilityJSONAndCSV(t *testing.T) {
	db := reportDB(t)
	ws, member, agent := uuid.New(), uuid.New(), uuid.New()
	shared, hidden := uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := db.Exec(sql, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO workspaces (id,name,slug,owner_id) VALUES ($1,'Usage',$2,$3)`, ws, ws.String(), uuid.New())
	exec(`INSERT INTO users (id,email,password_hash,display_name,username,is_active) VALUES ($1,$2,'x','Member',$3,true)`, member, member.String()+"@usage.test", member.String())
	exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES ($1,$2,$3,'member')`, uuid.New(), ws, member)
	exec(`INSERT INTO agents (id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES ($1,$2,'Agent',$3,'hash','test')`, agent, ws, agent.String())
	for _, p := range []uuid.UUID{shared, hidden} {
		exec(`INSERT INTO projects (id,workspace_id,name,slug) VALUES ($1,$2,$3,$3)`, p, ws, p.String())
	}
	exec(`INSERT INTO project_members (id,project_id,workspace_id,user_id,role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), shared, ws, member)
	exec(`INSERT INTO integration_configs (workspace_id,provider,is_active) VALUES ($1,'runner',true)`, ws)
	for i, p := range []uuid.UUID{shared, hidden, uuid.Nil} {
		exec(`INSERT INTO agent_usage_hourly (workspace_id,hour,agent_id,project_id,model,provider,account_ref,billing_mode,turns,turns_tokens_known,tokens_in,tokens_out)
		VALUES ($1,'2026-10-02T10:00:00Z',$2,$3,$4,'test','','',$5,$5,$6,0)`, ws, agent, p, []string{"public-model", "private-model", "orphan-model"}[i], i+1, (i+1)*100)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM agent_usage_hourly WHERE workspace_id=$1`, ws)
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws)
		_, _ = db.Exec(`DELETE FROM users WHERE id=$1`, member)
	})
	h := NewAnalyticsHandler(service.NewAnalyticsService(db))
	get := func(role, query string) *httptest.ResponseRecorder {
		t.Helper()
		e := echo.New()
		e.GET("/workspaces/:ws_id/analytics/usage", h.GetUsage, func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				asWorkspaceUser(c, member, role)
				return next(c)
			}
		})
		rec := httptest.NewRecorder()
		params, err := url.ParseQuery("from=2026-10-01&to=2026-10-03&group_by=model")
		require.NoError(t, err)
		extra, err := url.ParseQuery(query)
		require.NoError(t, err)
		for k, values := range extra {
			params[k] = values
		}
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workspaces/"+ws.String()+"/analytics/usage?"+params.Encode(), http.NoBody))
		return rec
	}
	for _, format := range []string{"json", "csv"} {
		// Positive control: admin gets both the private project and orphan turn.
		control := get(domain.RoleAdmin, "metric=tokens&format="+format)
		require.Equal(t, http.StatusOK, control.Code, control.Body.String())
		require.Contains(t, control.Body.String(), "private-model")
		require.Contains(t, control.Body.String(), "orphan-model")
		rec := get(domain.RoleMember, "metric=tokens&format="+format)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotContains(t, rec.Body.String(), "private-model")
		require.NotContains(t, rec.Body.String(), "orphan-model")
		if format == "json" {
			var result service.UsageResult
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
			require.Equal(t, 100.0, *result.Totals.Value)
			require.EqualValues(t, 1, result.Totals.Turns)
			require.NotContains(t, result.Capabilities.GroupBy, "account")
			require.Equal(t, []string{"public-model"}, result.Pricing.NoPriceModels)
		} else {
			rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
			require.NoError(t, err)
			require.Len(t, rows, 3) // header, public model, total
			require.Equal(t, "100", rows[2][4])
			require.Empty(t, rows[1][13], "unknown cost stays empty")
		}
		for _, query := range []string{"group_by=account", "stack_by=account", "account[]=private-pool"} {
			control := get(domain.RoleAdmin, "format="+format+"&"+query)
			require.Equal(t, http.StatusOK, control.Code, control.Body.String())
			denied := get(domain.RoleMember, "format="+format+"&"+query)
			require.Equal(t, http.StatusForbidden, denied.Code, denied.Body.String())
		}
		rec = get(domain.RoleMember, "project_id[]="+hidden.String()+"&format="+format)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		rec = get(domain.RoleMember, "metric=cost&format="+format)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		if format == "json" {
			var result service.UsageResult
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
			require.Nil(t, result.Totals.Value)
			require.EqualValues(t, 1, result.Totals.TurnsUnknown)
			require.Contains(t, rec.Body.String(), `"value":null`)
		} else {
			rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
			require.NoError(t, err)
			require.Empty(t, rows[1][4])
			require.Empty(t, rows[2][4])
			require.Equal(t, "1", rows[2][7])
		}
	}
}

func TestUsageQueryParsing(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	id := uuid.New()
	q, err := parseUsageQuery(url.Values{
		"group_by": {"project"}, "from": {"2026-10-01"}, "to": {"2026-10-02"},
		"project_id[]": {id.String()}, "model[]": {"one", "two"}, "provider": {"a,b"},
	}, now)
	require.Nil(t, err)
	require.Equal(t, "Europe/Moscow", q.Loc.String())
	require.Equal(t, "2026-10-01T00:00:00+03:00", q.From.Format(time.RFC3339))
	require.Equal(t, "2026-10-03T00:00:00+03:00", q.To.Format(time.RFC3339))
	require.Equal(t, service.UsageStepHour, q.Step)
	require.Equal(t, []uuid.UUID{id}, q.ProjectIDs)
	require.Equal(t, []string{"one", "two"}, q.Models)
	require.Equal(t, []string{"a", "b"}, q.Providers)
	// The second 01:30 at the DST fold still covers exactly 24 hourly buckets.
	foldNow := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)
	foldQuery, foldErr := parseUsageQuery(url.Values{"group_by": {"model"}, "period": {"1d"}, "tz": {"America/New_York"}}, foldNow)
	require.Nil(t, foldErr)
	require.True(t, foldQuery.From.Equal(foldNow.Add(-23*time.Hour-30*time.Minute)))
	for _, query := range []string{"", "group_by=model&tz=invalid", "group_by=model&from=2026-10-01", "group_by=model&period=90d", "group_by=model&top=21", "group_by=model&project_id[]=bad", "group_by=model&step=minute", "group_by=model&from=2026-10-02&to=2026-10-01"} {
		v, parseErr := url.ParseQuery(query)
		require.NoError(t, parseErr)
		_, err := parseUsageQuery(v, now)
		require.NotNil(t, err, query)
	}
}

func TestMyUsageHandler_WorkspaceVisibilityAndTotals(t *testing.T) {
	db := reportDB(t)
	user, memberOnly, other := uuid.New(), uuid.New(), uuid.New()
	owned, member, foreign := uuid.New(), uuid.New(), uuid.New()
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := db.Exec(query, args...)
		require.NoError(t, err)
	}
	for id, name := range map[uuid.UUID]string{user: "User", memberOnly: "Member only", other: "Other"} {
		exec(`INSERT INTO users (id,email,password_hash,display_name,username,is_active) VALUES ($1,$2,'x',$3,$4,true)`, id, id.String()+"@usage.test", name, id.String())
	}
	for id, name := range map[uuid.UUID]string{owned: "Owned", member: "Member", foreign: "Foreign"} {
		owner := other
		if id == owned {
			owner = user
		}
		exec(`INSERT INTO workspaces (id,name,slug,owner_id) VALUES ($1,$2,$3,$4)`, id, name, id.String(), owner)
	}
	for _, row := range []struct {
		ws, user uuid.UUID
		role     string
	}{{owned, user, domain.RoleOwner}, {member, user, domain.RoleMember}, {member, memberOnly, domain.RoleMember}} {
		exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES ($1,$2,$3,$4)`, uuid.New(), row.ws, row.user, row.role)
	}
	allowed, hidden := uuid.New(), uuid.New()
	for _, project := range []uuid.UUID{allowed, hidden} {
		exec(`INSERT INTO projects (id,workspace_id,name,slug) VALUES ($1,$2,$3,$3)`, project, member, project.String())
	}
	exec(`INSERT INTO project_members (id,project_id,workspace_id,user_id,role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), allowed, member, user)
	exec(`INSERT INTO project_members (id,project_id,workspace_id,user_id,role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), allowed, member, memberOnly)
	exec(`INSERT INTO integration_configs (workspace_id,provider,is_active) VALUES ($1,'runner',true)`, owned)
	agents := map[uuid.UUID]uuid.UUID{}
	for _, ws := range []uuid.UUID{owned, member, foreign} {
		agent := uuid.New()
		agents[ws] = agent
		exec(`INSERT INTO agents (id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES ($1,$2,'Agent',$3,'hash','test')`, agent, ws, agent.String())
	}
	exec(`UPDATE agents SET operator_user_id=$1 WHERE id=$2`, user, agents[owned])
	hourly := func(ws, project uuid.UUID, tokens int, account string) {
		t.Helper()
		exec(`INSERT INTO agent_usage_hourly (workspace_id,hour,agent_id,project_id,model,provider,account_ref,billing_mode,turns,turns_tokens_known,tokens_in,tokens_out)
			VALUES ($1,'2026-10-02T10:00:00Z',$2,$3,'model','test',$4,'',1,1,$5,0)`, ws, agents[ws], project, account, tokens)
	}
	hourly(owned, uuid.Nil, 100, "owned/account")
	hourly(member, allowed, 200, "member/account")
	hourly(member, hidden, 400, "hidden/account")
	hourly(member, uuid.Nil, 800, "orphan/account")
	hourly(foreign, uuid.Nil, 1600, "foreign/account")
	t.Cleanup(func() {
		for _, ws := range []uuid.UUID{owned, member, foreign} {
			_, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws)
		}
		for _, id := range []uuid.UUID{user, memberOnly, other} {
			_, _ = db.Exec(`DELETE FROM users WHERE id=$1`, id)
		}
	})

	h := NewAnalyticsHandler(service.NewAnalyticsService(db))
	get := func(viewer uuid.UUID, query string, asAgent ...bool) *httptest.ResponseRecorder {
		t.Helper()
		e := echo.New()
		e.GET("/me/analytics/usage", h.GetMyUsage, func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				if len(asAgent) > 0 && asAgent[0] {
					c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
					c.Set(mw.ContextKeyAgentID, viewer)
				} else {
					asWorkspaceUser(c, viewer, domain.RoleMember)
				}
				return next(c)
			}
		})
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me/analytics/usage?"+query, http.NoBody))
		return rec
	}
	const workspaceQuery = "from=2026-10-01&to=2026-10-03&metric=tokens&group_by=workspace&tz=UTC"
	rec := get(user, workspaceQuery)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var result service.UsageResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Len(t, result.Rows, 2)
	require.NotNil(t, result.Totals.Value)
	require.Equal(t, 300.0, *result.Totals.Value)
	require.Equal(t, 300.0, seriesSumForHandler(result.Series))
	labels := map[string]string{}
	for _, row := range result.Rows {
		labels[row.Key] = row.Label
	}
	require.Equal(t, map[string]string{owned.String(): "Owned", member.String(): "Member"}, labels)
	require.Contains(t, result.Capabilities.GroupBy, service.UsageDimWorkspace)
	require.Contains(t, result.Capabilities.GroupBy, service.UsageDimAccount)

	ownedNoRunner := uuid.New()
	exec(`INSERT INTO workspaces (id,name,slug,owner_id) VALUES ($1,'Owned without runner',$2,$3)`, ownedNoRunner, ownedNoRunner.String(), user)
	agents[ownedNoRunner] = uuid.New()
	exec(`INSERT INTO agents (id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES ($1,$2,'Agent',$3,'hash','test')`, agents[ownedNoRunner], ownedNoRunner, agents[ownedNoRunner].String())
	hourly(ownedNoRunner, uuid.Nil, 50, "no-runner/account")
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ownedNoRunner) })

	rec = get(user, "from=2026-10-01&to=2026-10-03&metric=tokens&group_by=account&tz=UTC")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Len(t, result.Rows, 1, "the member workspace is excluded from the admin-only account cut")
	require.Equal(t, "owned/account", result.Rows[0].Key)
	require.Equal(t, 100.0, *result.Totals.Value)
	_, err := db.Exec(`DELETE FROM workspaces WHERE id=$1`, ownedNoRunner)
	require.NoError(t, err)

	rec = get(memberOnly, workspaceQuery)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Len(t, result.Rows, 1)
	require.Equal(t, member.String(), result.Rows[0].Key)
	require.Equal(t, 200.0, *result.Totals.Value)
	require.NotContains(t, result.Capabilities.GroupBy, service.UsageDimAccount)
	rec = get(memberOnly, "from=2026-10-01&to=2026-10-03&metric=tokens&group_by=account&tz=UTC")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = get(user, workspaceQuery+"&workspace_id[]="+foreign.String())
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = get(user, workspaceQuery+"&project_id[]="+hidden.String())
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	rec = get(user, workspaceQuery+"&format=csv")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 4)
	require.Equal(t, "300", rows[3][4])

	rec = get(agents[owned], workspaceQuery, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Equal(t, 300.0, *result.Totals.Value, "an agent key reads its operator's workspaces")
	rec = get(agents[member], workspaceQuery, true)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String(), "an agent without an operator has no /me identity")

	rec = get(user, workspaceQuery+"&workspace_id[]=not-a-uuid")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "invalid workspace_id")

	serve := func(setup func(echo.Context)) *httptest.ResponseRecorder {
		t.Helper()
		e := echo.New()
		e.GET("/me/analytics/usage", h.GetMyUsage, func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error { setup(c); return next(c) }
		})
		out := httptest.NewRecorder()
		e.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/me/analytics/usage?"+workspaceQuery, http.NoBody))
		return out
	}
	rec = serve(func(echo.Context) {})
	require.Equal(t, http.StatusForbidden, rec.Code, "no user or agent identity")
	rec = serve(func(c echo.Context) {
		asWorkspaceUser(c, user, domain.RoleMember)
		c.Set(mw.ContextKeyOAuthConnectorUserID, user)
	})
	require.Equal(t, http.StatusForbidden, rec.Code, "an OAuth connector has no /me identity")
	require.Contains(t, rec.Body.String(), "user context required")
}

func seriesSumForHandler(series []service.UsageSeries) float64 {
	var total float64
	for _, s := range series {
		for _, value := range s.Values {
			if value != nil {
				total += *value
			}
		}
	}
	return total
}
