package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

// The body checks run before the repository is touched, so a handler with a nil
// repository proves every case below is refused at the HTTP boundary.
func TestRuntimeHandlerRefusesMalformedBodiesBeforeRepository(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	h := NewRuntimeHandler(nil)
	e.PUT("/workspaces/:ws_id/runtime", h.Save)
	e.POST("/workspaces/:ws_id/runtime/controllers/:controller_ref/report", h.Report)
	e.GET("/workspaces/:ws_id/runtime/controllers/:controller_ref/desired", h.Desired)
	e.PUT("/workspaces/:ws_id/runtime/bindings/:binding_id/admission", h.Admit)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/preview", h.Preview)
	e.POST("/workspaces/:ws_id/runtime/artifacts/:provenance_artifact_id/provenance", h.Provenance)
	e.GET("/workspaces/:ws_id/runtime/capacity", h.Capacity)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations", h.AcquireReservation)
	e.GET("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id", h.GetReservation)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id/consume", h.ConsumeReservation)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id/release", h.ReleaseReservation)

	ws := uuid.New().String()
	owner := "?resource_owner_workspace_id=" + uuid.New().String()
	rid := uuid.New().String()
	reservation := "/workspaces/" + ws + "/runtime/bindings/b/reservations/" + rid
	reservations := "/workspaces/" + ws + "/runtime/bindings/b/reservations" + owner
	acquire := `{"idempotency_key":"k","profile_id":"p","task_id":"` + uuid.New().String() + `","checkout_generation":1,"checkout_request_id":null,"worker_ref":"w",` +
		`"expected_catalog_revision":1,"expected_catalog_digest":"d","expected_admission_revision":1,"expected_profile_revision":"rev-1","expected_pool_set_digest":"p","ttl_seconds":60}`
	cases := []struct {
		name, method, path, body string
	}{
		{"save missing if_revision", http.MethodPut, "/workspaces/" + ws + "/runtime", `{"enabled":true,"config":{}}`},
		{"save unknown secret field", http.MethodPut, "/workspaces/" + ws + "/runtime", `{"if_revision":0,"enabled":true,"config":{},"api_key":"x"}`},
		{"save invalid workspace", http.MethodPut, "/workspaces/not-a-uuid/runtime", `{"if_revision":0,"enabled":true,"config":{}}`},
		{"report missing pools", http.MethodPost, "/workspaces/" + ws + "/runtime/controllers/runner-a/report", `{"schema_version":2,"revision":1,"digest":"d","status":"applied","capabilities":[],"emergency_paused":false}`},
		{"desired invalid workspace", http.MethodGet, "/workspaces/not-a-uuid/runtime/controllers/runner-a/desired", ``},
		{"admission without owner", http.MethodPut, "/workspaces/" + ws + "/runtime/bindings/b/admission", `{"if_revision":0,"enabled":true,"permitted_profiles":[]}`},
		{"admission missing profiles", http.MethodPut, "/workspaces/" + ws + "/runtime/bindings/b/admission" + owner, `{"if_revision":0,"enabled":true}`},
		{"preview forged qa flag", http.MethodPost, "/workspaces/" + ws + "/runtime/bindings/b/preview" + owner, `{"purpose":"new_launch","required_capabilities":[],"qa_allowed":true}`},
		{"provenance invalid artifact", http.MethodPost, "/workspaces/" + ws + "/runtime/artifacts/nope/provenance", `{"controller_ref":"c","artifact_revision":"s","complete":true,"authors":[]}`},
		{"provenance artifact in body", http.MethodPost, "/workspaces/" + ws + "/runtime/artifacts/" + uuid.New().String() + "/provenance", `{"controller_ref":"c","artifact_id":"` + uuid.New().String() + `","artifact_revision":"s","complete":true,"authors":[]}`},
		{"preview missing capabilities", http.MethodPost, "/workspaces/" + ws + "/runtime/bindings/b/preview" + owner, `{"purpose":"new_launch"}`},

		{"capacity invalid workspace", http.MethodGet, "/workspaces/not-a-uuid/runtime/capacity", ""},
		{"capacity invalid agent", http.MethodGet, "/workspaces/" + ws + "/runtime/capacity?agent_id=nope", ""},
		{"capacity nil agent", http.MethodGet, "/workspaces/" + ws + "/runtime/capacity?agent_id=" + uuid.Nil.String(), ""},
		{"acquire invalid workspace", http.MethodPost, "/workspaces/nope/runtime/bindings/b/reservations" + owner, acquire},
		{"acquire without owner", http.MethodPost, "/workspaces/" + ws + "/runtime/bindings/b/reservations", acquire},
		{"acquire missing ttl", http.MethodPost, reservations, strings.Replace(acquire, `,"ttl_seconds":60`, "", 1)},
		{"acquire client-chosen pools", http.MethodPost, reservations, strings.Replace(acquire, `"ttl_seconds":60`, `"ttl_seconds":60,"pools":["x"]`, 1)},
		{"get without owner", http.MethodGet, "/workspaces/" + ws + "/runtime/bindings/b/reservations/" + rid, ""},
		{"get invalid reservation", http.MethodGet, "/workspaces/" + ws + "/runtime/bindings/b/reservations/nope" + owner, ""},
		{"get nil reservation", http.MethodGet, "/workspaces/" + ws + "/runtime/bindings/b/reservations/" + uuid.Nil.String() + owner, ""},
		{"consume invalid reservation", http.MethodPost, "/workspaces/" + ws + "/runtime/bindings/b/reservations/nope/consume" + owner, `{"fence":1,"checkout_generation":1,"run_lease_seconds":60}`},
		{"consume missing lease", http.MethodPost, reservation + "/consume" + owner, `{"fence":1,"checkout_generation":1}`},
		{"release without owner", http.MethodPost, reservation + "/release", `{"fence":1,"checkout_generation":1,"stopped":true,"proof":{"kind":"no_child"}}`},
		{"release missing proof", http.MethodPost, reservation + "/release" + owner, `{"fence":1,"checkout_request_id":null,"checkout_generation":1,"stopped":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "api_key", "refusal must not echo the rejected field value")
		})
	}
}
