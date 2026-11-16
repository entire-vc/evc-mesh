package handler

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// asWorkspaceUser puts a human caller with the given workspace role on c.
func asWorkspaceUser(c echo.Context, userID uuid.UUID, role string) {
	c.Set(mw.ContextKeyAuthType, mw.AuthTypeUser)
	c.Set(mw.ContextKeyUserID, userID)
	c.Set(mw.ContextKeyWorkspaceRole, role)
}

// findCSVRow returns the first row of the parsed CSV whose first two columns
// (Metric, Category) match category/field, or nil if none does.
func findCSVRow(t *testing.T, body, category, field string) []string {
	t.Helper()
	r := csv.NewReader(strings.NewReader(body))
	rows, err := r.ReadAll()
	require.NoError(t, err)
	for _, row := range rows {
		if len(row) >= 2 && row[0] == category && row[1] == field {
			return row
		}
	}
	return nil
}

func doExportMetrics(t *testing.T, mockSvc *MockAnalyticsService) *httptest.ResponseRecorder {
	t.Helper()
	h := NewAnalyticsHandler(mockSvc)
	req := httptest.NewRequest(http.MethodGet, "/?format=csv&from=2026-08-01&to=2026-08-02", http.NoBody)
	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/analytics/export")
	c.SetParamNames("ws_id")
	c.SetParamValues(uuid.New().String())
	asWorkspaceUser(c, uuid.New(), domain.RoleOwner)

	require.NoError(t, h.ExportMetrics(c))
	return rec
}

// TestAnalyticsHandler_ExportMetrics_RetainedSincePresent covers the branch
// added for an internal ticket: when the event queue's TTL-floor is known, the
// CSV must carry a `retained_since` row alongside `total_events` and
// `period_fully_covered` — the exact condition the audit found silently
// missing (a closed August report reading the live queue with no coverage
// caveat at all).
func TestAnalyticsHandler_ExportMetrics_RetainedSincePresent(t *testing.T) {
	retainedSince := time.Date(2026, 9, 14, 0, 0, 52, 0, time.UTC)
	mockSvc := &MockAnalyticsService{
		GetMetricsFunc: func(_ context.Context, _ service.AnalyticsFilter) (*service.AnalyticsMetrics, error) {
			return &service.AnalyticsMetrics{
				EventMetrics: service.EventMetrics{
					TotalEvents:        0,
					ByType:             map[string]int{},
					RetainedSince:      &retainedSince,
					PeriodFullyCovered: false,
				},
			}, nil
		},
	}

	rec := doExportMetrics(t, mockSvc)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	totalRow := findCSVRow(t, body, "Event Coverage", "total_events")
	require.NotNil(t, totalRow, "total_events row must always be present")
	require.Equal(t, "0", totalRow[2])

	coveredRow := findCSVRow(t, body, "Event Coverage", "period_fully_covered")
	require.NotNil(t, coveredRow, "period_fully_covered row must always be present")
	require.Equal(t, "false", coveredRow[2])

	retainedRow := findCSVRow(t, body, "Event Coverage", "retained_since")
	require.NotNil(t, retainedRow, "retained_since row must appear when RetainedSince is non-nil")
	require.Equal(t, retainedSince.Format(time.RFC3339), retainedRow[2])
}

// TestAnalyticsHandler_ExportMetrics_RetainedSinceNil is the negative
// control: an empty scope (queue holds nothing at all) must NOT print a
// `retained_since` row — there is no floor to report.
func TestAnalyticsHandler_ExportMetrics_RetainedSinceNil(t *testing.T) {
	mockSvc := &MockAnalyticsService{
		GetMetricsFunc: func(_ context.Context, _ service.AnalyticsFilter) (*service.AnalyticsMetrics, error) {
			return &service.AnalyticsMetrics{
				EventMetrics: service.EventMetrics{
					TotalEvents:        0,
					ByType:             map[string]int{},
					RetainedSince:      nil,
					PeriodFullyCovered: false,
				},
			}, nil
		},
	}

	rec := doExportMetrics(t, mockSvc)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Nil(t, findCSVRow(t, body, "Event Coverage", "retained_since"),
		"retained_since row must be absent when RetainedSince is nil")
}

