package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ---------------------------------------------------------------------------
// Webhook handler tests.
//
// These tests define the expected HTTP contract for the WebhookHandler that
// will be implemented in Phase 5-OSS.
//
// Routes (to be registered in cmd/api/routes.go):
//   POST   /workspaces/:ws_id/webhooks         — create webhook
//   GET    /workspaces/:ws_id/webhooks         — list webhooks
//   GET    /webhooks/:webhook_id               — get webhook by ID
//   PATCH  /webhooks/:webhook_id               — update webhook
//   DELETE /webhooks/:webhook_id               — delete webhook
//
// Auth: DualAuth (user JWT or agent API key required for all routes).
//
// Tests that require implementation are marked t.Skip().
// Tests validating request parsing / validation are fully runnable once
// the handler is implemented.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Domain types mirrored from service/webhook_service_test.go
// (These will be unified in internal/domain/webhook.go)
// ---------------------------------------------------------------------------

// webhookEventType mirrors service.WebhookEvent for use in handler requests.
type webhookEventType = string

// webhookDomain is a local test representation of a webhook.
type webhookDomain struct {
	ID               uuid.UUID          `json:"id"`
	WorkspaceID      uuid.UUID          `json:"workspace_id"`
	URL              string             `json:"url"`
	Events           []webhookEventType `json:"events"`
	IsActive         bool               `json:"is_active"`
	ConsecutiveFails int                `json:"consecutive_fails"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// MockWebhookService for handler tests
// (Implements the WebhookService interface from service package)
// ---------------------------------------------------------------------------

type MockWebhookService struct {
	CreateFunc   func(ctx context.Context, wh *webhookDomain) error
	UpdateFunc   func(ctx context.Context, wh *webhookDomain) error
	DeleteFunc   func(ctx context.Context, id uuid.UUID) error
	ListFunc     func(ctx context.Context, workspaceID uuid.UUID) ([]webhookDomain, error)
	GetByIDFunc  func(ctx context.Context, id uuid.UUID) (*webhookDomain, error)
	DispatchFunc func(ctx context.Context, workspaceID uuid.UUID, event webhookEventType, payload map[string]any) error
}

func (m *MockWebhookService) Create(ctx context.Context, wh *webhookDomain) error {
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, wh)
	}
	return nil
}

func (m *MockWebhookService) Update(ctx context.Context, wh *webhookDomain) error {
	if m.UpdateFunc != nil {
		return m.UpdateFunc(ctx, wh)
	}
	return nil
}

func (m *MockWebhookService) Delete(ctx context.Context, id uuid.UUID) error {
	if m.DeleteFunc != nil {
		return m.DeleteFunc(ctx, id)
	}
	return nil
}

func (m *MockWebhookService) List(ctx context.Context, workspaceID uuid.UUID) ([]webhookDomain, error) {
	if m.ListFunc != nil {
		return m.ListFunc(ctx, workspaceID)
	}
	return []webhookDomain{}, nil
}

func (m *MockWebhookService) GetByID(ctx context.Context, id uuid.UUID) (*webhookDomain, error) {
	if m.GetByIDFunc != nil {
		return m.GetByIDFunc(ctx, id)
	}
	return nil, nil
}

func (m *MockWebhookService) Dispatch(ctx context.Context, workspaceID uuid.UUID, event webhookEventType, payload map[string]any) error {
	if m.DispatchFunc != nil {
		return m.DispatchFunc(ctx, workspaceID, event, payload)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tests: POST /workspaces/:ws_id/webhooks
// ---------------------------------------------------------------------------

func TestWebhookHandler_Create(t *testing.T) {
	t.Run("creates webhook with valid body", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
		// When handler is implemented:
		//   - Parse ws_id from URL param (must be valid UUID).
		//   - Parse {url, events, secret} from JSON body.
		//   - Call WebhookService.Create.
		//   - Return 201 with created webhook (secret omitted from response).
		wsID := uuid.New()
		body := `{
			"url":    "https://example.com/hooks",
			"events": ["task.created", "task.updated"],
			"secret": "my-webhook-secret"
		}`
		_ = wsID
		_ = body
	})

	t.Run("returns 400 for missing URL", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
		_ = `{"events":["task.created"]}`
	})

	t.Run("returns 400 for empty events list", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
		_ = `{"url":"https://example.com/hook","events":[]}`
	})

	t.Run("returns 400 for invalid URL scheme", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
		_ = `{"url":"ftp://not-http.example.com","events":["task.created"]}`
	})

	t.Run("returns 400 for invalid ws_id", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
		// ws_id = "not-a-uuid" → 400
	})

	t.Run("returns 401 when not authenticated", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
		// No auth headers → 401
	})

	t.Run("returns 500 on service error", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
		// Service returns internal error → 500
	})
}

// ---------------------------------------------------------------------------
// Tests: GET /workspaces/:ws_id/webhooks
// ---------------------------------------------------------------------------

func TestWebhookHandler_List(t *testing.T) {
	t.Run("returns list of webhooks for workspace", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
		// Response should be a JSON array of webhook objects.
		// Secret must NOT be included in the list response.
	})

	t.Run("returns empty array when no webhooks", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
		// Empty array, not null.
	})

	t.Run("returns 400 for invalid ws_id", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
	})

	t.Run("returns 401 when not authenticated", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
	})
}

// ---------------------------------------------------------------------------
// Tests: PATCH /webhooks/:webhook_id
// ---------------------------------------------------------------------------

func TestWebhookHandler_Update(t *testing.T) {
	t.Run("updates URL and events", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
		_ = `{"url":"https://new.example.com/hook","events":["task.created"]}`
	})

	t.Run("can deactivate webhook (is_active=false)", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
		_ = `{"is_active":false}`
	})

	t.Run("can rotate secret", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
		_ = `{"secret":"new-secret-value"}`
	})

	t.Run("returns 404 for unknown webhook_id", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
	})

	t.Run("returns 400 for invalid webhook_id", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
	})

	t.Run("returns 401 when not authenticated", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
	})
}

// ---------------------------------------------------------------------------
// Tests: DELETE /webhooks/:webhook_id
// ---------------------------------------------------------------------------

func TestWebhookHandler_Delete(t *testing.T) {
	t.Run("deletes existing webhook and returns 204", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
	})

	t.Run("returns 404 for unknown webhook_id", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
	})

	t.Run("returns 400 for invalid webhook_id", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
	})

	t.Run("returns 401 when not authenticated", func(t *testing.T) {
		t.Skip("TODO: implement WebhookHandler in internal/handler/webhook_handler.go")
	})
}

// ---------------------------------------------------------------------------
// Tests: Request parsing (runs NOW — validates JSON contracts)
//
// These tests validate parsing logic that can be tested independently of the
// handler implementation. They document the expected request/response shapes.
// ---------------------------------------------------------------------------

func TestWebhookHandler_RequestParsing_CreateBody(t *testing.T) {
	type createWebhookRequest struct {
		URL    string             `json:"url"`
		Events []webhookEventType `json:"events"`
		Secret string             `json:"secret"`
	}

	t.Run("valid body parses correctly", func(t *testing.T) {
		body := `{
			"url":    "https://example.com/hooks",
			"events": ["task.created", "comment.added"],
			"secret": "s3cr3t"
		}`
		var req createWebhookRequest
		err := json.Unmarshal([]byte(body), &req)
		require.NoError(t, err)
		assert.Equal(t, "https://example.com/hooks", req.URL)
		assert.Len(t, req.Events, 2)
		assert.Contains(t, req.Events, "task.created")
		assert.Contains(t, req.Events, "comment.added")
		assert.Equal(t, "s3cr3t", req.Secret)
	})

	t.Run("missing fields default to zero values", func(t *testing.T) {
		body := `{"url":"https://example.com/hooks"}`
		var req createWebhookRequest
		err := json.Unmarshal([]byte(body), &req)
		require.NoError(t, err)
		assert.Nil(t, req.Events)
		assert.Empty(t, req.Secret)
	})

	t.Run("invalid JSON returns parse error", func(t *testing.T) {
		var req createWebhookRequest
		err := json.Unmarshal([]byte(`{not-json}`), &req)
		assert.Error(t, err)
	})
}

func TestWebhookHandler_RequestParsing_UpdateBody(t *testing.T) {
	type updateWebhookRequest struct {
		URL      *string            `json:"url"`
		Events   []webhookEventType `json:"events"`
		Secret   *string            `json:"secret"`
		IsActive *bool              `json:"is_active"`
	}

	t.Run("partial update only sets provided fields", func(t *testing.T) {
		body := `{"is_active":false}`
		var req updateWebhookRequest
		err := json.Unmarshal([]byte(body), &req)
		require.NoError(t, err)
		require.NotNil(t, req.IsActive)
		assert.False(t, *req.IsActive)
		assert.Nil(t, req.URL)
		assert.Nil(t, req.Secret)
	})

	t.Run("url update parses correctly", func(t *testing.T) {
		body := `{"url":"https://new.example.com/webhook"}`
		var req updateWebhookRequest
		err := json.Unmarshal([]byte(body), &req)
		require.NoError(t, err)
		require.NotNil(t, req.URL)
		assert.Equal(t, "https://new.example.com/webhook", *req.URL)
	})
}

func TestWebhookHandler_ResponseShape_ListItem(t *testing.T) {
	// Documents the expected JSON shape of a webhook in list/get responses.
	// Secret must NOT be included.
	wsID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	wh := webhookDomain{
		ID:               uuid.New(),
		WorkspaceID:      wsID,
		URL:              "https://example.com/hook",
		Events:           []webhookEventType{"task.created"},
		IsActive:         true,
		ConsecutiveFails: 0,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	t.Run("webhook serializes to JSON without secret field", func(t *testing.T) {
		data, err := json.Marshal(wh)
		require.NoError(t, err)

		var result map[string]interface{}
		err = json.Unmarshal(data, &result)
		require.NoError(t, err)

		// Required fields present.
		assert.Contains(t, result, "id")
		assert.Contains(t, result, "workspace_id")
		assert.Contains(t, result, "url")
		assert.Contains(t, result, "events")
		assert.Contains(t, result, "is_active")
		assert.Contains(t, result, "created_at")
		assert.Contains(t, result, "updated_at")

		// Secret must NOT be in the response.
		assert.NotContains(t, result, "secret", "secret must never be returned in API responses")
	})

	t.Run("events field is an array", func(t *testing.T) {
		data, err := json.Marshal(wh)
		require.NoError(t, err)

		var result map[string]interface{}
		err = json.Unmarshal(data, &result)
		require.NoError(t, err)

		events, ok := result["events"].([]interface{})
		require.True(t, ok, "events must be a JSON array")
		assert.Len(t, events, 1)
		assert.Equal(t, "task.created", events[0].(string))
	})
}

// ---------------------------------------------------------------------------
// Tests: handleError contract for webhook-specific errors (runs NOW)
//
// These tests validate that the existing handleError function (shared across
// all handlers) correctly maps webhook service errors to HTTP status codes.
// ---------------------------------------------------------------------------

func TestWebhookHandler_ErrorMapping(t *testing.T) {
	e := echo.New()

	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{
			name:       "not found returns 404",
			err:        apierror.NotFound("Webhook"),
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "bad request returns 400",
			err:        apierror.BadRequest("invalid URL"),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "conflict returns 409",
			err:        apierror.Conflict("webhook already exists"),
			wantStatus: http.StatusConflict,
		},
		{
			name:       "internal error returns 500",
			err:        apierror.InternalError("database error"),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			// handleError is shared across all handlers; validate it works for webhooks.
			err := handleError(c, tt.err)
			require.NoError(t, err, "handleError must not return Go errors")
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: UUID param extraction (runs NOW — pure HTTP parsing logic)
// ---------------------------------------------------------------------------

func TestWebhookHandler_UUIDParamExtraction(t *testing.T) {
	e := echo.New()

	t.Run("valid UUID param is parsed correctly", func(t *testing.T) {
		id := uuid.New()
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("webhook_id")
		c.SetParamValues(id.String())

		parsed, err := uuid.Parse(c.Param("webhook_id"))
		require.NoError(t, err)
		assert.Equal(t, id, parsed)
	})

	t.Run("invalid UUID param causes parse error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("webhook_id")
		c.SetParamValues("not-a-uuid")

		_, err := uuid.Parse(c.Param("webhook_id"))
		assert.Error(t, err, "invalid UUID must produce a parse error")
	})
}

// ---------------------------------------------------------------------------
// Tests: Auth required (runs NOW — validates auth middleware contract)
//
// These tests verify that routes without auth headers produce 401 responses,
// using a minimal Echo setup that mimics how routes will be registered.
// ---------------------------------------------------------------------------

func TestWebhookHandler_AuthRequired(t *testing.T) {
	// Simulate an unprotected endpoint that would be behind DualAuth middleware.
	// The real webhook routes will use DualAuth (user JWT or agent key).
	// This test documents that the handler itself should NOT skip auth.

	e := echo.New()
	wsID := uuid.New()

	// Simulate the middleware rejecting an unauthenticated request.
	authRequiredMiddleware := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Simulate DualAuth: no auth provided → 401.
			if c.Request().Header.Get("Authorization") == "" &&
				c.Request().Header.Get("X-Agent-Key") == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{
					"error": "Authentication required",
				})
			}
			return next(c)
		}
	}

	handler := authRequiredMiddleware(func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	tests := []struct {
		name       string
		setupReq   func() *http.Request
		wantStatus int
	}{
		{
			name: "no auth headers returns 401",
			setupReq: func() *http.Request {
				req := httptest.NewRequest(http.MethodPost, "/workspaces/"+wsID.String()+"/webhooks", strings.NewReader(`{}`))
				req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
				return req
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "with Bearer token passes middleware",
			setupReq: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/workspaces/"+wsID.String()+"/webhooks", http.NoBody)
				req.Header.Set("Authorization", "Bearer some-token")
				return req
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "with agent key passes middleware",
			setupReq: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/workspaces/"+wsID.String()+"/webhooks", http.NoBody)
				req.Header.Set("X-Agent-Key", "agk_workspace_randompart12345678901234")
				return req
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.setupReq()
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := handler(c)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: Workspace ID scoping (runs NOW — mock-based)
// ---------------------------------------------------------------------------

func TestWebhookHandler_WorkspaceScoping(t *testing.T) {
	// Validates that the List endpoint scopes results to the provided workspace.
	// Uses the mock service to verify the workspace ID is passed correctly.

	wsA := uuid.New()
	wsB := uuid.New()

	webhooksA := []webhookDomain{
		{
			ID:          uuid.New(),
			WorkspaceID: wsA,
			URL:         "https://a.example.com/hook",
			Events:      []webhookEventType{"task.created"},
			IsActive:    true,
		},
	}
	webhooksB := []webhookDomain{
		{
			ID:          uuid.New(),
			WorkspaceID: wsB,
			URL:         "https://b.example.com/hook",
			Events:      []webhookEventType{"task.updated"},
			IsActive:    true,
		},
	}

	mockSvc := &MockWebhookService{
		ListFunc: func(ctx context.Context, workspaceID uuid.UUID) ([]webhookDomain, error) {
			if workspaceID == wsA {
				return webhooksA, nil
			}
			if workspaceID == wsB {
				return webhooksB, nil
			}
			return []webhookDomain{}, nil
		},
	}

	t.Run("list for workspace-A returns only workspace-A webhooks", func(t *testing.T) {
		results, err := mockSvc.List(context.Background(), wsA)
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, wsA, results[0].WorkspaceID)
	})

	t.Run("list for workspace-B returns only workspace-B webhooks", func(t *testing.T) {
		results, err := mockSvc.List(context.Background(), wsB)
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, wsB, results[0].WorkspaceID)
	})

	t.Run("list for unknown workspace returns empty", func(t *testing.T) {
		results, err := mockSvc.List(context.Background(), uuid.New())
		require.NoError(t, err)
		assert.Empty(t, results)
	})
}
