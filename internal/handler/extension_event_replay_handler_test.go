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

	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type fakeEventReplayer struct {
	gotQuery    service.ExtensionEventsQuery
	gotDelivery uuid.UUID
	gotConn     uuid.UUID
	err         error
}

func (f *fakeEventReplayer) ListExtensionEvents(_ context.Context, p *service.ExtensionPrincipal, q service.ExtensionEventsQuery) (*service.ExtensionEventPage, error) {
	f.gotConn, f.gotQuery = p.ConnectionID, q
	if f.err != nil {
		return nil, f.err
	}
	return &service.ExtensionEventPage{Items: []service.ExtensionEvent{}, NextCursor: "c1"}, nil
}

func (f *fakeEventReplayer) RedeliverExtensionEvent(_ context.Context, p *service.ExtensionPrincipal, id uuid.UUID) (*service.ExtensionRedelivery, error) {
	f.gotConn, f.gotDelivery = p.ConnectionID, id
	if f.err != nil {
		return nil, f.err
	}
	return &service.ExtensionRedelivery{DeliveryID: id, Success: true}, nil
}

func serveReplay(t *testing.T, h *ExtensionEventReplayHandler, method, target string, p *service.ExtensionPrincipal) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	e.GET("/ext/me/events", h.List, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if p != nil {
				c.Set(middleware.ContextKeyExtensionPrincipal, p)
			}
			return next(c)
		}
	})
	e.POST("/ext/me/events/:delivery_id/redeliver", h.Redeliver, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if p != nil {
				c.Set(middleware.ContextKeyExtensionPrincipal, p)
			}
			return next(c)
		}
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(method, target, http.NoBody))
	return rec
}

func TestExtensionEventReplayHandler_List(t *testing.T) {
	p := &service.ExtensionPrincipal{ConnectionID: uuid.New()}

	t.Run("only an extension actor", func(t *testing.T) {
		f := &fakeEventReplayer{}
		rec := serveReplay(t, NewExtensionEventReplayHandler(f), http.MethodGet, "/ext/me/events", nil)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, uuid.Nil, f.gotConn, "the service was not called")
	})

	t.Run("no replayer is 503", func(t *testing.T) {
		rec := serveReplay(t, NewExtensionEventReplayHandler(nil), http.MethodGet, "/ext/me/events", p)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	for name, target := range map[string]string{
		"bad since":       "/ext/me/events?since=yesterday",
		"bad limit":       "/ext/me/events?limit=0",
		"bad undelivered": "/ext/me/events?undelivered=maybe",
	} {
		t.Run(name, func(t *testing.T) {
			rec := serveReplay(t, NewExtensionEventReplayHandler(&fakeEventReplayer{}), http.MethodGet, target, p)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}

	t.Run("query reaches the service for the caller's connection", func(t *testing.T) {
		f := &fakeEventReplayer{}
		rec := serveReplay(t, NewExtensionEventReplayHandler(f), http.MethodGet, "/ext/me/events?since=2026-10-29T18:00:00Z&limit=7&undelivered=true", p)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, p.ConnectionID, f.gotConn)
		assert.Equal(t, 7, f.gotQuery.Limit)
		assert.True(t, f.gotQuery.Undelivered)
		assert.Equal(t, 2026, f.gotQuery.After.CreatedAt.Year())
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, "c1", body["next_cursor"])
	})

	t.Run("service error keeps its status", func(t *testing.T) {
		f := &fakeEventReplayer{err: apierror.ServiceUnavailable("down")}
		rec := serveReplay(t, NewExtensionEventReplayHandler(f), http.MethodGet, "/ext/me/events", p)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
}

func TestExtensionEventReplayHandler_Redeliver(t *testing.T) {
	p := &service.ExtensionPrincipal{ConnectionID: uuid.New()}
	id := uuid.New()
	target := "/ext/me/events/" + id.String() + "/redeliver"

	rec := serveReplay(t, NewExtensionEventReplayHandler(&fakeEventReplayer{}), http.MethodPost, target, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = serveReplay(t, NewExtensionEventReplayHandler(nil), http.MethodPost, target, p)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	rec = serveReplay(t, NewExtensionEventReplayHandler(&fakeEventReplayer{}), http.MethodPost, "/ext/me/events/not-a-uuid/redeliver", p)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	f := &fakeEventReplayer{}
	rec = serveReplay(t, NewExtensionEventReplayHandler(f), http.MethodPost, target, p)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, id, f.gotDelivery)
	assert.Equal(t, p.ConnectionID, f.gotConn)

	rec = serveReplay(t, NewExtensionEventReplayHandler(&fakeEventReplayer{err: apierror.NotFound("Event delivery")}), http.MethodPost, target, p)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
