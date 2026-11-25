package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

func memoryAnalyticsContext(query string) echo.Context {
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/?"+query, http.NoBody), httptest.NewRecorder())
	c.SetParamNames("ws_id")
	c.SetParamValues(uuid.NewString())
	asWorkspaceUser(c, uuid.New(), domain.RoleMember)
	return c
}

func TestMemoryAnalyticsFilterPresets(t *testing.T) {
	for _, tc := range []struct {
		query string
		days  int
	}{{"days=1", 1}, {"days=7", 7}, {"days=30", 30}, {"days=90", 90}, {"", 7}} {
		t.Run(tc.query, func(t *testing.T) {
			f, err := memoryFilter(memoryAnalyticsContext(tc.query))
			require.Nil(t, err)
			today := f.Now.Truncate(24 * time.Hour)
			require.Equal(t, today.AddDate(0, 0, -(tc.days-1)), f.From)
			require.Equal(t, f.Now, f.To)
		})
	}
}

func TestMemoryAnalyticsFilterFutureWindow(t *testing.T) {
	_, err := memoryFilter(memoryAnalyticsContext("from=2020-01-01&to=2099-01-01"))
	// A future end is clamped before validating the maximum window.
	require.NotNil(t, err, "the effective window still exceeds 366 days")
	_, err = memoryFilter(memoryAnalyticsContext("from=2098-01-01&to=2098-01-02"))
	require.NotNil(t, err, "a wholly future window must not become an inverted SQL range")
	from := time.Now().UTC().AddDate(0, 0, -2).Format("2006-01-02")
	f, err := memoryFilter(memoryAnalyticsContext("from=" + from + "&to=2099-01-01"))
	require.Nil(t, err)
	require.Equal(t, f.Now, f.To)
}

func TestMemoryAnalyticsFilterRepeatedSelection(t *testing.T) {
	a, b, project := uuid.New(), uuid.New(), uuid.New()
	f, err := memoryFilter(memoryAnalyticsContext("days=7&agent_id[]=" + a.String() + "&agent_id=" + b.String() +
		"&project_id=" + project.String() + "&op[]=recall&op[]=get_memory&source[]=memory&caller_kind[]=unknown&group_by=agent&group_by2=project&top_n=5"))
	require.Nil(t, err)
	require.ElementsMatch(t, []uuid.UUID{a, b}, f.AgentIDs)
	require.Equal(t, &project, f.ProjectID)
	require.Equal(t, []string{"recall", "get_memory"}, f.Ops)
	require.Equal(t, []string{"memory"}, f.Sources)
	require.Equal(t, []string{"unknown"}, f.CallerKinds)
	require.Equal(t, "agent", f.GroupBy)
	require.Equal(t, "project", f.GroupBy2)
	require.Equal(t, 5, f.TopN)
	require.False(t, f.Viewer.AllProjects)
}

func TestMemoryAnalyticsFilterRejectsInvalidSelection(t *testing.T) {
	for _, query := range []string{"days=2", "project_id=no", "agent_id[]=no", "op[]=drop", "source[]=secret",
		"caller_kind[]=bot", "group_by=content", "group_by2=agent", "group_by=agent&group_by2=agent",
		"view=unknown", "top_n=0", "top_n=201", "scope[]=content", "dead_days=7", "from=invalid", "from=2026-01-02&to=2026-01-01",
		"prev_from=invalid&prev_to=2026-01-02", "prev_from=2026-01-01", "prev_to=2026-01-02", "prev_from=2026-01-02&prev_to=2026-01-01",
		"prev_from=2020-01-01&prev_to=2026-01-01", "prev_from=2098-01-01&prev_to=2098-01-02", "source[]=*", "source[]=project_updates"} {
		t.Run(query, func(t *testing.T) {
			_, err := memoryFilter(memoryAnalyticsContext(query))
			require.NotNil(t, err)
			require.Equal(t, http.StatusBadRequest, err.Code)
		})
	}
}

