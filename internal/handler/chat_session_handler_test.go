package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// chatSvcFake is the service boundary of the handler.
type chatSvcFake struct {
	service.ChatSessionService
	issued  *service.ChatSessionIssued
	revoked int64
	err     error
	gotBind service.ChatSessionBinding
	gotRun  string
}

func (f *chatSvcFake) Issue(_ context.Context, b service.ChatSessionBinding) (*service.ChatSessionIssued, error) {
	f.gotBind = b
	return f.issued, f.err
}

func (f *chatSvcFake) Renew(_ context.Context, _ uuid.UUID, runnerID string, _ int64) (*service.ChatSessionIssued, error) {
	f.gotRun = runnerID
	return f.issued, f.err
}

func (f *chatSvcFake) RevokeSession(context.Context, uuid.UUID) (int64, error) {
	return f.revoked, f.err
}

func chatSessionCall(t *testing.T, fn echo.HandlerFunc, body, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("session_id")
	c.SetParamValues(sessionID)
	require.NoError(t, fn(c))
	return rec
}

func TestChatSessionHandler(t *testing.T) {
	sess := uuid.New()
	issued := &service.ChatSessionIssued{
		Token: "cst_x", TokenID: uuid.New(), ExpiresAt: time.Now().Add(time.Minute),
		Binding: service.ChatSessionBinding{ChatSessionID: sess, ExecutionEpoch: 2},
	}

	t.Run("issue", func(t *testing.T) {
		f := &chatSvcFake{issued: issued}
		rec := chatSessionCall(t, NewChatSessionHandler(f).Issue, `{"runner_id":"  r1 ","execution_epoch":2}`, "")
		assert.Equal(t, http.StatusCreated, rec.Code)
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		assert.Contains(t, rec.Body.String(), "cst_x")
		assert.Equal(t, "r1", f.gotBind.RunnerID, "runner id is trimmed")
		assert.Equal(t, http.StatusBadRequest, chatSessionCall(t, NewChatSessionHandler(f).Issue, `{`, "").Code)
		f.err = apierror.Forbidden("no")
		assert.Equal(t, http.StatusForbidden, chatSessionCall(t, NewChatSessionHandler(f).Issue, `{}`, "").Code)
	})

	t.Run("renew", func(t *testing.T) {
		f := &chatSvcFake{issued: issued}
		h := NewChatSessionHandler(f)
		rec := chatSessionCall(t, h.Renew, `{"runner_id":" r2 ","execution_epoch":2}`, sess.String())
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		assert.Equal(t, "r2", f.gotRun)
		assert.Equal(t, http.StatusBadRequest, chatSessionCall(t, h.Renew, `{}`, "nope").Code)
		assert.Equal(t, http.StatusBadRequest, chatSessionCall(t, h.Renew, `{}`, uuid.Nil.String()).Code)
		assert.Equal(t, http.StatusBadRequest, chatSessionCall(t, h.Renew, `{`, sess.String()).Code)
		f.err = apierror.Forbidden("no")
		assert.Equal(t, http.StatusForbidden, chatSessionCall(t, h.Renew, `{}`, sess.String()).Code)
	})

	t.Run("revoke", func(t *testing.T) {
		f := &chatSvcFake{revoked: 2}
		h := NewChatSessionHandler(f)
		rec := chatSessionCall(t, h.Revoke, ``, sess.String())
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"revoked":2`)
		assert.Equal(t, http.StatusBadRequest, chatSessionCall(t, h.Revoke, ``, "nope").Code)
		f.err = apierror.Forbidden("no")
		assert.Equal(t, http.StatusForbidden, chatSessionCall(t, h.Revoke, ``, sess.String()).Code)
	})
}
