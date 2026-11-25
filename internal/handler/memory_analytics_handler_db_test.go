package handler

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

func TestMemoryAnalyticsDuplicateCSVPrivacy(t *testing.T) {
	db := runtimeHTTPDB(t)
	ws, owner := seedRuntimeWorkspace(t, db)
	member, agent, visible, hidden := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		for _, query := range []string{
			`DELETE FROM memories WHERE workspace_id=$1`,
			`DELETE FROM project_members WHERE workspace_id=$1`,
			`DELETE FROM agents WHERE workspace_id=$1`,
			`DELETE FROM projects WHERE workspace_id=$1`,
			`DELETE FROM workspace_members WHERE workspace_id=$1`,
			`DELETE FROM workspaces WHERE id=$1`,
		} {
			_, _ = db.Exec(query, ws)
		}
		_, _ = db.Exec(`DELETE FROM users WHERE id IN ($1,$2)`, owner, member)
	})
	exec := func(query string, args ...interface{}) {
		t.Helper()
		_, err := db.Exec(query, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO users (id,email,password_hash,display_name,username,is_active)
		VALUES ($1,$2,'x','CSV Member',$3,true)`, member, member.String()+"@analytics.test", "csv-"+member.String()[:8])
	exec(`INSERT INTO workspace_members (id,workspace_id,user_id,role) VALUES ($1,$2,$3,'member')`, uuid.New(), ws, member)
	for _, project := range []uuid.UUID{visible, hidden} {
		exec(`INSERT INTO projects (id,workspace_id,name,slug,default_assignee_type,settings)
			VALUES ($1,$2,'CSV project',$3,'none','{}')`, project, ws, "csv-"+project.String()[:8])
	}
	exec(`INSERT INTO project_members (id,project_id,workspace_id,user_id,role)
		VALUES ($1,$2,$3,$4,'member')`, uuid.New(), visible, ws, member)
	exec(`INSERT INTO agents (id,workspace_id,name,slug,api_key_hash,api_key_prefix)
		VALUES ($1,$2,'CSV Agent',$3,'hash','csv_test')`, agent, ws, "csv-"+agent.String()[:8])
	for _, project := range []uuid.UUID{visible, hidden} {
		for _, scope := range []string{"agent", "project"} {
			exec(`INSERT INTO memories (id,workspace_id,project_id,agent_id,key,content,scope,tags,status,content_simhash)
				VALUES ($1,$2,$3,$4,$5,'never export record contents',$6,'{kind:guide}','active',123456789)`,
				uuid.New(), ws, project, agent, scope+"-csv-key-"+project.String(), scope)
		}
	}
	h := NewMemoryAnalyticsHandler(service.NewMemoryAnalyticsService(db))
	request := func(user uuid.UUID, role, query string, handle func(echo.Context) error) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/?days=7&"+query, http.NoBody), rec)
		c.SetParamNames("ws_id")
		c.SetParamValues(ws.String())
		asWorkspaceUser(c, user, role)
		require.NoError(t, handle(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotContains(t, rec.Body.String(), "never export record contents")
		return rec
	}
	jsonResponse := request(member, domain.RoleMember, "", h.Duplicates)
	var list service.MemoryList[service.MemoryRecordRow]
	require.NoError(t, json.Unmarshal(jsonResponse.Body.Bytes(), &list))
	require.EqualValues(t, 2, list.Total, "hidden project rows must not affect the count")
	require.Len(t, list.Items, 2)
	csvResponse := request(member, domain.RoleMember, "list=duplicates", h.Export)
	rows, err := csv.NewReader(strings.NewReader(csvResponse.Body.String())).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, len(list.Items)+1)
	byID := map[string][]string{}
	for _, row := range rows[1:] {
		byID[row[0]] = row
	}
	for _, item := range list.Items {
		require.NotNil(t, item.ProjectID)
		require.Equal(t, visible, *item.ProjectID)
		row := byID[item.MemoryID.String()]
		require.Len(t, row, 14)
		require.Equal(t, strconv.FormatBool(item.Redacted), row[2])
		require.Equal(t, item.Scope, row[3])
		require.Equal(t, visible.String(), row[5])
		require.EqualValues(t, 2, item.GroupSize, "hidden duplicates must not inflate group size")
		require.Equal(t, "2", row[12])
		if item.Scope == "agent" {
			require.True(t, item.Redacted)
			require.Nil(t, item.Key)
			require.Empty(t, row[1])
		} else {
			require.False(t, item.Redacted)
			require.NotNil(t, item.Key)
			require.Equal(t, *item.Key, row[1])
		}
	}
	privateKey := "agent-csv-key-" + visible.String()
	for _, response := range []*httptest.ResponseRecorder{jsonResponse, csvResponse} {
		require.NotContains(t, response.Body.String(), privateKey)
		require.NotContains(t, response.Body.String(), hidden.String())
	}
	for _, endpoint := range []struct {
		query  string
		handle func(echo.Context) error
	}{{"q=" + privateKey, h.Duplicates}, {"list=duplicates&q=" + privateKey, h.Export}} {
		denied := request(member, domain.RoleMember, endpoint.query, endpoint.handle)
		require.NotContains(t, denied.Body.String(), privateKey, "search must not be a private-key oracle")
		allowed := request(owner, domain.RoleOwner, endpoint.query, endpoint.handle)
		require.Contains(t, allowed.Body.String(), privateKey, "positive control: the admin can find and export the same key")
	}
}

// Exercise the real handler and SQL service against the migrated database.
// In particular, an export must keep the JSON endpoint's caller-kind filter.
func TestMemoryAnalyticsContractCSVSelection(t *testing.T) {
	db := runtimeHTTPDB(t)
	ws, owner := seedRuntimeWorkspace(t, db)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM memory_read_events WHERE workspace_id=$1`, ws)
		_, _ = db.Exec(`DELETE FROM workspace_members WHERE workspace_id=$1`, ws)
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws)
		_, _ = db.Exec(`DELETE FROM users WHERE id=$1`, owner)
	})
	for _, caller := range []string{"unknown", "agent_tool"} {
		_, err := db.Exec(`INSERT INTO memory_read_events
			(id, ts, workspace_id, caller_kind, route, user_agent, op, query_hash, query_text,
			 result_count, pinned_count, superseded_count, results, response_chars)
			VALUES ($1,$2,$3,$4,'/recall','contract-test','recall',$4,$5,0,0,0,'[]',0)`,
			uuid.New(), time.Now().UTC().Add(-time.Second), ws, caller, caller+" query")
		require.NoError(t, err)
	}
	h := NewMemoryAnalyticsHandler(service.NewMemoryAnalyticsService(db))
	request := func(query string, handle func(echo.Context) error) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/?days=7&caller_kind[]=unknown&"+query, http.NoBody), rec)
		c.SetParamNames("ws_id")
		c.SetParamValues(ws.String())
		asWorkspaceUser(c, owner, domain.RoleOwner)
		require.NoError(t, handle(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec
	}
	jsonResponse := request("", h.EmptyQueries)
	var list service.MemoryList[service.MemoryEmptyQuery]
	require.NoError(t, json.Unmarshal(jsonResponse.Body.Bytes(), &list))
	require.EqualValues(t, 1, list.Total)
	require.Len(t, list.Items, 1)
	require.NotNil(t, list.Items[0].QueryText)
	require.Equal(t, "unknown query", *list.Items[0].QueryText)
	require.EqualValues(t, 1, list.Items[0].Count)

	csvResponse := request("format=csv&list=empty-queries", h.Export)
	require.Contains(t, csvResponse.Header().Get("Content-Type"), "text/csv")
	rows, err := csv.NewReader(strings.NewReader(csvResponse.Body.String())).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 2, "CSV contains precisely the same selected query as JSON")
	require.Equal(t, list.Items[0].QueryHash, rows[1][0])
	require.Equal(t, *list.Items[0].QueryText, rows[1][1])
	require.Equal(t, "false", rows[1][2])
	require.Equal(t, "1", rows[1][5])
}

