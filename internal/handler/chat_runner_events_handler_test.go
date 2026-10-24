package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// The request-shape refusals of the runner events route. They return before the
// service is touched, so no database is involved (nil service on purpose: a
// refusal that reached it would panic).
func TestChatRunnerEvents_RefusesMalformedRequests(t *testing.T) {
	asAgent := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
			c.Set(mw.ContextKeyAgentID, uuid.New())
			c.Set(mw.ContextKeyWorkspaceID, uuid.New())
			return next(c)
		}
	}
	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	e.POST("/turns/:id/events", NewChatRunnerEventsHandler(nil).Events, asAgent)
	bare := echo.New()
	bare.HTTPErrorHandler = NewHTTPErrorHandler(bare.DefaultHTTPErrorHandler)
	bare.POST("/turns/:id/events", NewChatRunnerEventsHandler(nil).Events)

	do := func(srv *echo.Echo, turn, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/turns/"+turn+"/events", strings.NewReader(body))
		req.Header.Set("X-Chat-Protocol", "1")
		req.Header.Set(chatRunnerIDHeader, uuid.NewString())
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	turn := uuid.NewString()

	assert.Equal(t, http.StatusUnauthorized, do(bare, turn, `{}`), "not an agent key")
	assert.Equal(t, http.StatusNotFound, do(e, "not-a-uuid", `{}`))
	assert.Equal(t, http.StatusNotFound, do(e, uuid.Nil.String(), `{}`))
	assert.Equal(t, http.StatusBadRequest, do(e, turn, `{not json`))
	assert.Equal(t, http.StatusBadRequest, do(e, turn, `{"claim_token":"t","events":[]}`))
	huge := `{"claim_token":"t","events":[{"seq":1,"type":"text_delta","text":"` + strings.Repeat("x", postgres.ChatMaxBatchBytes) + `"}]}`
	assert.Equal(t, http.StatusRequestEntityTooLarge, do(e, turn, huge))
}
