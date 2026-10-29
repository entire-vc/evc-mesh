package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestBindWaitFor_UntilAt(t *testing.T) {
	bind := func(body string) (*waitForBody, int, string) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		c := echo.New().NewContext(req, httptest.NewRecorder())
		var b waitForBody
		if apiErr := bindWaitFor(c, &b); apiErr != nil {
			return nil, apiErr.Code, apiErr.ErrorCode
		}
		return &b, 0, ""
	}

	for _, v := range []string{`"6h"`, `"tomorrow"`, `"2026-10-30T03:00:00"`, `"2026-10-30"`, `42`} {
		t.Run("refused "+v, func(t *testing.T) {
			_, code, errCode := bind(`{"on":"time","until_at":` + v + `}`)
			assert.Equal(t, http.StatusUnprocessableEntity, code)
			assert.Equal(t, domain.CodeNaiveTime, errCode)
		})
	}

	b, code, _ := bind(`{"on":"time","until_at":"2026-10-30T03:00:00+03:00"}`)
	require.Zero(t, code)
	require.NotNil(t, b.UntilAt.Time)
	assert.Equal(t, "+03:00", b.UntilAt.Time.Format("Z07:00"))

	b, code, _ = bind(`{"on":"card"}`)
	require.Zero(t, code)
	assert.Nil(t, b.UntilAt.Time)

	_, code, _ = bind(`{"on":`)
	assert.Equal(t, http.StatusBadRequest, code)
}
