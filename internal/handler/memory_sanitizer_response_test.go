package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// AC2 of task #f78232c4: the sanitizer's refusal must reach the CALLER, not
// only a server log. The service returns *apierror.Error; this pins the rest of
// that chain — that handleError serialises the `validation` map into the 400
// body, which is the field evc-mesh-mcp's rest_client.apiErrorMessage flattens
// into the text an agent reads. Asserting the service return alone would not
// distinguish "refused with a reason the caller can read" from "refused with a
// bare 400", and it is the second that makes the guard useless in practice.
func TestRemember_SanitizerRefusalReachesTheCaller(t *testing.T) {
	ws := uuid.New()

	// Stand in for the real service's refusal, in exactly the shape
	// memoryService.Remember produces on a sanitizer violation.
	const reason = "secret-assignment: content assigns a literal value to CASDOOR_CLIENT_SECRET — " +
		"record where the secret lives (env file, secret manager) instead of its value; memory was not written"
	ms := &MockMemoryService{
		RememberFunc: func(_ context.Context, _ *domain.Memory) (service.RememberResult, error) {
			return service.RememberResult{}, apierror.ValidationError(map[string]string{"content": reason})
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	body := fmt.Sprintf(`{"workspace_id":%q,"key":"leaky-note","content":"see log","scope":"workspace"}`, ws)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ws)

	if err := h.Remember(c); err != nil {
		t.Fatalf("Remember returned a transport error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400", rec.Code)
	}

	var got struct {
		Message    string            `json:"message"`
		Validation map[string]string `json:"validation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response body is not JSON: %v (%s)", err, rec.Body.String())
	}

	// The generic message alone is not enough — "Validation failed" tells the
	// caller nothing about which rule fired or what to change.
	if got.Validation["content"] != reason {
		t.Errorf("the refusal reason did not survive serialisation\n  got:  %q\n  want: %q",
			got.Validation["content"], reason)
	}
	if !strings.Contains(rec.Body.String(), "memory was not written") {
		t.Errorf("response must tell the caller the write did not happen; body was: %s", rec.Body.String())
	}
}
