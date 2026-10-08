package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func TestTaskListFilters_ProjectAndWorkspace(t *testing.T) {
	agent := uuid.New()
	for _, global := range []bool{false, true} {
		t.Run(map[bool]string{false: "project", true: "workspace"}[global], func(t *testing.T) {
			called := false
			check := func(_ context.Context, _ uuid.UUID, f repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
				called = true
				require.NotNil(t, f.AssigneeID)
				assert.Equal(t, agent, *f.AssigneeID)
				require.NotNil(t, f.StatusCategory)
				assert.Equal(t, domain.StatusCategoryTodo, *f.StatusCategory)
				require.NotNil(t, f.AssigneeType)
				assert.Equal(t, domain.AssigneeTypeAgent, *f.AssigneeType)
				require.NotNil(t, f.Priority)
				assert.Equal(t, domain.PriorityHigh, *f.Priority)
				assert.Equal(t, []string{"gate"}, f.Labels)
				require.NotNil(t, f.HumanGate)
				assert.True(t, *f.HumanGate)
				assert.Equal(t, 2, pg.Page)
				assert.Equal(t, 20, pg.PageSize)
				return pagination.NewPage([]domain.Task{}, 0, pg), nil
			}
			h, e := setupTaskTest(&MockTaskService{ListFunc: check, SearchFunc: check})
			req := httptest.NewRequest(http.MethodGet, "/?search=none&status_category=todo&assignee_id="+agent.String()+"&assignee_type=agent&priority=high&labels=gate&human_gate=true&page=2&page_size=20", http.NoBody)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			if global {
				c.SetParamNames("ws_id")
			} else {
				c.SetParamNames("proj_id")
			}
			c.SetParamValues(uuid.NewString())
			if global {
				require.NoError(t, h.SearchGlobal(c))
			} else {
				require.NoError(t, h.List(c))
			}
			require.Equal(t, http.StatusOK, rec.Code)
			assert.True(t, called)
		})
	}
}

func TestTaskListFilters_RESTTriageCompatibility(t *testing.T) {
	for _, global := range []bool{false, true} {
		called := false
		check := func(_ context.Context, _ uuid.UUID, f repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
			called = true
			require.NotNil(t, f.StatusCategory)
			assert.Equal(t, domain.StatusCategoryTriage, *f.StatusCategory)
			return pagination.NewPage([]domain.Task{}, 0, pg), nil
		}
		h, e := setupTaskTest(&MockTaskService{ListFunc: check, SearchFunc: check})
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/?search=none&status_category=triage", http.NoBody), rec)
		if global {
			c.SetParamNames("ws_id")
		} else {
			c.SetParamNames("proj_id")
		}
		c.SetParamValues(uuid.NewString())
		if global {
			require.NoError(t, h.SearchGlobal(c))
		} else {
			require.NoError(t, h.List(c))
		}
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, called)
	}
}
func TestTaskListFilters_InvalidAssigneeAndCategory(t *testing.T) {
	for _, global := range []bool{false, true} {
		for _, query := range []string{"assignee_id=bart", "status_category=active"} {
			h, e := setupTaskTest(&MockTaskService{
				ListFunc: func(context.Context, uuid.UUID, repository.TaskFilter, pagination.Params) (*pagination.Page[domain.Task], error) {
					t.Error("invalid project filters reached service")
					return pagination.NewPage([]domain.Task{}, 0, pagination.Params{}), nil
				},
				SearchFunc: func(context.Context, uuid.UUID, repository.TaskFilter, pagination.Params) (*pagination.Page[domain.Task], error) {
					t.Error("invalid workspace filters reached service")
					return pagination.NewPage([]domain.Task{}, 0, pagination.Params{}), nil
				},
			})
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodGet, "/?search=none&"+query, http.NoBody), rec)
			if global {
				c.SetParamNames("ws_id")
			} else {
				c.SetParamNames("proj_id")
			}
			c.SetParamValues(uuid.NewString())
			if global {
				require.NoError(t, h.SearchGlobal(c))
			} else {
				require.NoError(t, h.List(c))
			}
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		}
	}
}

func TestTaskListFilters_WorkspaceSortDefaultAndExplicitOrder(t *testing.T) {
	for _, tc := range []struct{ query, dir string }{{"", "desc"}, {"&order=asc", "asc"}, {"&sort_by=created_at&order=desc", "desc"}} {
		t.Run(tc.query, func(t *testing.T) {
			called := false
			h, e := setupTaskTest(&MockTaskService{SearchFunc: func(_ context.Context, _ uuid.UUID, _ repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
				called = true
				assert.Equal(t, tc.dir, pg.SortDir)
				require.NotEmpty(t, pg.SortBy)
				return pagination.NewPage([]domain.Task{}, 0, pg), nil
			}})
			c := e.NewContext(httptest.NewRequest(http.MethodGet, "/?search=none"+tc.query, http.NoBody), httptest.NewRecorder())
			c.SetParamNames("ws_id")
			c.SetParamValues(uuid.NewString())
			require.NoError(t, h.SearchGlobal(c))
			require.True(t, called)
		})
	}
}
