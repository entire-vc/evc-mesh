package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// mcp is a reference-only connection card (#4a3195a5) — no handler or
// service ever read a stored row back for behavior, so is_active was a
// switch that switched nothing. Configure/Update now reject it outright
// instead of silently storing a row nobody consults.
// ---------------------------------------------------------------------------

func TestIntegrationConfigure_MCPProvider_Rejected(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"mcp","config":{},"is_active":true}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "reference-only")

	// Negative-control-of-the-negative-control: confirm the rejection didn't
	// happen to also skip creating the row (a silent-no-op would be worse
	// than the original bug — an unreadable AND uncreatable row is fine, but
	// a REJECTED-yet-created row would be a lie in the response).
	assert.Empty(t, svc.byID, "a rejected Configure must not persist anything")
}

func TestIntegrationConfigure_OtherProvidersStillWork_MCPRejectionIsScoped(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"spark","config":{},"is_active":true}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}