func TestMemoryAnalyticsFilterSourceContract(t *testing.T) {
	for _, source := range []string{"memory", "project_knowledge", "project_update", "doc", "unknown"} {
		f, err := memoryFilter(memoryAnalyticsContext("source[]=" + source))
		require.Nil(t, err)
		require.Equal(t, []string{source}, f.Sources)
	}
}

func TestMemoryAnalyticsFilterPreviousPeriod(t *testing.T) {
	f, err := memoryFilter(memoryAnalyticsContext("prev_from=2026-01-01&prev_to=2026-01-08"))
	require.Nil(t, err)
	require.NotNil(t, f.PreviousFrom)
	require.NotNil(t, f.PreviousTo)
	require.Equal(t, "2026-01-01", f.PreviousFrom.Format("2006-01-02"))
	require.Equal(t, "2026-01-08", f.PreviousTo.Format("2006-01-02"))
}
func TestMemoryAnalyticsFilterViewer(t *testing.T) {
	for _, role := range []string{domain.RoleOwner, domain.RoleAdmin, domain.RoleMember} {
		t.Run(role, func(t *testing.T) {
			c := memoryAnalyticsContext("days=7")
			actor := uuid.New()
			asWorkspaceUser(c, actor, role)
			f, err := memoryFilter(c)
			require.Nil(t, err)
			require.Equal(t, actor, f.Viewer.ActorID)
			require.Equal(t, role != domain.RoleMember, f.Viewer.AllProjects)
		})
	}
	t.Run("agent", func(t *testing.T) {
		c := memoryAnalyticsContext("days=7")
		actor := uuid.New()
		c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
		c.Set(mw.ContextKeyAgentID, actor)
		f, err := memoryFilter(c)
		require.Nil(t, err)
		require.Equal(t, actor, f.Viewer.ActorID)
		require.True(t, f.Viewer.Agent)
		require.False(t, f.Viewer.AllProjects)
	})
	t.Run("no identity", func(t *testing.T) {
		c := memoryAnalyticsContext("days=7")
		c.Set(mw.ContextKeyUserID, nil)
		_, err := memoryFilter(c)
		require.NotNil(t, err)
		require.Equal(t, http.StatusForbidden, err.Code)
	})
	t.Run("extension cannot inherit a user's analytics access", func(t *testing.T) {
		c := memoryAnalyticsContext("days=7")
		c.Set(mw.ContextKeyAuthType, mw.AuthTypeExtension)
		_, err := memoryFilter(c)
		require.NotNil(t, err)
		require.Equal(t, http.StatusForbidden, err.Code)
	})
}

func TestMemoryAnalyticsListParams(t *testing.T) {
	p, err := memoryListParams(memoryAnalyticsContext("page=2&page_size=17&q=%20query%20&sort=last_at"))
	require.Nil(t, err)
	require.Equal(t, 2, p.Page)
	require.Equal(t, 17, p.PageSize)
	require.Equal(t, "query", p.Search)
	require.Equal(t, "last_at", p.Sort)
	for _, query := range []string{"page=0", "page=-1", "page=invalid", "page_size=0", "page_size=101", "page_size=201"} {
		_, err := memoryListParams(memoryAnalyticsContext(query))
		require.NotNil(t, err, query)
		require.Equal(t, http.StatusBadRequest, err.Code)
	}
}

func TestMemoryAnalyticsFilterRecordDimensions(t *testing.T) {
	for _, dim := range []string{"kind", "scope"} {
		f, err := memoryFilter(memoryAnalyticsContext("group_by=" + dim + "&kind[]=guide&scope[]=agent"))
		require.Nil(t, err)
		require.Equal(t, dim, f.GroupBy)
		require.Equal(t, []string{"guide"}, f.Kinds)
		require.Equal(t, []string{"agent"}, f.Scopes)
	}
}

func TestMemoryAnalyticsDetailTimeoutResponse(t *testing.T) {
	c := memoryAnalyticsContext("")
	require.NoError(t, handleMemoryError(c, &service.MemoryDetailTimeoutError{}))
	rec := c.Response().Writer.(*httptest.ResponseRecorder)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.JSONEq(t, `{"code":503,"error_code":"service_unavailable","message":"Memory detail query timed out","reason":"detail_timeout"}`, rec.Body.String())
}
