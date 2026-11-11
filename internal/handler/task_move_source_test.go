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

	"github.com/entire-vc/evc-mesh/internal/service"
)

// moveWithSource posts a move whose body carries source (omitted when empty)
// and reports the response code and the source the service received ("" when
// the service was never called).
func moveWithSource(t *testing.T, source string, omit bool) (code int, got string, called bool) {
	t.Helper()
	h, e := setupTaskTest(&MockTaskService{
		MoveTaskFunc: func(_ context.Context, _ uuid.UUID, in service.MoveTaskInput) error {
			called, got = true, in.Source
			return nil
		},
	})
	body := `{"status_id":"` + uuid.New().String() + `"`
	if !omit {
		body += `,"source":"` + source + `"`
	}
	body += `}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/move")
	c.SetParamNames("task_id")
	c.SetParamValues(uuid.New().String())
	require.NoError(t, h.MoveTask(c))
	code = rec.Code
	return code, got, called
}

// A client must not claim a source the server writes for its own
// moves — that forges the audit trail, and auto_transition also changes the
// move itself. The service is never reached, so no activity row is written.
func TestTaskHandler_MoveTask_RefusesReservedSource(t *testing.T) {
	for _, src := range []string{
		"auto_transition", "auto_transition.parent", "auto_transition.dependency",
		"checkout", "lease_reaper.expired", "lease_reaper.unleased", "lease_reaper",
		"finish_task", "monitor_due_sweep", "human_gate_release", "transition_receipt", "system",
	} {
		code, _, called := moveWithSource(t, src, false)
		assert.Equal(t, http.StatusBadRequest, code, src)
		assert.False(t, called, "%s: a refused source must not reach MoveTask", src)
	}
}

func TestTaskHandler_MoveTask_RefusesMalformedSource(t *testing.T) {
	for _, src := range []string{"MCP", "-mcp", "mcp runner", "mcp;drop", strings.Repeat("a", 65), "мcp"} {
		code, _, called := moveWithSource(t, src, false)
		assert.Equal(t, http.StatusBadRequest, code, src)
		assert.False(t, called, src)
	}
}

func TestTaskHandler_MoveTask_PassesClientSource(t *testing.T) {
	for _, src := range []string{"mcp", "api", "ui", "mesh-runner", "dispatcher.session_exit", "board/fastlane", strings.Repeat("a", 64)} {
		code, got, called := moveWithSource(t, src, false)
		assert.Equal(t, http.StatusOK, code, src)
		require.True(t, called, src)
		assert.Equal(t, src, got)
	}
	code, got, called := moveWithSource(t, "", true)
	assert.Equal(t, http.StatusOK, code)
	require.True(t, called)
	assert.Equal(t, "api", got, "an omitted source stays api")
	code, got, _ = moveWithSource(t, "", false)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "api", got, "an empty source stays api")
}