func doGetMetrics(t *testing.T, query string, mockSvc *MockAnalyticsService) *httptest.ResponseRecorder {
	t.Helper()
	h := NewAnalyticsHandler(mockSvc)
	req := httptest.NewRequest(http.MethodGet, "/?"+query, http.NoBody)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetPath("/workspaces/:ws_id/analytics")
	c.SetParamNames("ws_id")
	c.SetParamValues(uuid.New().String())
	asWorkspaceUser(c, uuid.New(), domain.RoleOwner)
	require.NoError(t, h.GetMetrics(c))
	return rec
}

func TestAnalyticsHandler_GetMetrics_TopTasksSort(t *testing.T) {
	cases := []struct {
		query    string
		wantCode int
		wantSort string
	}{
		{"", http.StatusOK, service.TopTasksSortCost},
		{"top_tasks_sort=cost", http.StatusOK, service.TopTasksSortCost},
		{"top_tasks_sort=tokens", http.StatusOK, service.TopTasksSortTokens},
		{"top_tasks_sort=bogus", http.StatusBadRequest, ""},
		{"top_tasks_sort=Tokens", http.StatusBadRequest, ""},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			var got *service.AnalyticsFilter
			rec := doGetMetrics(t, tc.query, &MockAnalyticsService{
				GetMetricsFunc: func(_ context.Context, f service.AnalyticsFilter) (*service.AnalyticsMetrics, error) {
					got = &f
					return &service.AnalyticsMetrics{}, nil
				},
			})
			require.Equal(t, tc.wantCode, rec.Code)
			if tc.wantCode == http.StatusOK {
				require.NotNil(t, got)
				require.Equal(t, tc.wantSort, got.TopTasksSort)
			} else {
				require.Nil(t, got, "service must not be called on an invalid sort")
			}
		})
	}
}

// TestAnalyticsHandler_ViewerFromCaller pins who the aggregates are computed
// for: owner and admin see every project, a member and an agent only their own
// memberships, and a caller with no identity is refused before any query.
func TestAnalyticsHandler_ViewerFromCaller(t *testing.T) {
	userID, agentID := uuid.New(), uuid.New()
	cases := []struct {
		name   string
		setup  func(c echo.Context)
		want   *service.AnalyticsViewer
		status int
	}{
		{"owner", func(c echo.Context) { asWorkspaceUser(c, userID, domain.RoleOwner) }, &service.AnalyticsViewer{ActorID: userID, AllProjects: true}, http.StatusOK},
		{"admin", func(c echo.Context) { asWorkspaceUser(c, userID, domain.RoleAdmin) }, &service.AnalyticsViewer{ActorID: userID, AllProjects: true}, http.StatusOK},
		{"member", func(c echo.Context) { asWorkspaceUser(c, userID, domain.RoleMember) }, &service.AnalyticsViewer{ActorID: userID}, http.StatusOK},
		{"agent with owner role", func(c echo.Context) {
			c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
			c.Set(mw.ContextKeyAgentID, agentID)
			c.Set(mw.ContextKeyWorkspaceRole, domain.RoleOwner)
		}, &service.AnalyticsViewer{ActorID: agentID, Agent: true}, http.StatusOK},
		{"no identity", func(echo.Context) {}, nil, http.StatusForbidden},
	}
	for _, tc := range cases {
		for _, export := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/export=%t", tc.name, export), func(t *testing.T) {
				var got *service.AnalyticsViewer
				h := NewAnalyticsHandler(&MockAnalyticsService{
					GetMetricsFunc: func(_ context.Context, f service.AnalyticsFilter) (*service.AnalyticsMetrics, error) {
						v := f.Viewer
						got = &v
						return &service.AnalyticsMetrics{}, nil
					},
				})
				rec := httptest.NewRecorder()
				c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/?format=csv", http.NoBody), rec)
				c.SetParamNames("ws_id")
				c.SetParamValues(uuid.New().String())
				tc.setup(c)
				if export {
					require.NoError(t, h.ExportMetrics(c))
				} else {
					require.NoError(t, h.GetMetrics(c))
				}
				require.Equal(t, tc.status, rec.Code)
				require.Equal(t, tc.want, got)
			})
		}
	}
}

