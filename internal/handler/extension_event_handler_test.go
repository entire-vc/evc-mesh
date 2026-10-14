package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type fakeExtEventService struct {
	subscribeErr error
	got          service.CreateExtensionEventSubscriptionInput
	list         []domain.ExtensionEventSubscription
	unsubErr     error
}

func (f *fakeExtEventService) Subscribe(_ context.Context, in service.CreateExtensionEventSubscriptionInput) (*domain.ExtensionEventSubscription, error) {
	f.got = in
	if f.subscribeErr != nil {
		return nil, f.subscribeErr
	}
	return &domain.ExtensionEventSubscription{ID: uuid.New(), ConnectionID: in.ConnectionID, Event: in.Event, Delivery: in.Delivery}, nil
}

func (f *fakeExtEventService) ListByConnection(context.Context, uuid.UUID) ([]domain.ExtensionEventSubscription, error) {
	return f.list, nil
}

func (f *fakeExtEventService) Unsubscribe(context.Context, uuid.UUID) error { return f.unsubErr }

func extEventCall(t *testing.T, h echo.HandlerFunc, method, body string, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	var names, values []string
	for k, v := range params {
		names, values = append(names, k), append(values, v)
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	require.NoError(t, h(c))
	return rec
}

func TestExtensionEventHandler_ListEvents(t *testing.T) {
	rec := extEventCall(t, NewExtensionEventHandler(&fakeExtEventService{}).ListEvents, http.MethodGet, "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"version":`)
	assert.Contains(t, rec.Body.String(), extensions.EventTaskUpdated)
}

func TestExtensionEventHandler_Subscribe(t *testing.T) {
	connID := uuid.New().String()
	body := `{"event":"task.updated","delivery":"webhook","target":"https://x.example/h","secret_ref":"s"}`

	svc := &fakeExtEventService{}
	rec := extEventCall(t, NewExtensionEventHandler(svc).Subscribe, http.MethodPost, body, map[string]string{"int_id": connID})
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "task.updated", svc.got.Event)
	assert.Equal(t, domain.ExtensionDeliveryWebhook, svc.got.Delivery)

	// The service's apierror status reaches the caller unchanged.
	svc = &fakeExtEventService{subscribeErr: apierror.ForbiddenWithDetails("not declared", "task.updated")}
	rec = extEventCall(t, NewExtensionEventHandler(svc).Subscribe, http.MethodPost, body, map[string]string{"int_id": connID})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	svc = &fakeExtEventService{subscribeErr: apierror.BadRequestWithDetails("unknown event", "x")}
	rec = extEventCall(t, NewExtensionEventHandler(svc).Subscribe, http.MethodPost, body, map[string]string{"int_id": connID})
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = extEventCall(t, NewExtensionEventHandler(&fakeExtEventService{}).Subscribe, http.MethodPost, body, map[string]string{"int_id": "nope"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = extEventCall(t, NewExtensionEventHandler(&fakeExtEventService{}).Subscribe, http.MethodPost, "{", map[string]string{"int_id": connID})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestExtensionEventHandler_ListAndUnsubscribe(t *testing.T) {
	connID := uuid.New().String()
	h := NewExtensionEventHandler(&fakeExtEventService{})

	rec := extEventCall(t, h.List, http.MethodGet, "", map[string]string{"int_id": connID})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"items":[]}`, rec.Body.String(), "no subscriptions is an empty list, not null")
	assert.Equal(t, http.StatusBadRequest, extEventCall(t, h.List, http.MethodGet, "", map[string]string{"int_id": "x"}).Code)

	subID := uuid.New().String()
	assert.Equal(t, http.StatusNoContent, extEventCall(t, h.Unsubscribe, http.MethodDelete, "", map[string]string{"sub_id": subID}).Code)
	assert.Equal(t, http.StatusBadRequest, extEventCall(t, h.Unsubscribe, http.MethodDelete, "", map[string]string{"sub_id": "x"}).Code)
	missing := NewExtensionEventHandler(&fakeExtEventService{unsubErr: apierror.NotFound("Subscription")})
	assert.Equal(t, http.StatusNotFound, extEventCall(t, missing.Unsubscribe, http.MethodDelete, "", map[string]string{"sub_id": subID}).Code)
}
