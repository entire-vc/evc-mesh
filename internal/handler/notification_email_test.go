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
)

// recordingNotificationService is a minimal NotificationService double for the
// tests in this file — unlike mockNotificationService in
// notification_prefs_tenancy_test.go, it records the preference actually
// handed to UpsertPreferences so a test can assert on Config, and lets
// EmailAvailable be set per test.
type recordingNotificationService struct {
	emailAvailable bool
	upserted       *domain.NotificationPreference
	upsertErr      error
}

func (m *recordingNotificationService) Notify(context.Context, domain.NotificationEvent) {}
func (m *recordingNotificationService) GetPreferences(context.Context, uuid.UUID) ([]domain.NotificationPreference, error) {
	return nil, nil
}
func (m *recordingNotificationService) UpsertPreferences(_ context.Context, pref *domain.NotificationPreference) (*domain.NotificationPreference, error) {
	if m.upsertErr != nil {
		return nil, m.upsertErr
	}
	m.upserted = pref
	return pref, nil
}
func (m *recordingNotificationService) DeletePreference(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (m *recordingNotificationService) ListUnread(context.Context, uuid.UUID) ([]domain.Notification, error) {
	return nil, nil
}
func (m *recordingNotificationService) CountUnread(context.Context, uuid.UUID) (int, error) {
	return 0, nil
}
func (m *recordingNotificationService) MarkRead(context.Context, uuid.UUID, []uuid.UUID) error {
	return nil
}
func (m *recordingNotificationService) MarkAllRead(context.Context, uuid.UUID) error { return nil }
func (m *recordingNotificationService) EmailAvailable() bool                         { return m.emailAvailable }

// putPreferencesNoTenancyGuard serves PUT /notifications/preferences without
// mw.RequireBodyWorkspace in front of it — that guard is exercised in
// notification_prefs_tenancy_test.go; these tests are about the handler's own
// request-body validation, which runs regardless of the guard.
func putPreferencesNoTenancyGuard(t *testing.T, svc *recordingNotificationService, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewNotificationHandler(svc)

	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("user_id", uuid.New())
			return next(c)
		}
	})
	e.PUT("/api/v1/notifications/preferences", h.UpdatePreferences)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/notifications/preferences", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestUpdatePreferences_InvalidEmailInConfigIsRejected(t *testing.T) {
	svc := &recordingNotificationService{}
	body := `{"workspace_id":"` + uuid.New().String() + `","channel":"email","events":["comment.created"],"config":{"email":"not-an-email"}}`

	rec := putPreferencesNoTenancyGuard(t, svc, body)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, svc.upserted, "an invalid address must not reach UpsertPreferences")
}

func TestUpdatePreferences_ValidEmailInConfigIsStored(t *testing.T) {
	svc := &recordingNotificationService{}
	body := `{"workspace_id":"` + uuid.New().String() + `","channel":"email","events":["comment.created"],"config":{"email":"user@example.com"}}`

	rec := putPreferencesNoTenancyGuard(t, svc, body)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, svc.upserted)
	assert.JSONEq(t, `{"email":"user@example.com"}`, string(svc.upserted.Config))
}

func TestUpdatePreferences_EmptyConfigLeavesConfigNilForAccountDefault(t *testing.T) {
	svc := &recordingNotificationService{}
	body := `{"workspace_id":"` + uuid.New().String() + `","channel":"email","events":["comment.created"]}`

	rec := putPreferencesNoTenancyGuard(t, svc, body)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, svc.upserted)
	assert.Empty(t, svc.upserted.Config, "no config should be stored when the caller sends none, so dispatch falls back to the account email")
}

// --- GET /notifications/email-availability -----------------------------------

func TestGetEmailAvailability_ReflectsService(t *testing.T) {
	for _, available := range []bool{true, false} {
		svc := &recordingNotificationService{emailAvailable: available}
		h := NewNotificationHandler(svc)

		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/email-availability", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		require.NoError(t, h.GetEmailAvailability(c))
		assert.Equal(t, http.StatusOK, rec.Code)
		wantBody := `{"available":false}`
		if available {
			wantBody = `{"available":true}`
		}
		assert.JSONEq(t, wantBody, rec.Body.String())
	}
}
