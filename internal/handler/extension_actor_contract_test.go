package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// Contract tests for the routes of the extension actor itself, /api/v1/ext/me*.
// External extensions (examples/external-extension parses /ext/me) depend on
// these keys and status codes; see docs/extensions/extension-actor-contract.md.
// Run all of them with: go test ./internal/handler -run Contract

func extContractCall(t *testing.T, h echo.HandlerFunc, method string, p *service.ExtensionPrincipal) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/", http.NoBody)
	req.Header.Set("Authorization", "Bearer ext_presented_token")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	if p != nil {
		c.Set(middleware.ContextKeyExtensionPrincipal, p)
	}
	require.NoError(t, h(c))
	return rec
}

func extContractPrincipal() *service.ExtensionPrincipal {
	return &service.ExtensionPrincipal{
		ConnectionID: uuid.New(), WorkspaceID: uuid.New(), ActorID: uuid.New(), ExtensionID: "reference-ext",
		Capabilities: map[string][]string{"api": {"tasks:read", "comments:write"}, "events": {"task.created"}},
	}
}

func TestContract_ExtMe(t *testing.T) {
	now := time.Now()
	h := NewExtensionActorHandler(&fakeExtActorService{row: &domain.ExtensionActor{ViolationCount: 1, LastViolationAt: &now, LastViolation: "POST /x"}})
	p := extContractPrincipal()

	rec := extContractCall(t, h.Me, http.MethodGet, p)
	require.Equal(t, http.StatusOK, rec.Code)
	body := contractKeys(t, rec.Body.Bytes())
	requireContractKeys(t, body, "actor", "actor_id", "extension_id", "connection_id", "workspace_id", "capabilities", "violations")
	assert.JSONEq(t, `"ext:reference-ext@`+p.WorkspaceID.String()+`"`, string(body["actor"]), "actor is ext:<id>@<workspace>")
	assert.JSONEq(t, `{"api":["tasks:read","comments:write"],"events":["task.created"]}`, string(body["capabilities"]),
		"capabilities are the consented ones, grouped by kind")
	requireContractKeys(t, contractKeys(t, body["violations"]), "count", "last_at", "last")
	assert.NotContains(t, rec.Body.String(), "ext_presented_token", "the token is never echoed")

	// Any other principal: 403, never 401 (the request did authenticate).
	assert.Equal(t, http.StatusForbidden, extContractCall(t, h.Me, http.MethodGet, nil).Code)
}

func TestContract_ExtMeTokenRotate(t *testing.T) {
	h := NewExtensionActorHandler(&fakeExtActorService{})
	p := extContractPrincipal()

	rec := extContractCall(t, h.SelfRotate, http.MethodPost, p)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	requireContractKeys(t, contractKeys(t, rec.Body.Bytes()),
		"extension_token", "token_prefix", "token_length", "actor", "actor_id", "connection_id", "workspace_id",
		"previous_valid_for", "previous_valid_until")

	assert.Equal(t, http.StatusNoContent, extContractCall(t, h.SelfRevokePrevious, http.MethodDelete, p).Code)

	assert.Equal(t, http.StatusForbidden, extContractCall(t, h.SelfRotate, http.MethodPost, nil).Code)
	assert.Equal(t, http.StatusForbidden, extContractCall(t, h.SelfRevokePrevious, http.MethodDelete, nil).Code)
}
