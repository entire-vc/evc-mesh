package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// NewHTTPErrorHandler wraps Echo's own error handler so that a handler
// returning *apierror.Error gets the status code and body that type actually
// carries, instead of the 500 Echo gives anything that isn't *echo.HTTPError.
//
// Every handler in this codebase returns *apierror.Error for the ordinary,
// expected failures — bad input, missing auth, not found — and nothing ever
// taught Echo what that type means: DefaultHTTPErrorHandler type-switches on
// *echo.HTTPError only, so *apierror.Error fell through to its generic
// fallback, which is a bare 500. A caller sending a malformed request saw
// "the server is broken" instead of "your request is wrong" — reproduced
// live on GET /me/mentions and /me/document-mentions with no workspace_id
// (#fe3ae257), and pre-existing on /me/tasks the same way; this fixes it for
// every route at once rather than one handler at a time.
//
// fallback is Echo's own DefaultHTTPErrorHandler (captured by the caller
// before installing this one), so anything that is not *apierror.Error keeps
// behaving exactly as it did.
func NewHTTPErrorHandler(fallback echo.HTTPErrorHandler) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		var apiErr *apierror.Error
		if !errors.As(err, &apiErr) {
			fallback(err, c)
			return
		}

		if c.Response().Committed {
			return
		}

		var writeErr error
		if c.Request().Method == http.MethodHead {
			writeErr = c.NoContent(apiErr.StatusCode())
		} else {
			writeErr = c.JSON(apiErr.StatusCode(), apiErr)
		}
		if writeErr != nil {
			c.Logger().Error(writeErr)
		}
	}
}
