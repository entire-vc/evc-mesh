package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// Without a snapshot store the read routes say so instead of answering
// "no runner" for every thread.
func TestChatRuntimeSnapshot_RoutesWithoutStoreAre503(t *testing.T) {
	h := NewAgentChatHandler(nil, nil, nil)
	for name, fn := range map[string]echo.HandlerFunc{"runtime": h.ThreadRuntime, "sessions": h.RunnerSessions} {
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
		require.NoError(t, fn(c), name)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, name)
	}
}
