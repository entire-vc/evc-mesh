package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

func TestChatControllerAuth(t *testing.T) {
	run := func(header string) int {
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
		if header != "" {
			req.Header.Set(ChatControllerTokenHeader, header)
		}
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		h := ChatControllerAuth()(func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
		if err := h(c); err != nil {
			t.Fatal(err)
		}
		return rec.Code
	}

	t.Setenv(EnvChatControllerToken, "")
	assert.Equal(t, http.StatusUnauthorized, run(""), "unconfigured secret refuses an empty header")
	assert.Equal(t, http.StatusUnauthorized, run("anything"), "unconfigured secret refuses everything")

	t.Setenv(EnvChatControllerToken, "s3cret")
	assert.Equal(t, http.StatusUnauthorized, run(""))
	assert.Equal(t, http.StatusUnauthorized, run("wrong"))
	assert.Equal(t, http.StatusNoContent, run("s3cret"))
}
