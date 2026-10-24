package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

func chatRunnerCall(t *testing.T, fn echo.HandlerFunc, method, target string, agent uuid.UUID, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, target, http.NoBody)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if agent != uuid.Nil {
		c.Set(mw.ContextKeyAgentID, agent)
	}
	for k, v := range params {
		c.SetParamNames(append(c.ParamNames(), k)...)
		c.SetParamValues(append(c.ParamValues(), v)...)
	}
	require.NoError(t, fn(c))
	return rec
}

// The repo is never reached on these paths, so a nil one is enough.
func TestChatRunnerHandlerRefusesNonAgentCallers(t *testing.T) {
	h := NewChatRunnerHandler(nil)
	id := uuid.NewString()
	for name, fn := range map[string]echo.HandlerFunc{"commands": h.Commands, "ack": h.AckCommand, "history": h.History} {
		rec := chatRunnerCall(t, fn, http.MethodGet, "/", uuid.Nil, map[string]string{"command_id": id, "thread_id": id})
		require.Equal(t, http.StatusForbidden, rec.Code, name)
		require.Contains(t, rec.Body.String(), "chat_runner_forbidden", name)
	}
}

func TestChatRunnerHandlerRejectsMalformedInput(t *testing.T) {
	h := NewChatRunnerHandler(nil)
	agent := uuid.New()
	good := uuid.NewString()
	cases := []struct {
		name   string
		fn     echo.HandlerFunc
		target string
		params map[string]string
	}{
		{"agents", h.Commands, "/?agents=nope", nil},
		{"runner", h.Commands, "/?runner_id=nope", nil},
		{"command id", h.AckCommand, "/", map[string]string{"command_id": "nope"}},
		{"thread id", h.History, "/", map[string]string{"thread_id": "nope"}},
		{"limit", h.History, "/?limit=0", map[string]string{"thread_id": good}},
		{"limit text", h.History, "/?limit=x", map[string]string{"thread_id": good}},
		{"before", h.History, "/?before=nope", map[string]string{"thread_id": good}},
	}
	for _, tc := range cases {
		rec := chatRunnerCall(t, tc.fn, http.MethodGet, tc.target, agent, tc.params)
		require.Equal(t, http.StatusBadRequest, rec.Code, tc.name)
	}
}

// Naming a runner_id without that runner's credential is refused before the
// repo is touched: the id is an address, not an identity.
func TestChatRunnerHandlerRunnerIDNeedsCredential(t *testing.T) {
	h := NewChatRunnerHandler(nil)
	agent, runner := uuid.New(), uuid.NewString()
	rec := chatRunnerCall(t, h.Commands, http.MethodGet, "/?runner_id="+runner, agent, nil)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "chat_runner_forbidden")
}
