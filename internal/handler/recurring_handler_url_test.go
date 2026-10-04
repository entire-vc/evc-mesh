package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// URL-format tests for the schedule deep-link, mirroring the project/comment
// url tests from !1076: every response branch (list/get/create/update) must
// carry the canonical settings-anchored link, and behind the proxy it must
// carry the public origin, never the internal hop.

// recurringSvcURLStub lets each test control what the service returns, with
// schedules that carry the project/workspace ids the decorator resolves slugs
// by.
type recurringSvcURLStub struct {
	create  *domain.RecurringSchedule
	getByID *domain.RecurringSchedule
	update  *domain.RecurringSchedule
	list    *pagination.Page[domain.RecurringSchedule]
}

func (s *recurringSvcURLStub) Create(context.Context, service.CreateRecurringInput) (*domain.RecurringSchedule, error) {
	return s.create, nil
}

func (s *recurringSvcURLStub) GetByID(_ context.Context, _ uuid.UUID) (*domain.RecurringSchedule, error) {
	return s.getByID, nil
}

func (s *recurringSvcURLStub) Update(_ context.Context, _ uuid.UUID, _ service.UpdateRecurringInput) (*domain.RecurringSchedule, error) {
	return s.update, nil
}

func (s *recurringSvcURLStub) Delete(context.Context, uuid.UUID) error { return nil }

func (s *recurringSvcURLStub) ListByProject(context.Context, uuid.UUID, pagination.Params) (*pagination.Page[domain.RecurringSchedule], error) {
	return s.list, nil
}

func (s *recurringSvcURLStub) TriggerNow(context.Context, uuid.UUID) (*domain.Task, error) {
	return &domain.Task{}, nil
}

func (s *recurringSvcURLStub) GetHistory(context.Context, uuid.UUID, pagination.Params) (*pagination.Page[domain.RecurringInstanceSummary], error) {
	return &pagination.Page[domain.RecurringInstanceSummary]{}, nil
}

func (s *recurringSvcURLStub) RunDue(context.Context) (int, error) { return 0, nil }

// setupRecurringURLTest wires the handler with slug-resolving mock services.
// projectCalls/workspaceCalls count the slug lookups so the tests can pin that
// a list response resolves them once, not per item.
func setupRecurringURLTest(recurring service.RecurringService, projSlug, wsSlug string, projectCalls, workspaceCalls *int) *RecurringHandler {
	projectSvc := &MockProjectService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
			if projectCalls != nil {
				*projectCalls++
			}
			return &domain.Project{ID: id, Slug: projSlug}, nil
		},
	}
	workspaceSvc := &MockWorkspaceService{
		GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Workspace, error) {
			if workspaceCalls != nil {
				*workspaceCalls++
			}
			return &domain.Workspace{ID: id, Slug: wsSlug}, nil
		},
	}
	return NewRecurringHandler(recurring, projectSvc, workspaceSvc)
}

// expectedScheduleURL is the scheduleHref construction from
// web/src/lib/entity-deep-links.ts, the address !1077 verified opening the
// Recurring tab with the schedule highlighted.
func expectedScheduleURL(origin, wsSlug, projSlug string, scheduleID uuid.UUID) string {
	return origin + "/w/" + wsSlug + "/p/" + projSlug + "/settings?tab=recurring&schedule=" + scheduleID.String()
}

