package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// The /recurring routes are keyed on :recurring_id rather than :id so that
// WorkspaceRLS can resolve the schedule's workspace from the path and the
// membership guard applies — /recurring/:id used to be reachable across tenants
// because nothing in the path told the guard which tenant it belonged to.
//
// A rename like that fails in exactly one way: the router and the handler stop
// agreeing on the parameter's name, every request 400s with "invalid id", and no
// existing test notices because they all call the handler directly with a
// parameter they set themselves. These tests pin both halves — that the handler
// reads :recurring_id, and that the router registers it under that name.

// recurringSvcStub records the id each method was called with, which is the only
// thing these tests are about.
type recurringSvcStub struct {
	gotID uuid.UUID
}

func (s *recurringSvcStub) Create(context.Context, service.CreateRecurringInput) (*domain.RecurringSchedule, error) {
	return &domain.RecurringSchedule{ID: uuid.New()}, nil
}

func (s *recurringSvcStub) GetByID(_ context.Context, id uuid.UUID) (*domain.RecurringSchedule, error) {
	s.gotID = id
	return &domain.RecurringSchedule{ID: id}, nil
}

func (s *recurringSvcStub) Update(_ context.Context, id uuid.UUID, _ service.UpdateRecurringInput) (*domain.RecurringSchedule, error) {
	s.gotID = id
	return &domain.RecurringSchedule{ID: id}, nil
}

func (s *recurringSvcStub) Delete(_ context.Context, id uuid.UUID) error {
	s.gotID = id
	return nil
}

func (s *recurringSvcStub) ListByProject(context.Context, uuid.UUID, pagination.Params) (*pagination.Page[domain.RecurringSchedule], error) {
	return &pagination.Page[domain.RecurringSchedule]{}, nil
}

func (s *recurringSvcStub) TriggerNow(_ context.Context, id uuid.UUID) (*domain.Task, error) {
	s.gotID = id
	return &domain.Task{ID: uuid.New()}, nil
}

func (s *recurringSvcStub) GetHistory(_ context.Context, id uuid.UUID, _ pagination.Params) (*pagination.Page[domain.RecurringInstanceSummary], error) {
	s.gotID = id
	return &pagination.Page[domain.RecurringInstanceSummary]{}, nil
}

func (s *recurringSvcStub) RunDue(context.Context) (int, error) { return 0, nil }

// TestRecurringHandler_ReadsRecurringIDParam: every schedule-scoped method must
// take its id from :recurring_id, the name the router now registers.
func TestRecurringHandler_ReadsRecurringIDParam(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		body    string
		invoke  func(*RecurringHandler) func(echo.Context) error
		wantOK  int
		hasBody bool
	}{
		{"GetByID", http.MethodGet, "", func(h *RecurringHandler) func(echo.Context) error { return h.GetByID }, http.StatusOK, true},
		{"Update", http.MethodPatch, `{"title_template":"x"}`, func(h *RecurringHandler) func(echo.Context) error { return h.Update }, http.StatusOK, true},
		{"Delete", http.MethodDelete, "", func(h *RecurringHandler) func(echo.Context) error { return h.Delete }, http.StatusNoContent, false},
		{"Trigger", http.MethodPost, "", func(h *RecurringHandler) func(echo.Context) error { return h.Trigger }, http.StatusCreated, true},
		{"History", http.MethodGet, "", func(h *RecurringHandler) func(echo.Context) error { return h.History }, http.StatusOK, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &recurringSvcStub{}
			h := NewRecurringHandler(stub)
			scheduleID := uuid.New()

			e := echo.New()
			var req *http.Request
			if tc.body != "" {
				req = httptest.NewRequest(tc.method, "/", strings.NewReader(tc.body))
				req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			} else {
				req = httptest.NewRequest(tc.method, "/", http.NoBody)
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("recurring_id")
			c.SetParamValues(scheduleID.String())

			require.NoError(t, tc.invoke(h)(c))
			require.Equal(t, tc.wantOK, rec.Code, "body: %s", rec.Body.String())
			assert.Equal(t, scheduleID, stub.gotID,
				"the handler did not read the schedule id from :recurring_id")

			if tc.hasBody {
				var decoded map[string]any
				assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
			}
		})
	}
}

// TestRecurringHandler_RejectsMalformedID: the id still has to be a uuid.
func TestRecurringHandler_RejectsMalformedID(t *testing.T) {
	h := NewRecurringHandler(&recurringSvcStub{})
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.SetParamNames("recurring_id")
	c.SetParamValues("not-a-uuid")

	require.NoError(t, h.GetByID(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestRecurringRoutesUseRecurringIDParam is the other half: the router must
// register the name the handler reads. Reading main.go rather than asserting from
// memory is the point — this is the failure mode the rename introduced.
func TestRecurringRoutesUseRecurringIDParam(t *testing.T) {
	src, err := os.ReadFile("../../cmd/api/main.go")
	require.NoError(t, err)

	routes := regexp.MustCompile(`api\.[A-Z]+\("(/recurring/[^"]*)"`).FindAllStringSubmatch(string(src), -1)
	require.NotEmpty(t, routes, "no /recurring routes found — has the router changed?")

	for _, m := range routes {
		assert.Contains(t, m[1], ":recurring_id",
			"%s is keyed on a parameter WorkspaceRLS cannot resolve a workspace from, "+
				"which is how it was reachable across tenants", m[1])
		assert.NotContains(t, m[1], ":id/",
			"%s still carries the old :id parameter; the handler reads :recurring_id", m[1])
	}
}
