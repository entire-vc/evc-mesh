package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Slack integration was removed (#c9f9834f). A client still sending the
// old provider must get a 400 and nothing may be persisted.
func TestIntegrationConfigure_RemovedProvider_Rejected(t *testing.T) {
	svc := newFakeIntegrationService()
	e := newIntegrationTestServer(svc, nil)
	wsID := uuid.New()

	rec := doJSON(t, e, http.MethodPost, "/api/v1/workspaces/"+wsID.String()+"/integrations",
		`{"provider":"slack","config":{"webhook_url":"https://example.com/hook"},"is_active":true}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "unsupported provider")
	assert.Empty(t, svc.byID, "a rejected Configure must not persist anything")
}