// TestAnalyticsHandler_ExportMetrics_NeutralisesFormulas: names come from users
// (assignees, projects, agents); a name a spreadsheet would run as a formula is
// written as text. Ordinary names are the positive control and stay as they are.
func TestAnalyticsHandler_ExportMetrics_NeutralisesFormulas(t *testing.T) {
	mockSvc := &MockAnalyticsService{
		GetMetricsFunc: func(_ context.Context, _ service.AnalyticsFilter) (*service.AnalyticsMetrics, error) {
			return &service.AnalyticsMetrics{
				TaskMetrics: service.TaskMetrics{
					ByAssignee: []service.AssigneeCountRow{
						{AssigneeType: "user", Name: "=1+1", Count: 1},
						{AssigneeType: "user", Name: "Alice", Count: 2},
					},
					ByProject: []service.ProjectCountRow{
						{ProjectName: "@SUM(A1)", Count: 1},
						{ProjectName: "Ordinary Project", Count: 3},
					},
				},
				AgentMetrics: service.AgentMetrics{
					TasksByAgent: []service.AgentTaskRow{
						{AgentName: "+cmd|' /C calc'!A0", Completed: 1}, {AgentName: "-2", Completed: 1}, {AgentName: "\n=1+1", Completed: 1},
					},
				},
				EventMetrics: service.EventMetrics{ByType: map[string]int{}},
			}, nil
		},
	}

	body := doExportMetrics(t, mockSvc).Body.String()
	for _, c := range []struct{ metric, cell, count string }{
		{"Tasks by Assignee", "'=1+1", "1"},
		{"Tasks by Assignee", "Alice", "2"},
		{"Tasks by Project", "'@SUM(A1)", "1"},
		{"Tasks by Project", "Ordinary Project", "3"},
		{"Agent Activity", "'+cmd|' /C calc'!A0", "1"},
		{"Agent Activity", "'-2", "1"},
		{"Agent Activity", "'\n=1+1", "1"},
	} {
		row := findCSVRow(t, body, c.metric, c.cell)
		require.NotNil(t, row, "%s row %q missing in:\n%s", c.metric, c.cell, body)
		require.Equal(t, c.count, row[2])
	}
	require.Nil(t, findCSVRow(t, body, "Tasks by Assignee", "=1+1"), "a raw formula reached the CSV")
}

// TestCSVText: every start a spreadsheet may read as a formula becomes text;
// ordinary names, inner formula characters and a plain leading space stay as
// they are (the positive controls).
func TestCSVText(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"=1+1", "'=1+1"},
		{"+1", "'+1"},
		{"-1", "'-1"},
		{"@SUM(A1)", "'@SUM(A1)"},
		{"\t=1+1", "'\t=1+1"},
		{"\r=1+1", "'\r=1+1"},
		{"\n=1+1", "'\n=1+1"},
		{"\nAlice", "'\nAlice"},
		{"  =1+1", "'  =1+1"},
		{"\u00a0=1+1", "'\u00a0=1+1"},
		{"＝1+1", "'＝1+1"},
		{"＋1", "'＋1"},
		{"－1", "'－1"},
		{"＠SUM(A1)", "'＠SUM(A1)"},
		{"Alice", "Alice"},
		{"Ordinary Project", "Ordinary Project"},
		{"a=1+1", "a=1+1"},
		{" Alice", " Alice"},
		{"Анна-Мария", "Анна-Мария"},
		{"", ""},
	} {
		require.Equal(t, c.want, csvText(c.in), "csvText(%q)", c.in)
	}
}
