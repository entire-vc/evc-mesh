package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

type fakeExtRefSvc struct {
	got *domain.TaskExternalRef
	err error
}

func (f *fakeExtRefSvc) Get(context.Context, uuid.UUID) (*domain.TaskExternalRef, error) {
	return f.got, nil
}

func (f *fakeExtRefSvc) Put(_ context.Context, id uuid.UUID, ref, hash string, _ *uuid.UUID) (*domain.TaskExternalRef, error) {
	return f.got, f.err
}

func callExtRef(t *testing.T, h echo.HandlerFunc, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("task_id")
	c.SetParamValues(uuid.NewString())
	require.NoError(t, h(c))
	return rec
}

func TestTaskExternalRefHandler_Statuses(t *testing.T) {
	bound := &domain.TaskExternalRef{Ref: "r", Hash: "h"}
	cases := []struct {
		name   string
		svc    *fakeExtRefSvc
		method string
		want   int
	}{
		{"get unbound is 404", &fakeExtRefSvc{}, http.MethodGet, http.StatusNotFound},
		{"get bound is 200", &fakeExtRefSvc{got: bound}, http.MethodGet, http.StatusOK},
		{"put ok", &fakeExtRefSvc{got: bound}, http.MethodPut, http.StatusOK},
		{"put invalid is 422", &fakeExtRefSvc{err: service.ErrExternalRefInvalid}, http.MethodPut, http.StatusUnprocessableEntity},
		{"put changed hash is 409", &fakeExtRefSvc{got: bound, err: service.ErrExternalHashConflict}, http.MethodPut, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewTaskExternalRefHandler(tc.svc, nil)
			fn := h.Get
			if tc.method == http.MethodPut {
				fn = h.Put
			}
			rec := callExtRef(t, fn, tc.method, `{"external_ref":"r","external_hash":"h"}`)
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}