func TestRecurringHandler_GetByID_URL(t *testing.T) {
	wsID, projID, schedID := uuid.New(), uuid.New(), uuid.New()
	h := setupRecurringURLTest(&recurringSvcURLStub{
		getByID: &domain.RecurringSchedule{ID: schedID, WorkspaceID: wsID, ProjectID: projID},
	}, "growth", "acme", nil, nil)

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.SetParamNames("recurring_id")
	c.SetParamValues(schedID.String())

	require.NoError(t, h.GetByID(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var result domain.RecurringSchedule
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	// httptest's default host is example.com over plain http.
	assert.Equal(t, expectedScheduleURL("http://example.com", "acme", "growth", schedID), result.URL)
}

// TestRecurringHandler_GetByID_URLRespectsForwardedHeaders pins the same rule
// computeTaskURL established (#fe507dc9): behind Caddy the URL must carry the
// public scheme/host from X-Forwarded-Proto/X-Forwarded-Host, never the
// internal one the request actually arrived on.
func TestRecurringHandler_GetByID_URLRespectsForwardedHeaders(t *testing.T) {
	wsID, projID, schedID := uuid.New(), uuid.New(), uuid.New()
	h := setupRecurringURLTest(&recurringSvcURLStub{
		getByID: &domain.RecurringSchedule{ID: schedID, WorkspaceID: wsID, ProjectID: projID},
	}, "growth", "acme", nil, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "mesh.entire.host")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("recurring_id")
	c.SetParamValues(schedID.String())

	require.NoError(t, h.GetByID(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var result domain.RecurringSchedule
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, expectedScheduleURL("https://mesh.entire.host", "acme", "growth", schedID), result.URL)
}

func TestRecurringHandler_List_URL(t *testing.T) {
	wsID, projID := uuid.New(), uuid.New()
	first, second := uuid.New(), uuid.New()
	var projectCalls, workspaceCalls int
	h := setupRecurringURLTest(&recurringSvcURLStub{
		list: &pagination.Page[domain.RecurringSchedule]{
			Items: []domain.RecurringSchedule{
				{ID: first, WorkspaceID: wsID, ProjectID: projID},
				{ID: second, WorkspaceID: wsID, ProjectID: projID},
			},
			TotalCount: 2,
		},
	}, "growth", "acme", &projectCalls, &workspaceCalls)

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.SetParamNames("proj_id")
	c.SetParamValues(projID.String())

	require.NoError(t, h.List(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var page pagination.Page[domain.RecurringSchedule]
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	// Every item of a list response carries its own deep-link — a field the
	// handler computes on some paths only reads as absent on the others (the
	// #fc032545 lesson on tasks).
	for _, item := range page.Items {
		assert.Equal(t, expectedScheduleURL("http://example.com", "acme", "growth", item.ID), item.URL)
	}
	// One list response is one project: the slugs resolve once, not per item.
	assert.Equal(t, 1, projectCalls)
	assert.Equal(t, 1, workspaceCalls)
}

func TestRecurringHandler_Create_URL(t *testing.T) {
	wsID, projID, schedID := uuid.New(), uuid.New(), uuid.New()
	h := setupRecurringURLTest(&recurringSvcURLStub{
		create: &domain.RecurringSchedule{ID: schedID, WorkspaceID: wsID, ProjectID: projID},
	}, "growth", "acme", nil, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title_template":"Daily check"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("proj_id")
	c.SetParamValues(projID.String())
	c.Set(mw.ContextKeyWorkspaceID, wsID)

	require.NoError(t, h.Create(c))
	require.Equal(t, http.StatusCreated, rec.Code)

	var result domain.RecurringSchedule
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, expectedScheduleURL("http://example.com", "acme", "growth", schedID), result.URL)
}

func TestRecurringHandler_Update_URL(t *testing.T) {
	wsID, projID, schedID := uuid.New(), uuid.New(), uuid.New()
	h := setupRecurringURLTest(&recurringSvcURLStub{
		update: &domain.RecurringSchedule{ID: schedID, WorkspaceID: wsID, ProjectID: projID},
	}, "growth", "acme", nil, nil)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"title_template":"Renamed"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("recurring_id")
	c.SetParamValues(schedID.String())

	require.NoError(t, h.Update(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var result domain.RecurringSchedule
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	// Update answers with the deep-link too, not just Get/List/Create — a
	// field some paths compute and others skip reads as absent (#fc032545).
	assert.Equal(t, expectedScheduleURL("http://example.com", "acme", "growth", schedID), result.URL)
}

// TestRecurringHandler_GetByID_URLSkippedWhenProjectUnresolvable pins the
// decoration's failure semantics: an unresolvable project leaves url absent
// (omitempty) instead of failing a response whose data is otherwise valid.
func TestRecurringHandler_GetByID_URLSkippedWhenProjectUnresolvable(t *testing.T) {
	wsID, projID, schedID := uuid.New(), uuid.New(), uuid.New()
	h := NewRecurringHandler(
		&recurringSvcURLStub{
			getByID: &domain.RecurringSchedule{ID: schedID, WorkspaceID: wsID, ProjectID: projID},
		},
		&MockProjectService{
			GetByIDFunc: func(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
				return nil, assert.AnError
			},
		},
		&MockWorkspaceService{},
	)

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.SetParamNames("recurring_id")
	c.SetParamValues(schedID.String())

	require.NoError(t, h.GetByID(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
	assert.NotContains(t, decoded, "url", "unresolvable slugs must leave url absent, not fail the response")
	assert.Equal(t, schedID.String(), decoded["id"])
}
