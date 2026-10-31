package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// A spawner key lookup that fails for a reason other than a denial (DB down,
// canceled context) is not a revocation: the spawner gets a retryable 503,
// not 401. A denial, or no agent without an error, stays 401.
func TestSpawnerAuth_BackendErrorIs503_DenialIs401(t *testing.T) {
	t.Setenv(EnvSpawnerAgentIDs, "2b7f0a4e-6c1d-4f43-9e55-0d3b8a1c7e90")
	run := func(t *testing.T, agent *domain.Agent, authErr error) (*httptest.ResponseRecorder, bool) {
		t.Helper()
		svc := &mockAgentService{
			AuthenticateFunc: func(_ context.Context, _, _ string) (*domain.Agent, error) {
				return agent, authErr
			},
		}
		req := httptest.NewRequest(http.MethodPost, "/internal/secrets/materialize", http.NoBody)
		req.Header.Set("X-Agent-Key", "agk_my-workspace_abcdefghijklmnopqrstuvwxyz123456")
		rec := httptest.NewRecorder()
		reached := false
		err := SpawnerAuth(svc)(func(c echo.Context) error {
			reached = true
			return c.NoContent(http.StatusOK)
		})(echo.New().NewContext(req, rec))
		require.NoError(t, err)
		return rec, reached
	}
	decode := func(t *testing.T, rec *httptest.ResponseRecorder) apierror.Error {
		t.Helper()
		var body apierror.Error
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body
	}

	for name, backendErr := range map[string]error{
		"conn refused":     errors.New("conn refused"),
		"wrapped canceled": fmt.Errorf("get workspace: %w", context.Canceled),
	} {
		t.Run(name, func(t *testing.T) {
			rec, reached := run(t, nil, backendErr)
			assert.False(t, reached)
			require.Equal(t, http.StatusServiceUnavailable, rec.Code)
			assert.Equal(t, "2", rec.Header().Get("Retry-After"))
			assert.Equal(t, "authentication backend unavailable", decode(t, rec).Message)
		})
	}

	for name, authErr := range map[string]error{
		"denial":          apierror.Unauthorized("invalid API key"),
		"no agent no err": nil,
	} {
		t.Run(name, func(t *testing.T) {
			rec, reached := run(t, nil, authErr)
			assert.False(t, reached)
			require.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Empty(t, rec.Header().Get("Retry-After"))
			assert.Equal(t, "Invalid agent API key", decode(t, rec).Message)
		})
	}
}