func TestMemoryAnalyticsContractSummaryCSV(t *testing.T) {
	db := runtimeHTTPDB(t)
	ws, owner := seedRuntimeWorkspace(t, db)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM workspace_members WHERE workspace_id=$1`, ws)
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws)
		_, _ = db.Exec(`DELETE FROM users WHERE id=$1`, owner)
	})
	h := NewMemoryAnalyticsHandler(service.NewMemoryAnalyticsService(db))
	request := func(query string, serve func(echo.Context) error) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/?days=7&"+query, http.NoBody), recorder)
		c.SetParamNames("ws_id")
		c.SetParamValues(ws.String())
		asWorkspaceUser(c, owner, domain.RoleOwner)
		require.NoError(t, serve(c))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		return recorder
	}
	response := request("", h.Get)
	var result service.MemoryAnalytics
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Nil(t, result.KPIs.Reads)
	require.Equal(t, service.MemoryReasonNoJournalBefore, result.Reasons["reads"])
	require.NotNil(t, result.KPIs.Writes, "positive control: writes are measured even without the read journal")
	require.Zero(t, *result.KPIs.Writes)
	response = request("format=csv&list=summary", h.Export)
	rows, err := csv.NewReader(strings.NewReader(response.Body.String())).ReadAll()
	require.NoError(t, err)
	values := map[string][]string{}
	for _, row := range rows[1:] {
		values[row[0]+"/"+row[1]+"/"+row[5]] = row
	}
	for _, metric := range []string{"reads", "hit_rate", "useful_rate", "tokens_est"} {
		row := values["kpis//"+metric]
		require.Len(t, row, 8)
		require.Empty(t, row[6], "CSV must preserve JSON null")
		require.Equal(t, result.Reasons[metric], row[7])
	}
	require.Equal(t, "0", values["kpis//writes"][6])
	require.Empty(t, values["kpis//writes"][7])
	for _, point := range result.Series {
		for _, metric := range []string{"reads", "reads_by_op", "served_rows_by_source", "useful_rate"} {
			row := values["series/"+point.Day+"/"+metric]
			require.Len(t, row, 8)
			require.Empty(t, row[6])
			require.Equal(t, point.Reasons[metric], row[7])
		}
	}
	require.Equal(t, "0", values["totals/total/writes"][6])
	require.Equal(t, service.MemoryReasonNotCollected, values["quality//total"][7])
}

func TestMemoryAnalyticsContractSeriesCSV(t *testing.T) {
	db := runtimeHTTPDB(t)
	ws, owner := seedRuntimeWorkspace(t, db)
	now := time.Now().UTC()
	day := now.Truncate(24*time.Hour).AddDate(0, 0, -1)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM memory_stats_daily WHERE workspace_id=$1`, ws)
		_, _ = db.Exec(`DELETE FROM workspace_members WHERE workspace_id=$1`, ws)
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws)
		_, _ = db.Exec(`DELETE FROM users WHERE id=$1`, owner)
	})
	for _, row := range []struct {
		op, source                  string
		reads, hits, useful, served int
	}{{"recall", "*", 3, 2, 1, 0}, {"recall", "memory", 0, 0, 0, 8}, {"get_memory", "memory", 2, 0, 0, 2}} {
		_, err := db.Exec(`INSERT INTO memory_stats_daily
			(day,workspace_id,caller_kind,op,source,reads,hits,useful,served_rows)
			VALUES ($1,$2,'agent_tool',$3,$4,$5,$6,$7,$8)`, day, ws, row.op, row.source, row.reads, row.hits, row.useful, row.served)
		require.NoError(t, err)
	}
	h := NewMemoryAnalyticsHandler(service.NewMemoryAnalyticsService(db))
	request := func(query string, handle func(echo.Context) error) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet,
			"/?from="+day.Format("2006-01-02")+"&to="+day.AddDate(0, 0, 1).Format("2006-01-02")+"&op=read&"+query, http.NoBody), rec)
		c.SetParamNames("ws_id")
		c.SetParamValues(ws.String())
		asWorkspaceUser(c, owner, domain.RoleOwner)
		require.NoError(t, handle(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec
	}
	var result service.MemoryAnalytics
	response := request("", h.Get)
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Len(t, result.Series, 1)
	point := result.Series[0]
	require.EqualValues(t, 5, *point.Reads)
	require.EqualValues(t, 3, point.ReadsByOp["recall"])
	require.EqualValues(t, 2, point.ReadsByOp["get_memory"])
	require.EqualValues(t, 10, point.ServedRowsBySource["memory"])
	require.InDelta(t, 0.5, *point.UsefulRate, 0.000001)
	response = request("list=summary&format=csv", h.Export)
	rows, err := csv.NewReader(strings.NewReader(response.Body.String())).ReadAll()
	require.NoError(t, err)
	values := map[string]string{}
	for _, row := range rows[1:] {
		if row[0] == "series" && row[1] == point.Day {
			values[row[5]] = row[6]
		}
	}
	require.Equal(t, "5", values["reads"])
	require.Equal(t, "3", values["reads_by_op.recall"])
	require.Equal(t, "2", values["reads_by_op.get_memory"])
	require.Equal(t, "10", values["served_rows_by_source.memory"])
	require.Equal(t, "0.5", values["useful_rate"])
	require.NotContains(t, response.Body.String(), "reads_by_source")
}
