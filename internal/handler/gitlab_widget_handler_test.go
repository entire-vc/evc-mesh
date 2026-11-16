package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

type fakeWidgetAvailability struct {
	on  bool
	err error
}

func (f *fakeWidgetAvailability) WidgetsAvailable(context.Context, uuid.UUID, string) (bool, error) {
	return f.on, f.err
}

type fakeWidgetProjects []uuid.UUID

func (f fakeWidgetProjects) WidgetProjects(context.Context, uuid.UUID, string, uuid.UUID) ([]uuid.UUID, error) {
	return f, nil
}

type fakeFailedPipelines struct {
	gotProjects []uuid.UUID
	gotUser     uuid.UUID
	gotSince    time.Time
}

func (f *fakeFailedPipelines) FailedMergeRequestPipelines(_ context.Context, _, user uuid.UUID, projects []uuid.UUID, since time.Time, _ int) ([]postgres.FailedPipeline, error) {
	f.gotProjects, f.gotUser, f.gotSince = projects, user, since
	out := []postgres.FailedPipeline{}
	for _, p := range projects {
		out = append(out, postgres.FailedPipeline{ProjectID: p, ExternalID: "7"})
	}
	return out, nil
}

func TestGitLabWidgetHandler_FailedPipelines(t *testing.T) {
	ws, user := uuid.New(), uuid.New()
	p1, p2, foreign := uuid.New(), uuid.New(), uuid.New()
	avail := &fakeWidgetAvailability{on: true}
	links := &fakeFailedPipelines{}
	h := NewGitLabWidgetHandler(avail, fakeWidgetProjects{p1, p2}, links)
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }

	wsParam := ws.String()
	call := func(query string, signedIn bool) *httptest.ResponseRecorder {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/?"+query, http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("ws_id")
		c.SetParamValues(wsParam)
		if signedIn {
			c.Set("user_id", user)
		}
		require.NoError(t, h.FailedPipelines(c))
		return rec
	}

	rec := call("", true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Since time.Time                 `json:"since"`
		Items []postgres.FailedPipeline `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Items, 2, "every bound project the caller may open")
	require.Equal(t, []uuid.UUID{p1, p2}, links.gotProjects)
	require.Equal(t, user, links.gotUser, "the caller's own tasks")
	require.Equal(t, now.Add(-24*time.Hour), links.gotSince)

	rec = call("project_id="+p2.String(), true)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []uuid.UUID{p2}, links.gotProjects, "project_id narrows to one project")

	rec = call("project_id="+foreign.String(), true)
	require.Equal(t, http.StatusNotFound, rec.Code, "a project the caller may not use is not found")

	rec = call("project_id=nope", true)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = call("", false)
	require.Equal(t, http.StatusForbidden, rec.Code, "a person only")

	avail.on = false
	rec = call("", true)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "widget_unavailable")

	avail.on, avail.err = true, errors.New("registry down")
	rec = call("", true)
	require.Equal(t, http.StatusInternalServerError, rec.Code, "an unreadable extension state is an error, not an empty widget")

	avail.err = nil
	wsParam = "not-a-uuid"
	rec = call("", true)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
