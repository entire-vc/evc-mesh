package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// Keep the client contract under test independently of SQL/service tests.
type generationHandlerService struct {
	service.TaskService
	t                        *testing.T
	session, request, holder uuid.UUID
	called                   bool
}

func (s *generationHandlerService) CheckoutTask(_ context.Context, id uuid.UUID, ttl int, _ map[string]interface{}, scope ...domain.CheckoutScope) (*service.CheckoutResult, error) {
	s.called = true
	require.Equal(s.t, 30, ttl)
	require.Len(s.t, scope, 1)
	require.Equal(s.t, &s.session, scope[0].SessionID)
	require.Equal(s.t, &s.request, scope[0].RequestID)
	return &service.CheckoutResult{TaskID: id, SessionID: &s.session, RequestID: &s.request, Generation: 7}, nil
}

func (s *generationHandlerService) SelfReleaseCheckout(_ context.Context, _ uuid.UUID, expected ...domain.CheckoutExpectation) error {
	s.called = true
	require.Len(s.t, expected, 1)
	require.Equal(s.t, &s.session, expected[0].SessionID)
	require.EqualValues(s.t, 7, expected[0].Generation)
	return nil
}

func (s *generationHandlerService) ForceReleaseCheckout(_ context.Context, _ uuid.UUID, expected ...domain.CheckoutExpectation) error {
	require.NoError(s.t, s.SelfReleaseCheckout(context.Background(), uuid.Nil, expected...))
	require.Equal(s.t, &s.holder, expected[0].Holder)
	require.Equal(s.t, "stopped session", expected[0].Reason)
	return nil
}

func (s *generationHandlerService) ExtendCheckout(_ context.Context, id, token uuid.UUID, ttl int, expected ...domain.CheckoutExpectation) (*service.CheckoutResult, error) {
	require.NoError(s.t, s.SelfReleaseCheckout(context.Background(), id, expected...))
	require.Equal(s.t, uuid.Nil, token)
	require.Equal(s.t, 30, ttl)
	return &service.CheckoutResult{TaskID: id, Generation: 7}, nil
}

func TestTaskHandler_CheckoutGenerationContract(t *testing.T) {
	for _, operation := range []string{"acquire", "release", "extend", "recovery"} {
		t.Run(operation, func(t *testing.T) {
			svc := &generationHandlerService{t: t, session: uuid.New(), request: uuid.New(), holder: uuid.New()}
			h := NewTaskHandler(svc)
			body, err := json.Marshal(map[string]interface{}{"session_id": svc.session, "request_id": svc.request, "generation": 7, "previous_holder": svc.holder, "reason": "stopped session", "ttl_minutes": 30})
			require.NoError(t, err)
			method := map[string]string{"acquire": http.MethodPost, "release": http.MethodDelete, "extend": http.MethodPatch, "recovery": http.MethodDelete}[operation]
			req := httptest.NewRequest(method, "/", strings.NewReader(string(body)))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			if operation == "recovery" {
				req.URL.RawQuery = "force=true"
			}
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(req, rec)
			c.SetParamNames("task_id")
			c.SetParamValues(uuid.New().String())
			switch operation {
			case "acquire":
				err = h.Checkout(c)
			case "release", "recovery":
				err = h.ReleaseCheckout(c)
			case "extend":
				err = h.ExtendCheckout(c)
			}
			require.NoError(t, err)
			require.True(t, svc.called)
			if method == http.MethodDelete {
				require.Equal(t, http.StatusNoContent, rec.Code)
			} else {
				require.Equal(t, http.StatusOK, rec.Code)
				var response service.CheckoutResult
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
				require.EqualValues(t, 7, response.Generation)
			}
		})
	}
}
