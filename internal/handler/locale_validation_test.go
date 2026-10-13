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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// An unsupported locale is rejected with 400 before any service is touched, so
// these handlers are built without one: reaching the service would panic.
func TestUpdateMe_UnsupportedLocaleIs400(t *testing.T) {
	for _, bad := range []string{"de", "", "EN", "ru-RU"} {
		e := echo.New()
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/auth/me",
			strings.NewReader(`{"locale":"`+bad+`"}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.Set(mw.ContextKeyUserID, uuid.New())

		require.NoError(t, NewAuthHandler(nil).UpdateMe(c), "locale %q", bad)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "locale %q", bad)
		assert.Contains(t, rec.Body.String(), "locale")
	}
}

func TestWorkspaceUpdate_UnsupportedDefaultLocaleIs400(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"default_locale":"fr"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, NewWorkspaceHandler(nil).Update(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "default_locale")
}

func patchMe(t *testing.T, h *AuthHandler, userID uuid.UUID, body string) (code int, out map[string]any) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/auth/me", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(mw.ContextKeyUserID, userID)
	require.NoError(t, h.UpdateMe(c))

	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The language survives a profile edit that does not mention it, and a
// locale-only body does not need a name (the selector saves on change).
func TestUpdateMe_LocaleOnlyAndWithName(t *testing.T) {
	repo := newAuthTestUserRepo()
	id := uuid.New()
	require.NoError(t, repo.Create(context.Background(), &domain.User{
		ID: id, Email: "l@example.com", Name: "Lee", Username: "lee", IsActive: true, Locale: "en",
	}))
	h, _ := newAuthHandlerTest(repo)

	code, out := patchMe(t, h, id, `{"locale":"ru"}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ru", out["locale"])
	assert.Equal(t, true, out["locale_set_explicitly"])
	assert.Equal(t, "Lee", out["name"], "a locale-only body must not touch the name")

	code, out = patchMe(t, h, id, `{"name":"Lee Two"}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ru", out["locale"], "a name edit must not reset the language")

	code, out = patchMe(t, h, id, `{"name":"Lee Three","locale":"en"}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "en", out["locale"])
	assert.Equal(t, "Lee Three", out["name"])

	code, _ = patchMe(t, h, uuid.New(), `{"locale":"ru"}`)
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = patchMe(t, h, id, `{"name":"","locale":"ru"}`)
	assert.Equal(t, http.StatusBadRequest, code, "an invalid name still fails the whole edit")
}

func TestWorkspaceUpdate_SetsDefaultLocale(t *testing.T) {
	wsID := uuid.New()
	existing := &domain.Workspace{ID: wsID, Name: "W", Slug: "w", OwnerID: uuid.New(), DefaultLocale: "en"}
	mockSvc := &MockWorkspaceService{
		GetByIDFunc: func(context.Context, uuid.UUID) (*domain.Workspace, error) { return existing, nil },
		UpdateFunc: func(_ context.Context, ws *domain.Workspace) error {
			assert.Equal(t, "ru", ws.DefaultLocale)
			return nil
		},
	}
	h, e := setupWorkspaceTest(mockSvc)
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"default_locale":"ru"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(wsID.String())

	require.NoError(t, h.Update(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"default_locale":"ru"`)
}
