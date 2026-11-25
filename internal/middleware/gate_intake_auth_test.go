package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

type fakeGateIntakeAuth struct {
	valid string
	err   error
}

func (f fakeGateIntakeAuth) AuthenticateGateIntake(_ context.Context, token string) (*domain.GateIntakePrincipal, error) {
	if f.err != nil {
		return nil, f.err
	}
	if token == f.valid {
		return &domain.GateIntakePrincipal{ID: uuid.New(), Name: "p"}, nil
	}
	return nil, domain.ErrGateIntakeUnauthorized
}

func gateIntakeTestServer(auth GateIntakeAuthenticator, reached *int) *echo.Echo {
	e := echo.New()
	e.Use(GateIntakeGuard(auth))
	h := func(c echo.Context) error { *reached++; return c.NoContent(http.StatusNoContent) }
	e.POST(GateIntakeRoutePath, h, RequireGateIntakePrincipal())
	e.GET("/api/v1/tasks/:id", h)
	e.PATCH("/api/v1/tasks/:id", h)
	return e
}

func TestGateIntakeGuard(t *testing.T) {
	valid, _ := domain.NewGateIntakeToken()
	other, _ := domain.NewGateIntakeToken()
	cases := []struct {
		name, method, path, authz string
		want                      int
		reaches                   bool
	}{
		{"valid on its route", http.MethodPost, GateIntakeRoutePath, "Bearer " + valid, http.StatusNoContent, true},
		{"lowercase scheme", http.MethodPost, GateIntakeRoutePath, "bearer " + valid, http.StatusNoContent, true},
		{"valid on a read", http.MethodGet, "/api/v1/tasks/x", "Bearer " + valid, http.StatusForbidden, false},
		{"valid on a write", http.MethodPatch, "/api/v1/tasks/x", "Bearer " + valid, http.StatusForbidden, false},
		{"valid on an unknown route", http.MethodPost, "/api/v1/nope", "Bearer " + valid, http.StatusForbidden, false},
		{"valid, wrong method", http.MethodPut, GateIntakeRoutePath, "Bearer " + valid, http.StatusForbidden, false},
		{"invalid on its route", http.MethodPost, GateIntakeRoutePath, "Bearer " + other, http.StatusUnauthorized, false},
		{"invalid elsewhere", http.MethodGet, "/api/v1/tasks/x", "Bearer " + other, http.StatusUnauthorized, false},
		{"none on its route", http.MethodPost, GateIntakeRoutePath, "", http.StatusUnauthorized, false},
		{"other bearer on its route", http.MethodPost, GateIntakeRoutePath, "Bearer eyJhbGciOi.x.y", http.StatusUnauthorized, false},
		{"other bearer elsewhere passes through", http.MethodGet, "/api/v1/tasks/x", "Bearer eyJhbGciOi.x.y", http.StatusNoContent, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := 0
			e := gateIntakeTestServer(fakeGateIntakeAuth{valid: valid}, &reached)
			req := httptest.NewRequest(tc.method, tc.path, http.NoBody)
			if tc.authz != "" {
				req.Header.Set("Authorization", tc.authz)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
			assert.Equal(t, tc.reaches, reached == 1)
		})
	}
}

func TestGateIntakeGuard_StoreFailureIsNot401(t *testing.T) {
	valid, _ := domain.NewGateIntakeToken()
	reached := 0
	e := gateIntakeTestServer(fakeGateIntakeAuth{err: errors.New("db down")}, &reached)
	req := httptest.NewRequest(http.MethodPost, GateIntakeRoutePath, http.NoBody)
	req.Header.Set("Authorization", "Bearer "+valid)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "a storage failure is retryable, not a verdict on the key")
	assert.Zero(t, reached)
}
